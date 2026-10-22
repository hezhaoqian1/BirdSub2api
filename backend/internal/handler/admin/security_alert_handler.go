package admin

import (
	"encoding/json"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// SecurityAlertHandler 安全告警配置与记录接口。
type SecurityAlertHandler struct {
	service *service.SecurityAlertService
}

// NewSecurityAlertHandler 创建安全告警处理器。
func NewSecurityAlertHandler(svc *service.SecurityAlertService) *SecurityAlertHandler {
	return &SecurityAlertHandler{service: svc}
}

// GetConfig 读取配置（机器人地址只返回掩码）。
// GET /api/v1/admin/security-alerts/config
func (h *SecurityAlertHandler) GetConfig(c *gin.Context) {
	view, err := h.service.GetConfigView(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

type updateSecurityAlertConfigRequest struct {
	Config       json.RawMessage `json:"config"`
	WeComWebhook *string         `json:"wecom_webhook"`
}

// UpdateConfig 更新配置；config 为部分字段时与当前配置合并，wecom_webhook 为空字符串表示清除。
// PUT /api/v1/admin/security-alerts/config
func (h *SecurityAlertHandler) UpdateConfig(c *gin.Context) {
	var req updateSecurityAlertConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	view, err := h.service.UpdateConfig(c.Request.Context(), service.UpdateSecurityAlertConfigInput{
		ConfigPatch:  req.Config,
		WeComWebhook: req.WeComWebhook,
		Operator:     c.GetString(middleware.ContextKeyAuthEmail),
		ClientIP:     middleware.SecurityClientIP(c),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, view)
}

type testSecurityAlertRequest struct {
	WeComWebhook string `json:"wecom_webhook"`
}

// SendTest 发送测试消息；可传入未保存的机器人地址先验证。
// POST /api/v1/admin/security-alerts/test
func (h *SecurityAlertHandler) SendTest(c *gin.Context) {
	var req testSecurityAlertRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "Invalid request: "+err.Error())
			return
		}
	}
	if err := h.service.SendTest(c.Request.Context(), req.WeComWebhook); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"sent": true})
}

// ListEvents 最近的告警记录。
// GET /api/v1/admin/security-alerts/events?limit=50
func (h *SecurityAlertHandler) ListEvents(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	events, err := h.service.ListEvents(c.Request.Context(), limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, events)
}
