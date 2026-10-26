package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type activeRequestsEnvelope struct {
	Code int                    `json:"code"`
	Data ActiveRequestsResponse `json:"data"`
}

func newActiveRequestsTestService(t *testing.T) *service.ConcurrencyService {
	t.Helper()
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return service.NewConcurrencyService(repository.NewConcurrencyCache(client, 15, 900))
}

func getActiveRequests(t *testing.T, router *gin.Engine, path string) ActiveRequestsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var envelope activeRequestsEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	return envelope.Data
}

func TestAccountActiveRequestsListsSlotHolders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	concurrency := newActiveRequestsTestService(t)
	adminSvc := newStubAdminService()
	adminSvc.getAccountResult = &service.Account{ID: 7, Name: "acc", Concurrency: 5}
	handler := &AccountHandler{adminService: adminSvc, concurrencyService: concurrency}
	router := gin.New()
	router.GET("/api/v1/admin/accounts/:id/active-requests", handler.GetActiveRequests)

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req-1")
	ctx = service.WithSlotHolderTrace(ctx, 1, 2)
	slot, err := concurrency.AcquireAccountSlot(ctx, 7, 5)
	require.NoError(t, err)
	require.True(t, slot.Acquired)

	got := getActiveRequests(t, router, "/api/v1/admin/accounts/7/active-requests")
	require.Equal(t, 1, got.Current)
	require.Equal(t, 5, got.Max)
	require.Len(t, got.Items, 1)
	require.True(t, got.Items[0].Recorded)
	require.Equal(t, int64(7), got.Items[0].AccountID)

	slot.ReleaseFunc()
	got = getActiveRequests(t, router, "/api/v1/admin/accounts/7/active-requests")
	require.Zero(t, got.Current)
	require.Empty(t, got.Items)
}

// startTracedRequest 模拟一个经过 API Key 认证的请求：先占用户槽位，再占账号槽位。
func startTracedRequest(t *testing.T, concurrency *service.ConcurrencyService, userID, userMax, accountID int64, requestID string) {
	t.Helper()
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, requestID)
	ctx = service.WithSlotHolderTrace(ctx, userID, 100+userID)
	userSlot, err := concurrency.AcquireUserSlot(ctx, userID, int(userMax))
	require.NoError(t, err)
	require.True(t, userSlot.Acquired)
	t.Cleanup(userSlot.ReleaseFunc)
	accountSlot, err := concurrency.AcquireAccountSlot(ctx, accountID, 5)
	require.NoError(t, err)
	require.True(t, accountSlot.Acquired)
	t.Cleanup(accountSlot.ReleaseFunc)
}

func TestUserActiveRequestsShowsAccountOfEachRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	concurrency := newActiveRequestsTestService(t)
	adminSvc := newStubAdminService()
	adminSvc.users[0].Concurrency = 3
	handler := NewUserHandler(adminSvc, concurrency, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/api/v1/admin/users/:id/active-requests", handler.GetActiveRequests)

	userID := adminSvc.users[0].ID
	// 客户端可以带重复的 X-Request-ID，账号关联不能依赖它。
	startTracedRequest(t, concurrency, userID, 3, 41, "dup")
	startTracedRequest(t, concurrency, userID, 3, 42, "dup")
	startTracedRequest(t, concurrency, userID+1, 3, 43, "dup")

	got := getActiveRequests(t, router, "/api/v1/admin/users/1/active-requests")
	require.Equal(t, 2, got.Current)
	require.Equal(t, 3, got.Max)
	require.Len(t, got.Items, 2)
	accounts := []int64{got.Items[0].AccountID, got.Items[1].AccountID}
	require.ElementsMatch(t, []int64{41, 42}, accounts)
}

func TestUnlimitedUserActiveRequestsComeFromAccountSlots(t *testing.T) {
	gin.SetMode(gin.TestMode)
	concurrency := newActiveRequestsTestService(t)
	adminSvc := newStubAdminService()
	adminSvc.users[0].Concurrency = 0
	handler := NewUserHandler(adminSvc, concurrency, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/api/v1/admin/users/:id/active-requests", handler.GetActiveRequests)

	userID := adminSvc.users[0].ID
	startTracedRequest(t, concurrency, userID, 0, 41, "r1")
	startTracedRequest(t, concurrency, userID+1, 0, 42, "r2")

	got := getActiveRequests(t, router, "/api/v1/admin/users/1/active-requests")
	require.Equal(t, 1, got.Current, "unlimited users report what is actually running")
	require.Zero(t, got.Max)
	require.Len(t, got.Items, 1)
	require.Equal(t, int64(41), got.Items[0].AccountID)
	require.Equal(t, userID, got.Items[0].UserID)
}

func TestActiveRequestsRejectsInvalidID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/admin/users/:id/active-requests", NewUserHandler(newStubAdminService(), nil, nil, nil, nil, nil, nil).GetActiveRequests)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/users/abc/active-requests", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
