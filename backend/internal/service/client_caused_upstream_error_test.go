//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const claudeSessionBlockedBody = `{"error":{"code":"claude_safety_session_blocked","message":"This session is blocked after a Claude refusal or fallback. Start a new session to continue.","type":"permission_error"},"type":"error"}`

func TestClassifyClientCausedUpstreamError_ClaudeSessionBlocked(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"official code 403":     {http.StatusForbidden, claudeSessionBlockedBody},
		"top-level code":        {http.StatusForbidden, `{"code":"claude_safety_session_blocked","message":"blocked"}`},
		"message only, 400":     {http.StatusBadRequest, `{"error":{"message":"This session is blocked after a Claude refusal or fallback."}}`},
		"code nested in string": {http.StatusForbidden, `{"error":{"message":"{\"code\":\"claude_safety_session_blocked\"}"}}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cc, ok := ClassifyClientCausedUpstreamError(tc.status, []byte(tc.body))
			require.True(t, ok)
			require.Equal(t, ClientCausedKindClaudeSessionBlocked, cc.Kind)
			require.Equal(t, http.StatusForbidden, cc.StatusCode)
			require.Equal(t, "permission_error", cc.ErrType)
			require.True(t, cc.StopFailover)
			require.Equal(t, ClaudeSessionBlockedClientMessage, cc.Message)
		})
	}
}

func TestClassifyClientCausedUpstreamError_IgnoresAccountSideFailures(t *testing.T) {
	cases := map[int]string{
		http.StatusForbidden:           `{"error":{"message":"Insufficient account balance"}}`,
		http.StatusUnauthorized:        `{"error":{"message":"invalid api key"}}`,
		http.StatusTooManyRequests:     `{"error":{"message":"rate limited"}}`,
		http.StatusBadRequest:          `{"error":{"message":"prompt is too long: 2043691 tokens > 1000000 maximum"}}`,
		http.StatusNotFound:            `{"error":{"message":"Model \"x\" is not supported by any configured account in this group"}}`,
		http.StatusInternalServerError: `{"error":{"code":"claude_safety_session_blocked"}}`, // 5xx 永远不算客户端原因
	}
	for status, body := range cases {
		_, ok := ClassifyClientCausedUpstreamError(status, []byte(body))
		require.False(t, ok, "status=%d body=%s", status, body)
	}
	_, ok := ClassifyClientCausedUpstreamError(http.StatusForbidden, nil)
	require.False(t, ok)
}

func TestClassifyClientCausedUpstreamError_RequestBodyTooLargeKeepsFailover(t *testing.T) {
	cc, ok := ClassifyClientCausedUpstreamError(http.StatusRequestEntityTooLarge,
		[]byte(`{"error":{"type":"request_too_large","message":"Request body exceeds the 32 MiB limit for Messages requests"}}`))
	require.True(t, ok)
	require.Equal(t, ClientCausedKindRequestBodyTooLarge, cc.Kind)
	require.Equal(t, http.StatusRequestEntityTooLarge, cc.StatusCode)
	require.Equal(t, "request_too_large", cc.ErrType)
	require.False(t, cc.StopFailover, "413 只改文案，不改变既有的换号行为")
	require.False(t, IsClientCausedTerminalUpstreamError(http.StatusRequestEntityTooLarge, nil))
}

func TestClientCausedMessagesAreBilingualAndHideUpstream(t *testing.T) {
	for _, msg := range []string{ClaudeSessionBlockedClientMessage, RequestBodyTooLargeClientMessage} {
		parts := strings.SplitN(msg, " / ", 2)
		require.Len(t, parts, 2, "应为「中文 / English」格式: %s", msg)
		require.Regexp(t, `\p{Han}`, parts[0])
		require.NotRegexp(t, `\p{Han}`, parts[1])
		lower := strings.ToLower(msg)
		for _, leak := range []string{"upstream", "上游", "claude_safety_session_blocked", "fallback", "http", "request_id", "req_"} {
			require.NotContains(t, lower, leak, "客户端文案不得包含上游信息: %q", leak)
		}
	}
}

func TestUpstreamFailoverError_IsClientCausedTerminal(t *testing.T) {
	require.True(t, (&UpstreamFailoverError{StatusCode: http.StatusForbidden, ResponseBody: []byte(claudeSessionBlockedBody)}).IsClientCausedTerminal())
	require.False(t, (&UpstreamFailoverError{StatusCode: http.StatusForbidden, ResponseBody: []byte(`{"error":{"message":"forbidden"}}`)}).IsClientCausedTerminal())
	var nilErr *UpstreamFailoverError
	require.False(t, nilErr.IsClientCausedTerminal())
}

func TestPeekClientCausedTerminalRestoresBody(t *testing.T) {
	s := &GatewayService{}
	resp := &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(claudeSessionBlockedBody))}
	require.True(t, s.peekClientCausedTerminal(resp))
	restored, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, claudeSessionBlockedBody, string(restored), "探测后响应体必须可再次读取")

	other := &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Insufficient account balance"}}`))}
	require.False(t, s.peekClientCausedTerminal(other))
	restored, _ = io.ReadAll(other.Body)
	require.Contains(t, string(restored), "Insufficient account balance")

	serverErr := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("x"))}
	require.False(t, s.peekClientCausedTerminal(serverErr))
	restored, _ = io.ReadAll(serverErr.Body)
	require.Equal(t, "x", string(restored), "5xx 不应被读取")
}

func TestRateLimitService_ClientCausedErrorDoesNotPenalizeAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	// OAuth 403 正常会把账号标记为 error；会话被锁是客户端原因，不得影响账号。
	account := &Account{ID: 190, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(claudeSessionBlockedBody))

	require.False(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)

	// 对照：普通 403 仍按原逻辑处理账号。
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"forbidden"}}`))
	require.Equal(t, 1, repo.setErrorCalls+repo.tempCalls, "普通 403 仍应影响账号状态")
}
