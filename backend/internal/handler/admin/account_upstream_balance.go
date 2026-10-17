package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) SetUpstreamBalanceService(s *service.UpstreamBalanceService) {
	h.upstreamBalance = s
}

func (h *AccountHandler) QueryUpstreamBalances(c *gin.Context) {
	if h.upstreamBalance == nil {
		response.Error(c, 503, "余额查询暂不可用")
		return
	}
	var input struct {
		IDs     []int64 `json:"account_ids" binding:"required,min=1,max=100,dive,gt=0"`
		Refresh bool    `json:"refresh"`
		Force   bool    `json:"force"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "账号列表无效（最多 100 条）")
		return
	}
	if input.Refresh && len(input.IDs) > 10 {
		response.BadRequest(c, "每批最多刷新 10 条账号")
		return
	}
	response.Success(c, h.upstreamBalance.Batch(c.Request.Context(), input.IDs, input.Refresh, input.Force))
}

func (h *AccountHandler) SaveUpstreamBalanceConfig(c *gin.Context) {
	if h.upstreamBalance == nil {
		response.Error(c, 503, "余额查询暂不可用")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		response.BadRequest(c, "账号 ID 无效")
		return
	}
	var input service.UpstreamBalanceConfig
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "余额配置格式无效")
		return
	}
	state, err := h.upstreamBalance.Save(c.Request.Context(), id, input)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, state)
}
