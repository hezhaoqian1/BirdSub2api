package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 32MiB 上限渠道的真实 413 响应体。
const testSizeCapped413Body = `{"type":"error","error":{"type":"request_too_large","message":"Request body exceeds the 32 MiB limit for Messages requests"}}`

func bodyTooLargeFailoverErr() *service.UpstreamFailoverError {
	return upstreamErr(http.StatusRequestEntityTooLarge, testSizeCapped413Body)
}

func writeAnthropicFailoverExhausted(t *testing.T, lastErr *service.UpstreamFailoverError) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	(&GatewayHandler{}).handleFailoverExhausted(c, lastErr, service.PlatformAnthropic, false)
	return w
}

// 32MiB 渠道拒绝 → 换到其他渠道；不在该账号重试、不临时封禁。
func TestFailover_BodyTooLargeSwitchesAccountWithoutPenalty(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(10, false)

	action := fs.HandleFailoverError(context.Background(), mock, 190, service.PlatformAnthropic, 3, bodyTooLargeFailoverErr())

	require.Equal(t, FailoverContinue, action, "应换到上限更高的渠道")
	require.Equal(t, 1, fs.SwitchCount)
	require.Zero(t, fs.SameAccountRetryCount[190], "不得同账号重试")
	require.Contains(t, fs.FailedAccountIDs, int64(190))
	require.Empty(t, mock.calls, "请求体过大不应临时封禁账号")
	require.False(t, fs.LastFailoverErr.IsClientCausedTerminal())
}

// 190 拒绝请求体、150 繁忙 503：退避重试只放回 150，190 继续排除（重选只会再次被拒）。
func TestFailover_BodyTooLargeAccountStaysExcludedAcross503Backoff(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(3, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, bodyTooLargeFailoverErr()))
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 150, service.PlatformAnthropic, 0, upstreamErr(http.StatusServiceUnavailable, `{}`)))

	require.Equal(t, FailoverContinue, fs.HandleSelectionExhausted(ctx), "150 的 503 应触发退避重试")
	require.Contains(t, fs.FailedAccountIDs, int64(190), "请求体超限的账号退避后仍排除")
	require.NotContains(t, fs.FailedAccountIDs, int64(150), "繁忙账号可以重试")
}

// 只剩请求体超限的账号在排除列表中时，503 退避没有新候选，应直接结束而不是空转。
func TestFailover_AllExclusionsBodyTooLargeDoesNotSpin(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(10, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, bodyTooLargeFailoverErr()))
	fs.LastFailoverErr = upstreamErr(http.StatusServiceUnavailable, `{}`)

	require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(ctx))
}

// 所有可选渠道都拒绝请求体：回中英双语的 413「请求体过大」。
func TestFailover_AllChannelsBodyTooLargeReportsBilingual413(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(1, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, bodyTooLargeFailoverErr()))
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, mock, 111, service.PlatformAnthropic, 0, bodyTooLargeFailoverErr()))

	w := writeAnthropicFailoverExhausted(t, fs.LastFailoverErr)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.Contains(t, w.Body.String(), "请求体过大")
	require.NotContains(t, w.Body.String(), "32 MiB", "不得透出上游原文")
}

// 只有一个渠道拒绝请求体时（单渠道分组 / 其他渠道都不可选），同样回 413 双语提示。
func TestFailover_BodyTooLargeSingleAccountSelectionExhausted(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(10, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, bodyTooLargeFailoverErr()))
	require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(ctx))

	w := writeAnthropicFailoverExhausted(t, fs.LastFailoverErr)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.Contains(t, w.Body.String(), "请求体过大")
}

// 190 拒绝请求体、换到的渠道临时限流：按限流返回（稍后重试可能成功），不误报「请求体过大」。
func TestFailover_BodyTooLargeThenRateLimitedReports429(t *testing.T) {
	mock := &mockTempUnscheduler{}
	fs := NewFailoverState(1, false)
	ctx := context.Background()

	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, mock, 190, service.PlatformAnthropic, 3, bodyTooLargeFailoverErr()))
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, mock, 150, service.PlatformAnthropic, 0, upstreamErr(http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`)))

	w := writeAnthropicFailoverExhausted(t, fs.LastFailoverErr)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.NotContains(t, w.Body.String(), "请求体过大")
}
