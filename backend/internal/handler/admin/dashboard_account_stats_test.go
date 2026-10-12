package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountStatsRepoCapture struct {
	service.UsageLogRepository
	filters []usagestats.UsageLogFilters
}

func (r *accountStatsRepoCapture) GetAccountStatsWithUsageFilters(_ context.Context, _, _ time.Time, f usagestats.UsageLogFilters) ([]usagestats.AccountStat, error) {
	r.filters = append(r.filters, f)
	return []usagestats.AccountStat{{AccountID: 7, AccountName: "upstream", Cost: 10, ActualCost: 8, AccountCost: 3}}, nil
}
func TestDashboardAccountStatsFiltersAndCacheIsolation(t *testing.T) {
	dashboardAccountStatsCache = newSnapshotCache(30 * time.Second)
	t.Cleanup(func() { dashboardAccountStatsCache = newSnapshotCache(30 * time.Second) })
	repo := &accountStatsRepoCapture{}
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/accounts", handler.GetAccountStats)
	base := "/accounts?start_date=2026-10-01&end_date=2026-10-02&user_id=3&api_key_id=4&account_id=7&group_id=8&request_type=ws_v2&stream=invalid&native_compaction_v2=true&billing_type=1&upstream_model_mismatch=true&billing_mode=token&model="
	for i, model := range []string{"model-a", "model-a", "model-b"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+model, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"account_cost":3`)
		require.Contains(t, rec.Body.String(), `"actual_cost":8`)
		if i == 1 {
			require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
		} else {
			require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
		}
	}
	require.Len(t, repo.filters, 2)
	f := repo.filters[0]
	require.Equal(t, int64(8), f.GroupID)
	require.Equal(t, int64(7), f.AccountID)
	require.Equal(t, int64(3), f.UserID)
	require.Equal(t, int64(4), f.APIKeyID)
	require.Equal(t, "model-a", f.Model)
	require.Equal(t, "token", f.BillingMode)
	require.Equal(t, int16(service.RequestTypeWSV2), *f.RequestType)
	require.Nil(t, f.Stream)
	require.True(t, *f.NativeCompactionV2)
	require.True(t, *f.UpstreamModelMismatch)
	require.Equal(t, int8(1), *f.BillingType)
	require.Equal(t, "model-b", repo.filters[1].Model)
	groupRec := httptest.NewRecorder()
	router.ServeHTTP(groupRec, httptest.NewRequest(http.MethodGet, strings.Replace(base, "group_id=8", "group_id=9", 1)+"model-a", nil))
	require.Equal(t, http.StatusOK, groupRec.Code)
	require.Equal(t, "miss", groupRec.Header().Get("X-Snapshot-Cache"))
	require.Len(t, repo.filters, 3)
	require.Equal(t, int64(9), repo.filters[2].GroupID)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts?group_id=bad", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
