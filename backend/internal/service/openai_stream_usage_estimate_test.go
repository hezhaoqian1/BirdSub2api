//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestStreamOutputTextMeter_ChatChunks(t *testing.T) {
	m := &streamOutputTextMeter{}
	m.observeChatChunk(`{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`)
	m.observeChatChunk(`{"choices":[{"index":0,"delta":{"content":"你好世界"}}]}`)
	m.observeChatChunk(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"run","arguments":"{\"a\":1}"}}]}}]}`)
	m.observeChatChunk(`{"usage":{"prompt_tokens":10}}`) // 没有 delta 的分片不计
	// 4 个汉字 ×0.7 + ("think"+"run"+`{"a":1}`=15 字节)/4
	require.Equal(t, 4, m.cjkRunes)
	require.Equal(t, 15, m.otherBytes)
	require.Equal(t, 7, m.tokens()) // ceil(2.8 + 3.75)
}

func TestStreamOutputTextMeter_ResponsesEvents(t *testing.T) {
	m := &streamOutputTextMeter{}
	m.observeResponsesEvent([]byte(`{"type":"response.output_text.delta","delta":"hello world!"}`), "response.output_text.delta")
	m.observeResponsesEvent([]byte(`{"type":"response.function_call_arguments.delta","delta":"{}"}`), "response.function_call_arguments.delta")
	m.observeResponsesEvent([]byte(`{"type":"response.created","response":{"instructions":"ignored"}}`), "response.created")
	require.Equal(t, 14, m.otherBytes)
	require.Equal(t, 4, m.tokens())
}

func TestEstimateRequestInputTokens_SkipsInlineDataAndKeys(t *testing.T) {
	base64Blob := strings.Repeat("QUJD", 1000) // 4000 字节、无空白
	body := []byte(`{"model":"deepseek-v4-pro","messages":[` +
		`{"role":"system","content":"You are helpful."},` +
		`{"role":"user","content":[{"type":"text","text":"看这张图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64Blob + `"}}]},` +
		`{"role":"user","content":"` + base64Blob + `"}]}`)
	// "You are helpful."=16 字节 → 4；"看这张图"=4 汉字 → 2.8；合计 ceil(6.8)=7
	require.Equal(t, 7, estimateRequestInputTokens(body))
	require.Zero(t, estimateRequestInputTokens([]byte("not json")))
}

func TestSettleOpenAIStreamUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("api_key", &APIKey{ID: 987654})
	apiKeyID := getAPIKeyIDFromContext(c)
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("abcd ", 80) + `"}]}`) // 400 字节 → 100

	meter := &streamOutputTextMeter{}
	meter.addText(strings.Repeat("x", 40)) // 10 tokens

	// 上游给了 usage：不估算，只记下缓存命中率。
	if apiKeyID > 0 {
		real := OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 900, OutputTokens: 50}
		require.False(t, settleOpenAIStreamUsage(c, &real, meter, body, "estimate-model", 1, "test"))
		require.Equal(t, 1000, real.InputTokens)
	}

	// 没有 usage 但已有输出：估算。有历史时按 0.9 拆缓存，没有历史时整段按缓存读。
	usage := OpenAIUsage{}
	require.True(t, settleOpenAIStreamUsage(c, &usage, meter, body, "estimate-model", 1, "test"))
	require.Equal(t, 100, usage.InputTokens)
	require.Equal(t, 10, usage.OutputTokens)
	if apiKeyID > 0 {
		require.Equal(t, 90, usage.CacheReadInputTokens)
	} else {
		require.Equal(t, 100, usage.CacheReadInputTokens)
	}

	// 没有任何输出：保持 0，不估算。
	empty := OpenAIUsage{}
	require.False(t, settleOpenAIStreamUsage(c, &empty, &streamOutputTextMeter{}, body, "estimate-model", 1, "test"))
	require.True(t, openAIUsageIsEmpty(empty))
}

func TestOpenAIPromptCacheRatio_DefaultsToAllCachedAndSmooths(t *testing.T) {
	require.Equal(t, 1.0, lookupOpenAIPromptCacheRatio(424242, "never-seen"))
	rememberOpenAIPromptCacheRatio(424242, "m", OpenAIUsage{InputTokens: 100, CacheReadInputTokens: 100})
	require.InDelta(t, 1.0, lookupOpenAIPromptCacheRatio(424242, "M"), 1e-9)
	rememberOpenAIPromptCacheRatio(424242, "m", OpenAIUsage{InputTokens: 100, CacheReadInputTokens: 0})
	require.InDelta(t, 0.7, lookupOpenAIPromptCacheRatio(424242, "m"), 1e-9)
	rememberOpenAIPromptCacheRatio(0, "m", OpenAIUsage{InputTokens: 100}) // 无 key 不记
}

// 流在输出一半后被上游截断：仍按错误返回，但 usage 按已输出内容估算，不再是 0。
func TestForwardAsRawChatCompletions_TruncatedStreamEstimatesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello world, please answer"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_cut","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"reasoning_content":"let me think about it"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_cut","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"half an answer"},"finish_reason":null}]}`,
		"",
	}, "\n")
	svc := &OpenAIGatewayService{
		cfg: rawChatCompletionsTestConfig(),
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		}},
	}

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), body, "")
	require.Error(t, err)
	require.NotNil(t, result)
	// "let me think about it"(21) + "half an answer"(14) = 35 字节 → 9 tokens
	require.Equal(t, 9, result.Usage.OutputTokens)
	require.Positive(t, result.Usage.InputTokens)
	require.LessOrEqual(t, result.Usage.CacheReadInputTokens, result.Usage.InputTokens)
}
