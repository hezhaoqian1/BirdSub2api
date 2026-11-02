package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// serveNoAvailable 走一遍 ops 中间件，返回入口拒绝原因与写入 ops_error_logs 队列的条数。
func serveNoAvailable(t *testing.T, handle gin.HandlerFunc) (middleware2.IngressRejectReason, bool, int64) {
	t.Helper()
	setupOpsErrorLogTestQueue(t, 4)
	gin.SetMode(gin.TestMode)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var reason middleware2.IngressRejectReason
	var rejected bool
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		reason, rejected = middleware2.GetIngressRejectReason(c)
	})
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1beta/models/gemini-3.8-flash:generateContent", handle)
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.8-flash:generateContent", nil))
	return reason, rejected, OpsErrorLogQueueLength()
}

// 分组里没有可调度账号、没试过任何上游：按入口拒绝聚合，不逐条写库。
func TestOpsErrorLogger_PureNoAvailableAccountsIsAggregated(t *testing.T) {
	reason, rejected, queued := serveNoAvailable(t, func(c *gin.Context) {
		googleError(c, http.StatusServiceUnavailable, "No available Gemini accounts: no available accounts")
	})
	require.True(t, rejected)
	require.Equal(t, middleware2.IngressRejectNoAvailableAccounts, reason)
	require.Zero(t, queued)
}

// 换号用尽后才报无可用账号：带有上游错误事件，仍逐条记录以便排查。
func TestOpsErrorLogger_NoAvailableAfterUpstreamAttemptsIsStillLogged(t *testing.T) {
	_, rejected, queued := serveNoAvailable(t, func(c *gin.Context) {
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 174, UpstreamStatusCode: http.StatusForbidden, Kind: "failover", Message: "insufficient quota"}})
		googleError(c, http.StatusServiceUnavailable, "No available Gemini accounts: no available accounts")
	})
	require.False(t, rejected)
	require.Equal(t, int64(1), queued)
}

// 其他 503 不受影响。
func TestOpsErrorLogger_OtherServiceUnavailableIsStillLogged(t *testing.T) {
	_, rejected, queued := serveNoAvailable(t, func(c *gin.Context) {
		googleError(c, http.StatusServiceUnavailable, "Upstream service temporarily unavailable")
	})
	require.False(t, rejected)
	require.Equal(t, int64(1), queued)
}
