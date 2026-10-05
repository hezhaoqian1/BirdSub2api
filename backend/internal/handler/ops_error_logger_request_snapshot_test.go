package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const snapshotTestRequestBody = `{"model":"claude-sonnet-4-6","max_tokens":20000,"stream":false,"messages":[{"role":"user","content":"write a long essay"}]}`

// newSnapshotTestRouter 模拟网关 handler：经公共读取函数读请求体，再返回给定状态码。
func newSnapshotTestRouter(ops *service.OpsService, status int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(c *gin.Context) {
		body, err := pkghttputil.ReadLenientJSONRequestBodyWithPrealloc(c.Request, 0)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		setOpsRequestContext(c, "claude-sonnet-4-6", false)
		_ = body
		if status >= 400 {
			c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": "upstream_error", "message": "Upstream request failed"}})
			return
		}
		c.JSON(status, gin.H{"ok": true})
	})
	return router
}

// newSnapshotTestOps 返回一个 OpsService，并按 enabled 设置「报错时记录请求内容」。
func newSnapshotTestOps(t *testing.T, enabled bool) *service.OpsService {
	t.Helper()
	ops := service.NewOpsService(nil, &ingressRejectSettingRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	cfg, err := ops.GetOpsAdvancedSettings(context.Background())
	require.NoError(t, err)
	cfg.RecordRequestBodyOnError = enabled
	_, err = ops.UpdateOpsAdvancedSettings(context.Background(), cfg)
	require.NoError(t, err)
	return ops
}

func serveSnapshotTestRequest(router *gin.Engine) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(snapshotTestRequestBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func TestOpsErrorLogger_RecordsRequestSnapshotOnError(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	ops := newSnapshotTestOps(t, true)

	rec := serveSnapshotTestRequest(newSnapshotTestRouter(ops, http.StatusBadGateway))
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, int64(1), OpsErrorLogQueueLength())

	job := <-opsErrorLogQueue
	require.NotEmpty(t, job.entry.RequestSnapshot)
	var snap service.OpsRequestBodySnapshot
	require.NoError(t, json.Unmarshal([]byte(job.entry.RequestSnapshot), &snap))
	require.Equal(t, snapshotTestRequestBody, snap.Body)
	require.Equal(t, "claude-sonnet-4-6", snap.Summary.Model)
	require.EqualValues(t, 20000, *snap.Summary.MaxTokens)
}

func TestOpsErrorLogger_NoRequestSnapshotByDefault(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	// 默认配置：未开启「报错时记录请求内容」。
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	cfg, err := ops.GetOpsAdvancedSettings(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.RecordRequestBodyOnError, "recording must default to off")

	serveSnapshotTestRequest(newSnapshotTestRouter(ops, http.StatusBadGateway))
	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	job := <-opsErrorLogQueue
	require.Empty(t, job.entry.RequestSnapshot)
}

func TestOpsErrorLogger_NoRequestSnapshotWhenDisabled(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	ops := newSnapshotTestOps(t, false)

	serveSnapshotTestRequest(newSnapshotTestRouter(ops, http.StatusBadGateway))
	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	job := <-opsErrorLogQueue
	require.Empty(t, job.entry.RequestSnapshot)
}

func TestOpsErrorLogger_NoRequestSnapshotOnSuccess(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	ops := newSnapshotTestOps(t, true)

	rec := serveSnapshotTestRequest(newSnapshotTestRouter(ops, http.StatusOK))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(0), OpsErrorLogQueueLength())
}

func TestEnqueueOpsErrorLog_DropsSnapshotUnderQueuePressure(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	// 只剩下容纳一条普通错误记录的字节预算，装不下请求体快照。
	opsErrorLogQueueBytes.Store(opsErrorLogMaxQueueBytes - 8*1024)
	t.Cleanup(func() { opsErrorLogQueueBytes.Store(0) })

	entry := &service.OpsInsertErrorLogInput{
		ErrorPhase:      "upstream",
		ErrorType:       "upstream_error",
		StatusCode:      http.StatusBadGateway,
		RequestSnapshot: strings.Repeat("x", 64*1024),
	}
	enqueueOpsErrorLog(ops, entry)

	require.Equal(t, int64(1), OpsErrorLogQueueLength(), "the error row itself must still be queued")
	job := <-opsErrorLogQueue
	require.Empty(t, job.entry.RequestSnapshot)
	require.Equal(t, http.StatusBadGateway, job.entry.StatusCode)
}
