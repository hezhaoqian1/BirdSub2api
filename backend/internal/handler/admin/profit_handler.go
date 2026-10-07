package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ProfitHandler 提供利润统计与一次性历史成本回填接口。
type ProfitHandler struct {
	profitService *service.ProfitStatsService
}

func NewProfitHandler(profitService *service.ProfitStatsService) *ProfitHandler {
	return &ProfitHandler{profitService: profitService}
}

// GetReport GET /api/v1/admin/profit/report
// Query: start_date, end_date (YYYY-MM-DD, 含当天), timezone, exclude_admin (默认 true), group_id
func (h *ProfitHandler) GetReport(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	excludeAdmin := true
	if raw := strings.TrimSpace(c.Query("exclude_admin")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			response.BadRequest(c, "Invalid exclude_admin")
			return
		}
		excludeAdmin = value
	}
	var groupID int64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			response.BadRequest(c, "Invalid group_id")
			return
		}
		groupID = value
	}

	report, err := h.profitService.GetReport(c.Request.Context(), service.ProfitStatsFilter{
		StartTime:    startTime,
		EndTime:      endTime,
		Timezone:     c.Query("timezone"),
		ExcludeAdmin: excludeAdmin,
		GroupID:      groupID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, report)
}

func parseProfitBackfillStart(raw, userTZ string) (time.Time, bool) {
	start, err := timezone.ParseInUserLocation("2006-01-02", strings.TrimSpace(raw), userTZ)
	if err != nil {
		return time.Time{}, false
	}
	return start, true
}

// PreviewCostBackfill GET /api/v1/admin/profit/cost-backfill/preview?start_date=YYYY-MM-DD&timezone=
func (h *ProfitHandler) PreviewCostBackfill(c *gin.Context) {
	start, ok := parseProfitBackfillStart(c.Query("start_date"), c.Query("timezone"))
	if !ok {
		response.BadRequest(c, "Invalid start_date")
		return
	}
	preview, err := h.profitService.PreviewCostRateBackfill(c.Request.Context(), start)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, preview)
}

type profitCostBackfillRequest struct {
	StartDate  string  `json:"start_date" binding:"required"`
	Timezone   string  `json:"timezone"`
	AccountIDs []int64 `json:"account_ids"`
}

// ApplyCostBackfill POST /api/v1/admin/profit/cost-backfill
func (h *ProfitHandler) ApplyCostBackfill(c *gin.Context) {
	var req profitCostBackfillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	start, ok := parseProfitBackfillStart(req.StartDate, req.Timezone)
	if !ok {
		response.BadRequest(c, "Invalid start_date")
		return
	}
	state, err := h.profitService.ApplyCostRateBackfill(c.Request.Context(), start, req.AccountIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
