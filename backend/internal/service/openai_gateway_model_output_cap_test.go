//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// hy4UpstreamTestAccount 构造与生产 acct 143 同形的 openai APIKey 账号（Aizzz
// zl.aizzz.xyz，Chat Completions 入站转 /v1/responses 出站）。
func hy4UpstreamTestAccount(id int64) *Account {
	return &Account{
		ID:       id,
		Name:     "aizzz-hy4",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://zl.aizzz.xyz",
		},
		Extra: map[string]any{},
	}
}

func TestIsHy4Model(t *testing.T) {
	for _, model := range []string{"hy4-preview", "HY4-preview", " Hy4-preview ", "tencent/hy4-preview", "hy4"} {
		require.True(t, isHy4Model(model), model)
	}
	for _, model := range []string{"", "hunyuan-t1", "gpt-4.1", "why4-preview", "deepseek-v4-flash"} {
		require.False(t, isHy4Model(model), model)
	}
}

func TestClampModelMaxOutputTokens(t *testing.T) {
	t.Run("hy4 above cap is clamped", func(t *testing.T) {
		out := clampModelMaxOutputTokens([]byte(`{"model":"hy4-preview","max_output_tokens":65536}`), responsesOutputLimitKeys, "hy4-preview")
		require.Equal(t, int64(64000), gjson.GetBytes(out, "max_output_tokens").Int())
	})
	t.Run("any candidate model matching applies the cap", func(t *testing.T) {
		out := clampModelMaxOutputTokens([]byte(`{"max_output_tokens":131072}`), responsesOutputLimitKeys, "mapped-upstream", "HY4-preview")
		require.Equal(t, int64(64000), gjson.GetBytes(out, "max_output_tokens").Int())
	})
	t.Run("chat keys are both clamped", func(t *testing.T) {
		out := clampModelMaxOutputTokens([]byte(`{"max_tokens":65536,"max_completion_tokens":100000}`), chatOutputLimitKeys, "hy4-preview")
		require.Equal(t, int64(64000), gjson.GetBytes(out, "max_tokens").Int())
		require.Equal(t, int64(64000), gjson.GetBytes(out, "max_completion_tokens").Int())
	})
	for name, body := range map[string]string{
		"at cap":         `{"max_output_tokens":64000}`,
		"below cap":      `{"max_output_tokens":4096}`,
		"missing field":  `{"input":"hi"}`,
		"non-numeric":    `{"max_output_tokens":"65536"}`,
		"invalid json":   `{"max_output_tokens":`,
		"empty body":     ``,
		"other model hi": `{"max_output_tokens":65536}`,
	} {
		t.Run(name+" is untouched", func(t *testing.T) {
			model := "hy4-preview"
			if name == "other model hi" {
				model = "gpt-4.1"
			}
			out := clampModelMaxOutputTokens([]byte(body), responsesOutputLimitKeys, model)
			require.Equal(t, body, string(out))
		})
	}
}

// TestForwardAsChatCompletionsClampsHy4MaxOutputTokens 复现生产 user 57 的请求形态：
// /v1/chat/completions + max_tokens=65536 → 出站 /v1/responses 的 max_output_tokens
// 必须被压到 64000（Aizzz hy4 路由上限），其它模型不受影响。
func TestForwardAsChatCompletionsClampsHy4MaxOutputTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	run := func(body []byte) *httpUpstreamRecorder {
		upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		_, err := svc.ForwardAsChatCompletions(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), hy4UpstreamTestAccount(901), body, "", "")
		require.Error(t, err)
		return upstream
	}

	for _, body := range []string{
		`{"model":"hy4-preview","max_tokens":65536,"messages":[{"role":"user","content":"Reply with exactly: CH-OK"}],"stream":false}`,
		`{"model":"hy4-preview","max_completion_tokens":131072,"messages":[{"role":"user","content":"hi"}],"stream":true}`,
	} {
		upstream := run([]byte(body))
		require.Equal(t, "https://zl.aizzz.xyz/v1/responses", upstream.lastReq.URL.String())
		require.Equal(t, int64(64000), gjson.GetBytes(upstream.lastBody, "max_output_tokens").Int(), body)
	}

	atCap := run([]byte(`{"model":"hy4-preview","max_tokens":64000,"messages":[{"role":"user","content":"hi"}],"stream":false}`))
	require.Equal(t, int64(64000), gjson.GetBytes(atCap.lastBody, "max_output_tokens").Int())

	other := run([]byte(`{"model":"gpt-4.1","max_tokens":65536,"messages":[{"role":"user","content":"hi"}],"stream":false}`))
	require.Equal(t, int64(65536), gjson.GetBytes(other.lastBody, "max_output_tokens").Int())
}

func TestForwardResponsesClampsHy4MaxOutputTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	run := func(body []byte) *httpUpstreamRecorder {
		upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		_, err := svc.Forward(context.Background(), adaptiveProtocolTestContext("/v1/responses", body), hy4UpstreamTestAccount(902), body)
		require.Error(t, err)
		return upstream
	}

	native := run([]byte(`{"model":"hy4-preview","input":"hi","max_output_tokens":65536,"stream":false}`))
	require.Equal(t, int64(64000), gjson.GetBytes(native.lastBody, "max_output_tokens").Int())

	// OpenAI 平台把 max_tokens 归一化为 max_output_tokens，clamp 以 max_tokens 为生效值。
	legacy := run([]byte(`{"model":"hy4-preview","input":"hi","max_tokens":65536,"stream":false}`))
	require.Equal(t, int64(64000), gjson.GetBytes(legacy.lastBody, "max_output_tokens").Int())
	require.False(t, gjson.GetBytes(legacy.lastBody, "max_tokens").Exists())

	other := run([]byte(`{"model":"gpt-4.1","input":"hi","max_output_tokens":65536,"stream":false}`))
	require.Equal(t, int64(65536), gjson.GetBytes(other.lastBody, "max_output_tokens").Int())
}

func TestForwardAsAnthropicClampsHy4MaxOutputTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"hy4-preview","max_tokens":65536,"messages":[{"role":"user","content":"hi"}],"stream":false}`)
	upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	_, err := svc.ForwardAsAnthropic(context.Background(), adaptiveProtocolTestContext("/v1/messages", body), hy4UpstreamTestAccount(903), body, "", "")
	require.Error(t, err)
	require.Equal(t, int64(64000), gjson.GetBytes(upstream.lastBody, "max_output_tokens").Int())
}

func TestForwardAsRawChatCompletionsClampsHy4MaxTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	account := hy4UpstreamTestAccount(904)
	account.Extra[openai_compat.ExtraKeyResponsesMode] = string(openai_compat.ResponsesSupportModeForceChatCompletions)

	body := []byte(`{"model":"hy4-preview","max_tokens":65536,"messages":[{"role":"user","content":"hi"}],"stream":false}`)
	upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	_, err := svc.ForwardAsChatCompletions(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), account, body, "", "")
	require.Error(t, err)
	require.Equal(t, "https://zl.aizzz.xyz/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, int64(64000), gjson.GetBytes(upstream.lastBody, "max_tokens").Int())
}
