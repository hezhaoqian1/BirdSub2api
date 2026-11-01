package admin

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// 渠道保真体检（只开放给管理员，挂在 /api/v1/admin/security-alerts/fidelity 下）。

// GetFidelityStatus 体检目标的最新结果与近 24 小时历史。
// GET /api/v1/admin/security-alerts/fidelity/status
func (h *SecurityAlertHandler) GetFidelityStatus(c *gin.Context) {
	status, err := h.service.GetFidelityStatus(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

// GetFidelityConfig 读取保真体检配置。
// GET /api/v1/admin/security-alerts/fidelity/config
func (h *SecurityAlertHandler) GetFidelityConfig(c *gin.Context) {
	cfg, err := h.service.GetFidelityConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

type updateFidelityConfigRequest struct {
	Config json.RawMessage `json:"config"`
}

// UpdateFidelityConfig 更新保真体检配置；config 为部分字段时与当前配置合并。
// PUT /api/v1/admin/security-alerts/fidelity/config
func (h *SecurityAlertHandler) UpdateFidelityConfig(c *gin.Context) {
	var req updateFidelityConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.service.UpdateFidelityConfig(c.Request.Context(), req.Config)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, cfg)
}

// RunFidelitySweep 立即在后台体检全部目标。
// POST /api/v1/admin/security-alerts/fidelity/run
func (h *SecurityAlertHandler) RunFidelitySweep(c *gin.Context) {
	started, err := h.service.RunFidelitySweepNow(c.Request.Context())
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"started": started})
}

type fidelityTargetRequest struct {
	AccountID     int64  `json:"account_id"`
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model"`
}

// RunFidelityCheck 立即体检一个（账号，模型），同步返回结果。
// POST /api/v1/admin/security-alerts/fidelity/check
func (h *SecurityAlertHandler) RunFidelityCheck(c *gin.Context) {
	var req fidelityTargetRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.AccountID <= 0 || strings.TrimSpace(req.Model) == "" {
		response.BadRequest(c, "account_id and model are required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	res, err := h.service.RunFidelityCheck(ctx, req.AccountID, req.Model)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, res)
}

// ResetFidelityBaseline 删除 prompt_tokens 基线，下次全部通过时重新记录。
// POST /api/v1/admin/security-alerts/fidelity/reset-baseline
func (h *SecurityAlertHandler) ResetFidelityBaseline(c *gin.Context) {
	var req fidelityTargetRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.AccountID <= 0 || strings.TrimSpace(req.UpstreamModel) == "" {
		response.BadRequest(c, "account_id and upstream_model are required")
		return
	}
	if err := h.service.ResetFidelityBaseline(c.Request.Context(), req.AccountID, req.UpstreamModel); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"reset": true})
}
