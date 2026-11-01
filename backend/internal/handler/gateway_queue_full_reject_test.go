package handler

import (
	"net/http"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 网关排队满时请求被标记为入口拒绝：ops_error_logs 不再逐条记录，改由入口拒绝聚合按分钟计数。
func TestAcquireUserSlotWithWait_QueueFullMarksIngressReject(t *testing.T) {
	cache := &helperConcurrencyCacheStub{
		userSeq:     []bool{false},
		waitAllowed: false,
	}
	concurrency := service.NewConcurrencyService(cache)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1beta/models/gemini-3.8-flash:generateContent")
	streamStarted := false

	release, err := helper.acquireUserSlotWithWaitTimeout(c, 4, 1, time.Second, false, &streamStarted)
	require.Nil(t, release)
	var queueFull *WaitQueueFullError
	require.ErrorAs(t, err, &queueFull)

	reason, rejected := middleware2.GetIngressRejectReason(c)
	require.True(t, rejected)
	require.Equal(t, middleware2.IngressRejectGatewayQueueFull, reason)
}

func TestAcquireUserSlotWithWait_ImmediateAcquireIsNotIngressReject(t *testing.T) {
	cache := &helperConcurrencyCacheStub{userSeq: []bool{true}}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	streamStarted := false

	release, err := helper.acquireUserSlotWithWaitTimeout(c, 4, 1, time.Second, false, &streamStarted)
	require.NoError(t, err)
	release()
	_, rejected := middleware2.GetIngressRejectReason(c)
	require.False(t, rejected)
}
