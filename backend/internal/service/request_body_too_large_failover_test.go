//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 32MiB 上限渠道的真实 413 响应体（生产 11-11 / 11-19 观测到的原文）。
const sizeCapped413Body = `{"type":"error","error":{"type":"request_too_large","message":"Request body exceeds the 32 MiB limit for Messages requests"}}`

const promptTooLong413Body = `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 2043691 tokens > 1000000 maximum"}}`

// fixedStatusUpstream 每次调用都返回一份新的固定响应，并记录调用次数。
type fixedStatusUpstream struct {
	status int
	body   string
	calls  int
}

func (u *fixedStatusUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	if req != nil && req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	return &http.Response{
		StatusCode: u.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"rid-413"}},
		Body:       io.NopCloser(strings.NewReader(u.body)),
	}, nil
}

func (u *fixedStatusUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func newBodyTooLargeTestService(upstream HTTPUpstream, repo AccountRepository) *GatewayService {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	return &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     NewRateLimitService(repo, nil, cfg, nil, nil),
		deferredService:      &DeferredService{},
	}
}

// 开了自定义错误码（不含 413）的 API Key 账号：改动前 413 会在同账号上重试并直接报错给客户。
func newSizeCappedAPIKeyAccount(passthrough bool) *Account {
	account := &Account{
		ID:          190,
		Name:        "size-capped",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":                    "upstream-key",
			"base_url":                   "https://api.anthropic.com",
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(529)},
		},
		Status:      StatusActive,
		Schedulable: true,
	}
	if passthrough {
		account.Extra = map[string]any{"anthropic_passthrough": true}
	}
	return account
}

func newBodyTooLargeTestContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, rec
}

func opsUpstreamEventKinds(c *gin.Context) []string {
	value, ok := c.Get(OpsUpstreamErrorsKey)
	if !ok {
		return nil
	}
	events, _ := value.([]*OpsUpstreamErrorEvent)
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		if event != nil {
			kinds = append(kinds, event.Kind)
		}
	}
	return kinds
}

func requireBodyTooLargeFailover(t *testing.T, err error) *UpstreamFailoverError {
	t.Helper()
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "应返回换号错误，实际: %v", err)
	require.Equal(t, http.StatusRequestEntityTooLarge, failoverErr.StatusCode)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.False(t, failoverErr.RetryableOnSameAccount, "同一请求体不得在同账号重试")
	require.False(t, failoverErr.IsClientCausedTerminal(), "413 不得终止换号")
	require.True(t, failoverErr.IsRequestBodyTooLarge())
	return failoverErr
}

func TestIsAnthropicRequestBodyTooLargeError(t *testing.T) {
	tooLarge := []string{
		sizeCapped413Body,
		`{"error":{"type":"request_too_large","message":"Request exceeds the maximum allowed number of bytes."}}`,
		`<html><head><title>413 Request Entity Too Large</title></head><body>nginx</body></html>`,
		``,
	}
	for _, body := range tooLarge {
		require.True(t, isAnthropicRequestBodyTooLargeError(http.StatusRequestEntityTooLarge, []byte(body)), body)
	}

	notTooLarge := map[int]string{
		http.StatusBadRequest: sizeCapped413Body,
		http.StatusBadGateway: sizeCapped413Body,
	}
	for status, body := range notTooLarge {
		require.False(t, isAnthropicRequestBodyTooLargeError(status, []byte(body)), "status=%d", status)
	}
	// 上下文超长：换号无用。
	for _, body := range []string{
		promptTooLong413Body,
		`{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 200000 tokens"}}`,
	} {
		require.False(t, isAnthropicRequestBodyTooLargeError(http.StatusRequestEntityTooLarge, []byte(body)), body)
	}

	var nilErr *UpstreamFailoverError
	require.False(t, nilErr.IsRequestBodyTooLarge())
}

func TestForward_413BodyTooLargeFailsOverWithoutRetryOrAccountPenalty(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account *Account
	}{
		{"apikey", newSizeCappedAPIKeyAccount(false)},
		{"apikey_passthrough", newSizeCappedAPIKeyAccount(true)},
		{"oauth", newAnthropicOAuthAccountForPartialUsageTest()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newBodyTooLargeTestContext("/v1/messages")
			body := []byte(`{"model":"claude-opus-5-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)

			upstream := &fixedStatusUpstream{status: http.StatusRequestEntityTooLarge, body: sizeCapped413Body}
			repo := &rateLimitAccountRepoStub{}
			svc := newBodyTooLargeTestService(upstream, repo)

			result, err := svc.Forward(context.Background(), c, tc.account, parsed)

			require.Nil(t, result)
			requireBodyTooLargeFailover(t, err)
			require.Equal(t, 1, upstream.calls, "不得把同一大请求体在同账号上重发")
			require.False(t, c.Writer.Written(), "换号前不得向客户端写任何内容")
			require.Zero(t, rec.Body.Len())
			require.Zero(t, repo.setErrorCalls+repo.tempCalls+repo.rateLimitedCalls, "请求体过大与账号健康无关")
			require.Contains(t, opsUpstreamEventKinds(c), "request_body_too_large_failover")
		})
	}
}

func TestForward_413PromptTooLongDoesNotFailOver(t *testing.T) {
	c, rec := newBodyTooLargeTestContext("/v1/messages")
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	upstream := &fixedStatusUpstream{status: http.StatusRequestEntityTooLarge, body: promptTooLong413Body}
	svc := newBodyTooLargeTestService(upstream, &rateLimitAccountRepoStub{})

	_, err = svc.Forward(context.Background(), c, newSizeCappedAPIKeyAccount(false), parsed)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "上下文超长换号无用，应直接返回客户端")
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Contains(t, rec.Body.String(), "请求体过大")
}

func TestForwardAsChatCompletions_413BodyTooLargeFailsOver(t *testing.T) {
	c, rec := newBodyTooLargeTestContext("/v1/chat/completions")
	upstream := &fixedStatusUpstream{status: http.StatusRequestEntityTooLarge, body: sizeCapped413Body}
	repo := &rateLimitAccountRepoStub{}
	svc := newBodyTooLargeTestService(upstream, repo)
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, newSizeCappedAPIKeyAccount(false), body, nil)

	require.Nil(t, result)
	requireBodyTooLargeFailover(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Zero(t, rec.Body.Len())
	require.Zero(t, repo.setErrorCalls+repo.tempCalls+repo.rateLimitedCalls)
}

func TestForwardAsResponses_413BodyTooLargeFailsOver(t *testing.T) {
	c, rec := newBodyTooLargeTestContext("/v1/responses")
	upstream := &fixedStatusUpstream{status: http.StatusRequestEntityTooLarge, body: sizeCapped413Body}
	repo := &rateLimitAccountRepoStub{}
	svc := newBodyTooLargeTestService(upstream, repo)
	body := []byte(`{"model":"claude-opus-5-5","input":"hi","stream":true}`)

	result, err := svc.ForwardAsResponses(context.Background(), c, newSizeCappedAPIKeyAccount(false), body, nil)

	require.Nil(t, result)
	requireBodyTooLargeFailover(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Zero(t, rec.Body.Len())
	require.Zero(t, repo.setErrorCalls+repo.tempCalls+repo.rateLimitedCalls)
}

func TestHandleBedrockUpstreamErrors_413BodyTooLargeFailsOver(t *testing.T) {
	c, rec := newBodyTooLargeTestContext("/v1/messages")
	repo := &rateLimitAccountRepoStub{}
	svc := newBodyTooLargeTestService(&fixedStatusUpstream{}, repo)
	account := &Account{ID: 189, Name: "bedrock", Platform: PlatformAnthropic, Type: AccountTypeBedrock}
	resp := &http.Response{
		StatusCode: http.StatusRequestEntityTooLarge,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"message":"Request payload size exceeds the 20 MB limit"}`)),
	}

	result, err := svc.handleBedrockUpstreamErrors(context.Background(), resp, c, account, "claude-opus-5-5")

	require.Nil(t, result)
	requireBodyTooLargeFailover(t, err)
	require.Zero(t, rec.Body.Len())
	require.Zero(t, repo.setErrorCalls+repo.tempCalls+repo.rateLimitedCalls)
}

func TestPeekRequestBodyTooLargeFailoverRestoresBodyWhenNotFailingOver(t *testing.T) {
	svc := &GatewayService{}
	c, _ := newBodyTooLargeTestContext("/v1/messages")
	account := newSizeCappedAPIKeyAccount(false)

	resp := &http.Response{StatusCode: http.StatusRequestEntityTooLarge, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(promptTooLong413Body))}
	require.Nil(t, svc.peekRequestBodyTooLargeFailover(c, account, resp, false))
	restored, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, promptTooLong413Body, string(restored), "不换号时后续错误处理仍需读到完整响应体")

	other := &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("x"))}
	require.Nil(t, svc.peekRequestBodyTooLargeFailover(c, account, other, false))
	restored, _ = io.ReadAll(other.Body)
	require.Equal(t, "x", string(restored), "非 413 不应被读取")
}

func TestShouldRetryUpstreamErrorNeverRetries413(t *testing.T) {
	svc := &GatewayService{}
	require.False(t, svc.shouldRetryUpstreamError(newSizeCappedAPIKeyAccount(false), http.StatusRequestEntityTooLarge))
	// 对照：未配置的其他错误码仍按原逻辑重试。
	require.True(t, svc.shouldRetryUpstreamError(newSizeCappedAPIKeyAccount(false), http.StatusBadGateway))
}
