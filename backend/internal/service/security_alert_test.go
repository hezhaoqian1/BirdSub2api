package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func securityTestSnapshot(t *testing.T) securityRuleSnapshot {
	t.Helper()
	trusted, err := parseTrustedCIDRs([]string{"39.144.0.0/16"})
	require.NoError(t, err)
	return securityRuleSnapshot{
		cfg:           DefaultSecurityAlertConfig(),
		trusted:       trusted,
		knownPrefixes: map[string]struct{}{"155.254.124.0/24": {}},
		knownIPs:      map[string]struct{}{"155.254.124.36": {}},
		adminEmails:   map[string]struct{}{"admin@example.com": {}},
		baselineReady: true,
		loc:           time.FixedZone("CST", 8*3600),
	}
}

func TestSecurityNetworkPrefix(t *testing.T) {
	require.Equal(t, "155.254.124.0/24", securityNetworkPrefix("155.254.124.36"))
	require.Equal(t, "2001:db8:1::/48", securityNetworkPrefix("2001:db8:1:2::5"))
	require.Equal(t, "", securityNetworkPrefix("not-an-ip"))
}

func TestSecurityAlertConfigValidate(t *testing.T) {
	cfg := DefaultSecurityAlertConfig()
	require.NoError(t, cfg.Validate())
	cfg.TrustedCIDRs = []string{"10.0.0.0/8", "1.2.3.4"}
	require.NoError(t, cfg.Validate())
	cfg.TrustedCIDRs = []string{"nope"}
	require.Error(t, cfg.Validate())
	cfg = DefaultSecurityAlertConfig()
	cfg.DailyDigestHour = 24
	require.Error(t, cfg.Validate())
}

func TestEvaluateAuditRecordAdminLogin(t *testing.T) {
	snap := securityTestSnapshot(t)
	login := func(ip, email string, status int) *AuditLog {
		return &AuditLog{
			Action: AuditActionLogin, StatusCode: status, ClientIP: ip,
			RequestBody: `{"email":"` + email + `","password":"***"}`,
			CreatedAt:   time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC),
		}
	}

	drafts := evaluateAuditRecord(snap, login("52.54.207.165", "Admin@Example.com", 200))
	require.Len(t, drafts, 1)
	require.Equal(t, SecurityAlertCritical, drafts[0].Severity)
	require.Equal(t, "admin_net:52.54.207.0/24:2026-10", drafts[0].DedupeKey)
	require.Contains(t, drafts[0].body(), "52.54.207.165")

	require.Empty(t, evaluateAuditRecord(snap, login("155.254.124.36", "admin@example.com", 200)), "known IP")

	newIP := evaluateAuditRecord(snap, login("155.254.124.99", "admin@example.com", 200))
	require.Len(t, newIP, 1, "new IP inside a known /24")
	require.Equal(t, SecurityAlertInfo, newIP[0].Severity)
	require.Equal(t, "admin_ip:155.254.124.99", newIP[0].DedupeKey)
	snap.cfg.AdminNewIPNotify = false
	require.Empty(t, evaluateAuditRecord(snap, login("155.254.124.99", "admin@example.com", 200)), "new-IP notice disabled")
	snap.cfg.AdminNewIPNotify = true
	require.Empty(t, evaluateAuditRecord(snap, login("39.144.45.68", "admin@example.com", 200)), "trusted CIDR")
	require.Empty(t, evaluateAuditRecord(snap, login("52.54.207.165", "user@example.com", 200)), "not an admin")
	require.Empty(t, evaluateAuditRecord(snap, login("52.54.207.165", "admin@example.com", 401)), "failed login handled by periodic check")

	snap.baselineReady = false
	require.Empty(t, evaluateAuditRecord(snap, login("52.54.207.165", "admin@example.com", 200)), "no baseline yet")
}

func TestEvaluateAdminAccessSharesDedupeWithLogin(t *testing.T) {
	snap := securityTestSnapshot(t)
	at := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	d, ok := evaluateAdminAccess(snap, SecurityAdminAccess{
		Email: "admin@example.com", ClientIP: "52.54.207.10", AuthMethod: "admin_api_key", Path: "GET /api/v1/admin/accounts", At: at,
	})
	require.True(t, ok)
	require.Equal(t, adminNetworkDedupeKey("52.54.207.165", at, snap.loc), d.DedupeKey)
	require.Contains(t, d.body(), "管理员 API Key")

	_, ok = evaluateAdminAccess(snap, SecurityAdminAccess{ClientIP: "155.254.124.36", At: at})
	require.False(t, ok)
}

func TestEvaluateAuditRecordAdminWriteFromNewNetwork(t *testing.T) {
	snap := securityTestSnapshot(t)
	drafts := evaluateAuditRecord(snap, &AuditLog{
		Action: "admin.accounts.update", Method: "PUT", Path: "/api/v1/admin/accounts/:id",
		ActorRole: RoleAdmin, ActorEmail: "admin@example.com", ClientIP: "8.8.8.8", StatusCode: 200,
	})
	require.Len(t, drafts, 1)
	require.Equal(t, "admin_new_network", drafts[0].Kind)
}

func TestEvaluateSensitiveActions(t *testing.T) {
	snap := securityTestSnapshot(t)
	base := func(action, body string) *AuditLog {
		return &AuditLog{
			Action: action, ActorRole: RoleAdmin, ActorEmail: "admin@example.com", ClientIP: "155.254.124.36",
			StatusCode: 200, RequestBody: body, RequestID: "req-1",
			Extra: map[string]any{"params": map[string]string{"id": "43"}},
		}
	}

	big := evaluateAuditRecord(snap, base("admin.users.balance.create", `{"balance":5536,"notes":"wx 充值","operation":"add"}`))
	require.Len(t, big, 1)
	require.Equal(t, "大额余额调整", big[0].Title)
	require.Contains(t, big[0].body(), "目标用户 ID：43")
	require.Contains(t, big[0].body(), "增加 5536")

	require.Empty(t, evaluateAuditRecord(snap, base("admin.users.balance.create", `{"balance":10,"operation":"add"}`)))

	promote := evaluateAuditRecord(snap, base("admin.users.update", `{"role":"admin"}`))
	require.Len(t, promote, 1)
	require.Equal(t, SecurityAlertCritical, promote[0].Severity)

	require.Empty(t, evaluateAuditRecord(snap, base("admin.users.update", `{"notes":"vip"}`)))

	reset := evaluateAuditRecord(snap, base("admin.users.update", `{"password":"***"}`))
	require.Len(t, reset, 1)
	require.Equal(t, "后台重置了用户密码", reset[0].Title)

	clear := evaluateAuditRecord(snap, base(AuditActionAuditLogClear, ""))
	require.Len(t, clear, 1)
	require.Equal(t, SecurityAlertCritical, clear[0].Severity)

	totp := base("user.totp.disable.create", "")
	totp.Path = "/api/v1/user/totp/disable"
	require.Len(t, evaluateAuditRecord(snap, totp), 1)

	failed := base("admin.accounts.export", "")
	failed.StatusCode = 403
	require.Empty(t, evaluateAuditRecord(snap, failed))

	export := evaluateAuditRecord(snap, base("admin.accounts.export", ""))
	require.Len(t, export, 1)
	require.Equal(t, "audit:admin.accounts.export:req-1", export[0].DedupeKey)
}

func TestSanitizeSecurityTextSingleLine(t *testing.T) {
	got := sanitizeSecurityText("Mozilla/5.0\n🔴 严重 · 伪造的告警\r\n  x", 200)
	require.Equal(t, "Mozilla/5.0 🔴 严重 · 伪造的告警 x", got)
	require.Equal(t, "abc...", sanitizeSecurityText("abcdef", 3))
	require.Equal(t, "-", sanitizeSecurityText("  ", 10))
}

func TestRenderSecurityAlertMessageIsPlainText(t *testing.T) {
	snap := securityTestSnapshot(t)
	d, ok := evaluateAdminAccess(snap, SecurityAdminAccess{
		Email: "admin@example.com", ClientIP: "52.54.207.10", Path: "GET /api/v1/admin/accounts",
		At: time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC),
	})
	require.True(t, ok)
	msg := renderSecurityAlertMessage("BirdApi", []pendingSecurityAlert{{Severity: d.Severity, Title: d.Title, Body: d.body()}})
	require.True(t, strings.HasPrefix(msg, "【BirdApi 安全告警】\n\n🔴 严重 · 陌生网络正在访问管理后台\n账号：admin@example.com\n"), msg)
	require.Contains(t, msg, "\n查归属：https://ipinfo.io/52.54.207.10\n")
	for _, markup := range []string{"**", "> ", "<font", "###", "]("} {
		require.NotContains(t, msg, markup)
	}
}

func TestWeComWebhookValidationAndMask(t *testing.T) {
	good := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=5e74056c-4372-4545-8355-f9129e030930"
	require.NoError(t, ValidateWeComRobotWebhook(good))
	require.Error(t, ValidateWeComRobotWebhook("http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x"))
	require.Error(t, ValidateWeComRobotWebhook("https://evil.example.com/cgi-bin/webhook/send?key=x"))
	require.Error(t, ValidateWeComRobotWebhook("https://qyapi.weixin.qq.com/cgi-bin/webhook/send"))

	masked := MaskWeComRobotWebhook(good)
	require.Equal(t, "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=5e74****0930", masked)
	require.NotContains(t, masked, "4372")
	require.Equal(t, "", MaskWeComRobotWebhook(""))
}

func TestWeComRobotSenderSend(t *testing.T) {
	var gotBody map[string]any
	sender := &weComRobotSender{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "qyapi.weixin.qq.com", r.URL.Host)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &gotBody))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errcode":0,"errmsg":"ok"}`))}, nil
	})}}
	webhook := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc123456789"
	require.NoError(t, sender.Send(context.Background(), webhook, "hello"))
	require.Equal(t, "text", gotBody["msgtype"])
	require.Equal(t, map[string]any{"content": "hello"}, gotBody["text"])

	rejecting := &weComRobotSender{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errcode":93000,"errmsg":"invalid webhook url"}`))}, nil
	})}}
	err := rejecting.Send(context.Background(), webhook, "hello")
	require.ErrorContains(t, err, "93000")

	failing := &weComRobotSender{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}}
	err = failing.Send(context.Background(), webhook, "hello")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "abc123456789", "error must not leak the robot key")
}

func TestGroupSecurityAlertsRespectsSizeLimit(t *testing.T) {
	long := strings.Repeat("长", 300) // 900 bytes
	events := []pendingSecurityAlert{
		{ID: 1, Title: "a", Body: long},
		{ID: 2, Title: "b", Body: long},
		{ID: 3, Title: "c", Body: long},
		{ID: 4, Title: "d", Body: "short"},
	}
	groups := groupSecurityAlerts("BirdApi", events)
	require.Len(t, groups, 2)
	for _, g := range groups {
		require.LessOrEqual(t, len(renderSecurityAlertMessage("BirdApi", g)), weComRobotMaxContentBytes)
	}
	require.Equal(t, int64(1), groups[0][0].ID)
	require.Equal(t, int64(4), groups[1][len(groups[1])-1].ID)
}

func TestTruncateUTF8Bytes(t *testing.T) {
	out := truncateUTF8Bytes(strings.Repeat("好", 10), 10)
	require.True(t, strings.HasPrefix(out, "好好好"))
	require.NotContains(t, out, "�")
}

type recordingSecuritySender struct {
	messages []string
	err      error
}

func (r *recordingSecuritySender) Send(_ context.Context, _ string, content string) error {
	r.messages = append(r.messages, content)
	return r.err
}

func TestDeliverPendingMarksSentAndRetries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sendErr error
	}{{"success", nil}, {"failure", errors.New("boom")}} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()

			sender := &recordingSecuritySender{err: tc.sendErr}
			svc := NewSecurityAlertService(db, nil, nil)
			svc.sender = sender
			svc.webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc123456789"

			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_events")).
				WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "severity", "title", "body", "created_at"}).
					AddRow(int64(7), "admin_new_network", "critical", "陌生网络正在访问管理后台", "IP：1.2.3.4", time.Now()))
			mock.ExpectExec(regexp.QuoteMeta("SET locked_at = NOW()")).WithArgs(int64(7), sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			if tc.sendErr == nil {
				mock.ExpectExec(regexp.QuoteMeta("SET sent_at = NOW()")).WithArgs(int64(7), sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec(regexp.QuoteMeta("next_attempt_at = NOW()")).WithArgs(int64(7), sqlmock.AnyArg(), "boom").
					WillReturnResult(sqlmock.NewResult(0, 1))
			}

			svc.deliverPending(context.Background())
			require.NoError(t, mock.ExpectationsWereMet())
			require.Len(t, sender.messages, 1)
			require.Contains(t, sender.messages[0], "陌生网络正在访问管理后台")
			require.Contains(t, sender.messages[0], "🔴 严重")
		})
	}
}

func TestDeliverPendingSkipsWithoutWebhook(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	svc := NewSecurityAlertService(db, nil, nil)
	svc.deliverPending(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
}

type captureObserver struct{ entries []*AuditLog }

func (c *captureObserver) OnAuditRecord(entry *AuditLog) { c.entries = append(c.entries, entry) }

func TestAuditLogServiceNotifiesObserver(t *testing.T) {
	svc := NewAuditLogService(nil, nil)
	obs := &captureObserver{}
	svc.SetObserver(obs)
	svc.Record(&AuditLog{Action: AuditActionLogin, StatusCode: 200})
	require.Len(t, obs.entries, 1)
	require.False(t, obs.entries[0].CreatedAt.IsZero())

	// Observers that do not implement AdminAccessObserver are simply skipped.
	svc.ObserveAdminAccess(SecurityAdminAccess{ClientIP: "1.2.3.4"})
}

func TestObserveAdminAccessLocalSuppression(t *testing.T) {
	svc := NewSecurityAlertService(nil, nil, nil)
	svc.ObserveAdminAccess(SecurityAdminAccess{ClientIP: "8.8.8.8"})
	svc.ObserveAdminAccess(SecurityAdminAccess{ClientIP: "8.8.8.9"}) // same /24, suppressed
	svc.ObserveAdminAccess(SecurityAdminAccess{ClientIP: "9.9.9.9"})
	svc.ObserveAdminAccess(SecurityAdminAccess{ClientIP: "bogus"}) // ignored
	require.Len(t, svc.observeCh, 2)
}
