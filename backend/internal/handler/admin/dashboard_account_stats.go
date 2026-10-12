package admin

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/gin-gonic/gin"
)

var dashboardAccountStatsCache = newSnapshotCache(30 * time.Second)

// GetAccountStats returns upstream account costs within the selected usage filters.
// GET /api/v1/admin/dashboard/accounts
func (h *DashboardHandler) GetAccountStats(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	parsed, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	filters := usagestats.UsageLogFilters{
		UserID: parsed.UserID, APIKeyID: parsed.APIKeyID, AccountID: parsed.AccountID, GroupID: parsed.GroupID,
		Model: parsed.Model, RequestType: parsed.RequestType, Stream: parsed.Stream,
		NativeCompactionV2: parsed.NativeCompactionV2, BillingType: parsed.BillingType,
		BillingMode: strings.TrimSpace(c.Query("billing_mode")), UpstreamModelMismatch: parsed.UpstreamModelMismatch,
	}
	key := mustMarshalDashboardCacheKey(struct {
		StartTime time.Time
		EndTime   time.Time
		Filters   usagestats.UsageLogFilters
	}{startTime.UTC(), endTime.UTC(), filters})
	cached, hit, err := dashboardAccountStatsCache.GetOrLoad(key, func() (any, error) {
		return h.dashboardService.GetAccountStatsWithUsageFilters(c.Request.Context(), startTime, endTime, filters)
	})
	if err != nil {
		response.Error(c, 500, "Failed to get account usage statistics")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, gin.H{
		"accounts":   cached.Payload,
		"start_date": startTime.Format("2006-01-02"),
		"end_date":   endTime.AddDate(0, 0, -1).Format("2006-01-02"),
	})
}
