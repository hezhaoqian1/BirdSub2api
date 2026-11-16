package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const testClaudeSessionBlockedBody = `{"error":{"code":"claude_safety_session_blocked","message":"This session is blocked after a Claude refusal or fallback. Start a new session to continue.","type":"permission_error"},"type":"error"}`

func sessionBlockedFailoverErr() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:             http.StatusForbidden,
		ResponseBody:           []byte(testClaudeSessionBlockedBody),
		RetryableOnSameAccount: true, // 池模式账号：正常会先同账号重试
	}
}

func upstreamErr(status int, body string) *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{StatusCode: status, ResponseBody: []byte(body)}
}

// 会话被锁：不在该账号上重试、不临时封禁，但继续尝试其他账号。
func TestHandleFailoverError_ClientCausedSwitchesWithoutSameAccountRetry(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(10, false)

	action := fs.HandleFailoverError(context.Background(), mock, 190, service.PlatformAnthropic, 3, sessionBlockedFailoverErr())

	require.Equal(t, FailoverContinue, action, "应切换到其他账号尝试")
	require.Equal(t, 1, fs.SwitchCount)
	require.Zero(t, fs.SameAccountRetryCount[190], "不得同账号重试")
	require.Contains(t, fs.FailedAccountIDs, int64(190), "本请求内不再选回该账号")
	require.Empty(t, mock.calls, "客户端原因不应临时封禁账号")
}

// A 会话被锁 → B 返回 502 → 全部失败：回报 A 的「请新开对话」，而不是 B 的 502。
func TestFailover_BlockedThenOtherChannel502ReportsBlocked(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(1, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, sessionBlockedFailoverErr()))
	action := fs.HandleFailoverError(ctx, mock, 150, service.PlatformAnthropic, 0, upstreamErr(http.StatusBadGateway, `{"error":{"message":"bad gateway"}}`))

	require.Equal(t, FailoverExhausted, action)
	require.True(t, fs.LastFailoverErr.IsClientCausedTerminal(), "耗尽时应回报会话被锁")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	(&GatewayHandler{}).handleFailoverExhausted(c, fs.LastFailoverErr, service.PlatformAnthropic, false)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "请新开一个对话")
}

// 顺序反过来（B 先 502、A 后被锁）同样回报会话被锁。
func TestFailover_Other502ThenBlockedReportsBlocked(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(1, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 150, service.PlatformAnthropic, 0, upstreamErr(http.StatusBadGateway, `{}`)))
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, sessionBlockedFailoverErr()))
	require.True(t, fs.LastFailoverErr.IsClientCausedTerminal())
}

// 被锁后其他渠道返回「停止换号」类错误：同样优先回报会话被锁。
func TestFailover_BlockedThenStopErrorReportsBlocked(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(5, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, sessionBlockedFailoverErr()))
	stop := upstreamErr(http.StatusBadRequest, `{"error":{"message":"stop"}}`)
	stop.NextAccountAction = service.NextAccountStop
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, mock, 150, service.PlatformAnthropic, 0, stop))
	require.True(t, fs.LastFailoverErr.IsClientCausedTerminal())
}

// 单账号分组：被锁后没有其他账号可选 → 直接回报会话被锁。
func TestFailover_BlockedSingleAccountSelectionExhausted(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(10, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, sessionBlockedFailoverErr()))
	require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(ctx))
	require.True(t, fs.LastFailoverErr.IsClientCausedTerminal())
}

// A 被锁、B 繁忙 503：503 退避会清空排除列表重试 B，但被锁的 A 必须继续排除；
// 最终仍失败时回报会话被锁。
func TestFailover_BlockedAccountStaysExcludedAcross503Backoff(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(2, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, sessionBlockedFailoverErr()))
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 150, service.PlatformAnthropic, 0, upstreamErr(http.StatusServiceUnavailable, `{}`)))

	require.Equal(t, FailoverContinue, fs.HandleSelectionExhausted(ctx), "B 的 503 应触发退避重试")
	require.Contains(t, fs.FailedAccountIDs, int64(190), "被锁账号退避后仍排除")
	require.NotContains(t, fs.FailedAccountIDs, int64(150), "繁忙账号可以重试")

	// B 再次 503，切换次数耗尽 → 回报会话被锁。
	action := fs.HandleFailoverError(ctx, mock, 150, service.PlatformAnthropic, 0, upstreamErr(http.StatusServiceUnavailable, `{}`))
	require.Equal(t, FailoverExhausted, action)
	require.True(t, fs.LastFailoverErr.IsClientCausedTerminal())
}

// 只剩被锁账号在排除列表中时，503 退避没有新候选，应直接结束。
func TestFailover_AllExclusionsBlockedDoesNotSpin(t *testing.T) {
	fs := NewFailoverState(10, false)
	fs.recordClientCaused(190, sessionBlockedFailoverErr())
	fs.FailedAccountIDs[190] = struct{}{}
	fs.LastFailoverErr = upstreamErr(http.StatusServiceUnavailable, `{}`)

	require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(context.Background()))
	require.True(t, fs.LastFailoverErr.IsClientCausedTerminal())
}

// 没有出现会话被锁时，耗尽仍回报最后一个错误（行为不变）。
func TestFailover_NoClientCausedKeepsLastError(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(0, false)
	last := upstreamErr(http.StatusBadGateway, `{}`)
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(context.Background(), mock, 150, service.PlatformAnthropic, 0, last))
	require.Same(t, last, fs.LastFailoverErr)
}

func TestHandleFailoverError_OrdinaryForbiddenStillRetries(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(10, false)
	err := &service.UpstreamFailoverError{
		StatusCode:             http.StatusForbidden,
		ResponseBody:           []byte(`{"error":{"message":"Insufficient account balance"}}`),
		RetryableOnSameAccount: true,
	}
	// retryLimit=0：跳过同账号重试（避免 sleep），只验证普通 403 仍会换号。
	action := fs.HandleFailoverError(context.Background(), mock, 190, service.PlatformAnthropic, 0, err)
	require.Equal(t, FailoverContinue, action, "普通 403 仍应换号")
	require.Equal(t, 1, fs.SwitchCount)
	require.Contains(t, fs.FailedAccountIDs, int64(190))
}

func decodeAnthropicError(t *testing.T, body []byte) (string, string) {
	t.Helper()
	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, "error", payload.Type)
	return payload.Error.Type, payload.Error.Message
}

func TestHandleFailoverExhausted_ClaudeSessionBlockedBilingual(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	h := &GatewayHandler{}
	h.handleFailoverExhausted(c, sessionBlockedFailoverErr(), service.PlatformAnthropic, false)

	require.Equal(t, http.StatusForbidden, w.Code)
	errType, msg := decodeAnthropicError(t, w.Body.Bytes())
	require.Equal(t, "permission_error", errType)
	require.Equal(t, service.ClaudeSessionBlockedClientMessage, msg)
	require.Contains(t, msg, "请新开一个对话")
	require.Contains(t, msg, "start a new conversation")
	require.NotContains(t, w.Body.String(), "claude_safety_session_blocked", "不得透出上游错误码")
}

func TestHandleFailoverExhausted_ClaudeSessionBlockedMidStreamSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	h := &GatewayHandler{}
	h.handleFailoverExhausted(c, sessionBlockedFailoverErr(), service.PlatformAnthropic, true)

	body := w.Body.String()
	require.Contains(t, body, `data: {"type":"error"`)
	require.Contains(t, body, "permission_error")
	require.True(t, strings.Contains(body, "请新开一个对话"))
}

func TestHandleFailoverExhausted_RequestBodyTooLarge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	h := &GatewayHandler{}
	h.handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusRequestEntityTooLarge,
		ResponseBody: []byte(`{"error":{"type":"request_too_large","message":"Request body exceeds the 32 MiB limit for Messages requests"}}`),
	}, service.PlatformAnthropic, false)

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	errType, msg := decodeAnthropicError(t, w.Body.Bytes())
	require.Equal(t, "request_too_large", errType)
	require.Equal(t, service.RequestBodyTooLargeClientMessage, msg)
}

func TestHandleFailoverExhausted_AccountSideForbiddenStaysMasked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	h := &GatewayHandler{}
	h.handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusForbidden,
		ResponseBody: []byte(`{"error":{"message":"Insufficient account balance"}}`),
	}, service.PlatformAnthropic, false)

	require.Equal(t, http.StatusBadGateway, w.Code)
	_, msg := decodeAnthropicError(t, w.Body.Bytes())
	require.Equal(t, "Upstream access forbidden, please contact administrator", msg)
	require.NotContains(t, w.Body.String(), "balance", "渠道余额等账号侧原因仍需隐藏")
}

func TestChatCompletionsAndResponsesFailoverExhausted_ClaudeSessionBlocked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	h.handleCCFailoverExhausted(c, sessionBlockedFailoverErr(), false)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "请新开一个对话")
	require.NotContains(t, w.Body.String(), "claude_safety_session_blocked")

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	h.handleResponsesFailoverExhausted(c, sessionBlockedFailoverErr(), false)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "请新开一个对话")
	require.NotContains(t, w.Body.String(), "claude_safety_session_blocked")
}
