package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const forceStreamClientBody = `{"model":"claude-sonnet-4-6","max_tokens":1024,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`

func forceStreamAccount() *Account {
	account := newAnthropicAPIKeyAccountForTest()
	account.Extra["anthropic_force_upstream_stream"] = true
	return account
}

func sseUpstreamResponse(sse string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid-force-stream"},
		},
		Body: io.NopCloser(strings.NewReader(sse)),
	}
}

func newForceStreamTestService(cfg *config.Config, upstream *anthropicHTTPUpstreamRecorder) *GatewayService {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &GatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{},
	}
}

func newForceStreamTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c, rec
}

var forceStreamSuccessSSE = buildSSE([][2]string{
	{"message_start", testMessageStart},
	{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"aggregated reply"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":0}`},
	{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`},
	{"message_stop", `{"type":"message_stop"}`},
})

func TestForceUpstreamStream_AccountFlagAggregatesToJSON(t *testing.T) {
	c, rec := newForceStreamTestContext()
	upstream := &anthropicHTTPUpstreamRecorder{resp: sseUpstreamResponse(forceStreamSuccessSSE)}
	svc := newForceStreamTestService(nil, upstream)

	result, err := svc.forwardAnthropicAPIKeyPassthrough(context.Background(), c, forceStreamAccount(),
		[]byte(forceStreamClientBody), "claude-sonnet-4-6", "claude-sonnet-4-6", false, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)

	// 上游实际收到的是流式请求。
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool(), "upstream body: %s", upstream.lastBody)
	// 客户端拿到的是完整的非流式 JSON。
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	out := gjson.Parse(rec.Body.String())
	require.Equal(t, "message", out.Get("type").String())
	require.Equal(t, "aggregated reply", out.Get("content.0.text").String())
	require.Equal(t, "end_turn", out.Get("stop_reason").String())
	require.Equal(t, int64(9), out.Get("usage.output_tokens").Int())
	// 计费 usage 与对客户端的结果标记为非流式。
	require.False(t, result.Stream)
	require.Equal(t, 25, result.Usage.InputTokens)
	require.Equal(t, 9, result.Usage.OutputTokens)
	require.Equal(t, 10, result.Usage.CacheReadInputTokens)
}

func TestForceUpstreamStream_GlobalConfigFlag(t *testing.T) {
	c, rec := newForceStreamTestContext()
	upstream := &anthropicHTTPUpstreamRecorder{resp: sseUpstreamResponse(forceStreamSuccessSSE)}
	cfg := &config.Config{}
	cfg.Gateway.AnthropicForceUpstreamStream = true
	svc := newForceStreamTestService(cfg, upstream)

	_, err := svc.forwardAnthropicAPIKeyPassthrough(context.Background(), c, newAnthropicAPIKeyAccountForTest(),
		[]byte(forceStreamClientBody), "claude-sonnet-4-6", "claude-sonnet-4-6", false, time.Now())
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "aggregated reply", gjson.Get(rec.Body.String(), "content.0.text").String())
}

func TestForceUpstreamStream_DisabledKeepsNonStreamBody(t *testing.T) {
	c, rec := newForceStreamTestContext()
	upstreamJSON := `{"id":"msg_x","type":"message","role":"assistant","content":[{"type":"text","text":"plain"}],"usage":{"input_tokens":3,"output_tokens":2}}`
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamJSON)),
	}}
	svc := newForceStreamTestService(nil, upstream)

	_, err := svc.forwardAnthropicAPIKeyPassthrough(context.Background(), c, newAnthropicAPIKeyAccountForTest(),
		[]byte(forceStreamClientBody), "claude-sonnet-4-6", "claude-sonnet-4-6", false, time.Now())
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(upstream.lastBody, "stream").Bool(), "flag off must not change the wire body")
	require.Equal(t, upstreamJSON, rec.Body.String())
}

func TestForceUpstreamStream_UpstreamIgnoresStreamFallsBackToJSON(t *testing.T) {
	c, rec := newForceStreamTestContext()
	upstreamJSON := `{"id":"msg_y","type":"message","role":"assistant","content":[{"type":"text","text":"json anyway"}],"usage":{"input_tokens":3,"output_tokens":2}}`
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamJSON)),
	}}
	svc := newForceStreamTestService(nil, upstream)

	result, err := svc.forwardAnthropicAPIKeyPassthrough(context.Background(), c, forceStreamAccount(),
		[]byte(forceStreamClientBody), "claude-sonnet-4-6", "claude-sonnet-4-6", false, time.Now())
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, upstreamJSON, rec.Body.String())
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestForceUpstreamStream_StreamClientUnaffected(t *testing.T) {
	c, rec := newForceStreamTestContext()
	upstream := &anthropicHTTPUpstreamRecorder{resp: sseUpstreamResponse(forceStreamSuccessSSE)}
	svc := newForceStreamTestService(nil, upstream)
	body := strings.Replace(forceStreamClientBody, `"stream":false`, `"stream":true`, 1)

	result, err := svc.forwardAnthropicAPIKeyPassthrough(context.Background(), c, forceStreamAccount(),
		[]byte(body), "claude-sonnet-4-6", "claude-sonnet-4-6", true, time.Now())
	require.NoError(t, err)
	require.True(t, result.Stream)
	// 流式客户端仍收到原始 SSE，而不是聚合 JSON。
	require.Contains(t, rec.Body.String(), "event: content_block_delta")
}

func TestForceUpstreamStream_IncompleteStreamFailsOverWithoutWritingClient(t *testing.T) {
	c, rec := newForceStreamTestContext()
	truncated := buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut off"}}`},
	})
	upstream := &anthropicHTTPUpstreamRecorder{resp: sseUpstreamResponse(truncated)}
	svc := newForceStreamTestService(nil, upstream)

	result, err := svc.forwardAnthropicAPIKeyPassthrough(context.Background(), c, forceStreamAccount(),
		[]byte(forceStreamClientBody), "claude-sonnet-4-6", "claude-sonnet-4-6", false, time.Now())
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "err = %v", err)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Empty(t, rec.Body.String(), "nothing may be written to the client before failover")
}

func TestForceUpstreamStream_ErrorEventFailsOverWithMappedStatus(t *testing.T) {
	c, rec := newForceStreamTestContext()
	sse := buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
	})
	upstream := &anthropicHTTPUpstreamRecorder{resp: sseUpstreamResponse(sse)}
	repo := &gatewayForwardErrorPolicyRepoStub{}
	cfg := &config.Config{}
	svc := newForceStreamTestService(cfg, upstream)
	svc.rateLimitService = NewRateLimitService(repo, nil, cfg, nil, nil)

	result, err := svc.forwardAnthropicAPIKeyPassthrough(context.Background(), c, forceStreamAccount(),
		[]byte(forceStreamClientBody), "claude-sonnet-4-6", "claude-sonnet-4-6", false, time.Now())
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "err = %v", err)
	require.Equal(t, 529, failoverErr.StatusCode)
	require.Equal(t, "overloaded_error", gjson.GetBytes(failoverErr.ResponseBody, "error.type").String())
	require.Equal(t, 1, repo.overloadCalls, "overloaded account must be cooled down")
	require.Empty(t, rec.Body.String())
}

func TestForceUpstreamStream_NonFailoverErrorEventPassesThroughAnthropicError(t *testing.T) {
	c, rec := newForceStreamTestContext()
	sse := buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"error", `{"type":"error","error":{"type":"invalid_request_error","message":"bad input"}}`},
	})
	upstream := &anthropicHTTPUpstreamRecorder{resp: sseUpstreamResponse(sse)}
	svc := newForceStreamTestService(nil, upstream)

	_, err := svc.forwardAnthropicAPIKeyPassthrough(context.Background(), c, forceStreamAccount(),
		[]byte(forceStreamClientBody), "claude-sonnet-4-6", "claude-sonnet-4-6", false, time.Now())
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "400 must not fail over")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "bad input")
}
