package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// activeRequestsCacheStub 只实现弹窗接口会用到的方法；其余 ConcurrencyCache 方法未实现（调用即 panic）。
// 与真实 Redis 缓存的端到端行为在 repository 包里测试。
type activeRequestsCacheStub struct {
	service.ConcurrencyCache
	userSlots      *service.ActiveSlotSnapshot
	accountSlots   *service.ActiveSlotSnapshot
	allAccountHits int
	userWaiting    int
	accountWaiting int
}

var _ service.SlotHolderCache = (*activeRequestsCacheStub)(nil)

func (s *activeRequestsCacheStub) AcquireAccountSlotWithHolder(context.Context, int64, int, string, *service.SlotHolder, string) (bool, error) {
	return true, nil
}

func (s *activeRequestsCacheStub) AcquireUserSlotWithHolder(context.Context, int64, int, string, *service.SlotHolder) (bool, error) {
	return true, nil
}

func (s *activeRequestsCacheStub) ListAccountSlots(context.Context, int64) (*service.ActiveSlotSnapshot, error) {
	return s.accountSlots, nil
}

func (s *activeRequestsCacheStub) ListUserSlots(context.Context, int64) (*service.ActiveSlotSnapshot, error) {
	return s.userSlots, nil
}

func (s *activeRequestsCacheStub) ListAllAccountSlots(context.Context) (*service.ActiveSlotSnapshot, error) {
	s.allAccountHits++
	return s.accountSlots, nil
}

func (s *activeRequestsCacheStub) GetAccountWaitingCount(context.Context, int64) (int, error) {
	return s.accountWaiting, nil
}

func (s *activeRequestsCacheStub) GetUsersLoadBatch(_ context.Context, users []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	out := make(map[int64]*service.UserLoadInfo, len(users))
	for _, u := range users {
		out[u.ID] = &service.UserLoadInfo{UserID: u.ID, WaitingCount: s.userWaiting}
	}
	return out, nil
}

type activeRequestsEnvelope struct {
	Code int                    `json:"code"`
	Data ActiveRequestsResponse `json:"data"`
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

func newUserActiveRequestsRouter(adminSvc *stubAdminService, cache *activeRequestsCacheStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewUserHandler(adminSvc, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/api/v1/admin/users/:id/active-requests", handler.GetActiveRequests)
	return router
}

func TestAccountActiveRequestsListsSlotHolders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cache := &activeRequestsCacheStub{
		accountWaiting: 2,
		accountSlots: &service.ActiveSlotSnapshot{Now: 100, Slots: []service.ActiveSlot{
			{SlotID: "a1", AccountID: 7, StartedAt: 90, Holder: &service.SlotHolder{UserID: 1, AccountID: 7}},
			{SlotID: "a2", AccountID: 7, StartedAt: 95},
		}},
	}
	adminSvc := newStubAdminService()
	adminSvc.getAccountResult = &service.Account{ID: 7, Name: "acc", Concurrency: 5}
	handler := &AccountHandler{adminService: adminSvc, concurrencyService: service.NewConcurrencyService(cache)}
	router := gin.New()
	router.GET("/api/v1/admin/accounts/:id/active-requests", handler.GetActiveRequests)

	got := getActiveRequests(t, router, "/api/v1/admin/accounts/7/active-requests")
	require.Equal(t, 2, got.Current)
	require.Equal(t, 5, got.Max)
	require.Equal(t, 2, got.Waiting)
	require.Len(t, got.Items, 2)
	require.True(t, got.Items[0].Recorded)
	require.Equal(t, int64(1), got.Items[0].UserID)
	require.False(t, got.Items[1].Recorded)
}

func TestLimitedUserActiveRequestsReadOnlyUserSlots(t *testing.T) {
	cache := &activeRequestsCacheStub{
		userWaiting: 1,
		userSlots: &service.ActiveSlotSnapshot{Now: 100, Slots: []service.ActiveSlot{
			{SlotID: "u1", AccountID: 41, StartedAt: 90, Holder: &service.SlotHolder{UserID: 1, AccountID: 41}},
			{SlotID: "u2", StartedAt: 95, Holder: &service.SlotHolder{UserID: 1}},
		}},
		accountSlots: &service.ActiveSlotSnapshot{Now: 100},
	}
	adminSvc := newStubAdminService()
	adminSvc.users[0].Concurrency = 3
	router := newUserActiveRequestsRouter(adminSvc, cache)

	got := getActiveRequests(t, router, "/api/v1/admin/users/1/active-requests")
	require.Equal(t, 2, got.Current)
	require.Equal(t, 3, got.Max)
	require.Equal(t, 1, got.Waiting)
	require.Equal(t, []int64{41, 0}, []int64{got.Items[0].AccountID, got.Items[1].AccountID})
	require.Zero(t, cache.allAccountHits, "limited users must not trigger the all-accounts scan")
}

func TestUnlimitedUserActiveRequestsComeFromAccountSlots(t *testing.T) {
	cache := &activeRequestsCacheStub{
		userSlots: &service.ActiveSlotSnapshot{Now: 100},
		accountSlots: &service.ActiveSlotSnapshot{Now: 100, Slots: []service.ActiveSlot{
			{SlotID: "a1", AccountID: 41, StartedAt: 90, Holder: &service.SlotHolder{UserID: 1, AccountID: 41}},
			{SlotID: "a2", AccountID: 42, StartedAt: 91, Holder: &service.SlotHolder{UserID: 2, AccountID: 42}},
		}},
	}
	adminSvc := newStubAdminService()
	adminSvc.users[0].Concurrency = 0
	router := newUserActiveRequestsRouter(adminSvc, cache)

	got := getActiveRequests(t, router, "/api/v1/admin/users/1/active-requests")
	require.Equal(t, 1, got.Current, "unlimited users report what is actually running")
	require.Zero(t, got.Max)
	require.Len(t, got.Items, 1)
	require.Equal(t, int64(41), got.Items[0].AccountID)
	require.Equal(t, 1, cache.allAccountHits)
}

func TestActiveRequestsRejectsInvalidID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/admin/users/:id/active-requests", NewUserHandler(newStubAdminService(), nil, nil, nil, nil, nil, nil).GetActiveRequests)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/users/abc/active-requests", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
