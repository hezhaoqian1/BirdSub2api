package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *ChannelMonitorV2Handler) HybridSnapshot(c *gin.Context) {
	admin := channelMonitorV2IsAdmin(c)
	filter := service.ChannelMonitorV2Filter{}
	if !h.scopeFilter(c, &filter, admin) {
		return
	}
	cfg, items, err := h.service.Hybrid.Snapshot(c.Request.Context(), admin, filter.AllowedGroupIDs)
	if err != nil {
		response.InternalError(c, "无法读取融合监控")
		return
	}
	payload := gin.H{"config": cfg, "items": items}
	if admin {
		events, eventErr := h.service.Hybrid.Notifications(c.Request.Context())
		if eventErr != nil {
			response.InternalError(c, "无法读取通知记录")
			return
		}
		payload["notifications"] = events
	}
	response.Success(c, payload)
}

func (h *ChannelMonitorV2Handler) HybridSave(c *gin.Context) {
	var cfg service.HybridMonitorConfig
	if c.ShouldBindJSON(&cfg) != nil {
		response.BadRequest(c, "无效的监控配置")
		return
	}
	saved, err := h.service.Hybrid.Save(c.Request.Context(), cfg)
	if err == service.ErrHybridConflict {
		response.Error(c, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, saved)
}

func (h *ChannelMonitorV2Handler) HybridTest(c *gin.Context) {
	if err := h.service.Hybrid.TestNotification(c.Request.Context()); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"sent": true})
}
