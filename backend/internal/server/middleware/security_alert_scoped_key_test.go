//go:build unit

package middleware

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminAccessCapture struct {
	accesses []service.SecurityAdminAccess
}

func (c *adminAccessCapture) OnAuditRecord(*service.AuditLog) {}
func (c *adminAccessCapture) ObserveAdminAccess(access service.SecurityAdminAccess) {
	c.accesses = append(c.accesses, access)
}

// 受限管理密钥（adm_sc_）的请求同样要交给安全告警做陌生网络检测。
func TestScopedAdminKeyRequestsReachSecurityObserver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := service.NewSettingService(nil, nil)
	settings.SetScopedAdminKeyRepository(&adminAuthKeyRepo{})
	key, _, err := settings.CreateScopedAdminKey(context.Background(), service.ScopedAdminKeyInput{Name: "只读", Enabled: true, Policy: adminaccess.Policy{GlobalPermissions: []string{"users.read"}}}, 9)
	require.NoError(t, err)
	users := service.NewUserService(&stubUserRepo{getByID: func(context.Context, int64) (*service.User, error) {
		return &service.User{ID: 9, Email: "admin@example.com", Role: service.RoleAdmin, Status: service.StatusActive}, nil
	}}, nil, nil, nil)
	capture := &adminAccessCapture{}
	audit := service.NewAuditLogService(nil, nil)
	audit.SetObserver(capture)

	r := gin.New()
	r.Use(gin.HandlerFunc(NewAdminAuthMiddleware(nil, users, settings, audit)))
	r.GET("/api/v1/admin/users", func(c *gin.Context) { c.Status(204) })
	req := httptest.NewRequest("GET", "/api/v1/admin/users", nil)
	req.Header.Set("x-api-key", key)
	req.RemoteAddr = "8.8.8.8:1234"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.Len(t, capture.accesses, 1)
	got := capture.accesses[0]
	require.Equal(t, service.AuditAuthMethodAdminAPIKey, got.AuthMethod)
	require.Equal(t, "admin@example.com", got.Email)
	require.Equal(t, int64(9), got.UserID)
	require.Equal(t, "GET /api/v1/admin/users", got.Path)
}

// 安全告警规则按这些推导出的动作名匹配受限管理密钥的生命周期操作。
func TestScopedAdminKeyLifecycleAuditActions(t *testing.T) {
	cases := map[string]string{
		"POST /api/v1/admin/settings/admin-api-keys":              "admin.settings.admin_api_keys.create",
		"PUT /api/v1/admin/settings/admin-api-keys/:id":           "admin.settings.admin_api_keys.update",
		"POST /api/v1/admin/settings/admin-api-keys/:id/rotate":   "admin.settings.admin_api_keys.rotate.create",
		"DELETE /api/v1/admin/settings/admin-api-keys/:id":        "admin.settings.admin_api_keys.delete",
		"DELETE /api/v1/admin/settings/admin-api-keys/:id/record": "admin.settings.admin_api_keys.record.delete",
	}
	for route, want := range cases {
		method, path, _ := strings.Cut(route, " ")
		require.Equal(t, want, deriveAuditAction(method, path), route)
	}
}
