package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const securityAlertAdvice = "如果不是你本人操作：立即修改管理员密码、开启两步验证，并轮换 JWT 密钥。"

// evaluateAdminAccess 管理员从陌生网络访问后台（含伪造/被盗的登录态、Admin API Key）。
func evaluateAdminAccess(snap securityRuleSnapshot, access SecurityAdminAccess) (securityAlertDraft, bool) {
	if snap.isKnownNetwork(access.ClientIP) {
		return securityAlertDraft{}, false
	}
	method := "登录态（JWT）"
	if access.AuthMethod == "admin_api_key" {
		method = "管理员 API Key"
	}
	return securityAlertDraft{
		DedupeKey: adminNetworkDedupeKey(access.ClientIP, access.At, snap.loc),
		Kind:      "admin_new_network",
		Severity:  SecurityAlertCritical,
		Title:     "陌生网络正在访问管理后台",
		Lines: []string{
			"账号：" + sanitizeSecurityText(access.Email, 80),
			"IP：" + formatSecurityIP(access.ClientIP) + "（网段 " + securityNetworkPrefix(access.ClientIP) + " 近 90 天未用过）",
			securityIPLookupLine(access.ClientIP),
			"认证方式：" + method,
			"接口：" + sanitizeSecurityText(access.Path, 80),
			"设备：" + sanitizeSecurityText(access.UserAgent, 80),
			"时间：" + formatSecurityTime(access.At, snap.loc),
			"如果是你自己换了代理节点或网络，可以忽略；同一网段本月不再重复提醒。",
			securityAlertAdvice,
		},
	}, true
}

// adminNetworkDedupeKey 同一网段每月只告警一次（登录与访问共用，避免一次事件两条消息）。
func adminNetworkDedupeKey(ip string, at time.Time, loc *time.Location) string {
	if at.IsZero() {
		at = time.Now()
	}
	return "admin_net:" + securityNetworkPrefix(ip) + ":" + at.In(loc).Format("2006-01")
}

// evaluateAuditRecord 对单条审计记录做实时判定。
func evaluateAuditRecord(snap securityRuleSnapshot, rec *AuditLog) []securityAlertDraft {
	if rec == nil {
		return nil
	}
	ok := rec.StatusCode >= 200 && rec.StatusCode < 300
	var drafts []securityAlertDraft

	// 1. 管理员账号登录成功（含进入两步验证前的密码校验通过）且来自陌生网络。
	if ok && (rec.Action == AuditActionLogin || rec.Action == AuditActionLogin2FA) {
		email := rec.ActorEmail
		if email == "" {
			email = auditBodyString(rec.RequestBody, "email")
		}
		isAdmin := rec.ActorRole == RoleAdmin || snap.isAdminEmail(email)
		switch {
		case !isAdmin:
		case !snap.isKnownNetwork(rec.ClientIP):
			drafts = append(drafts, securityAlertDraft{
				DedupeKey: adminNetworkDedupeKey(rec.ClientIP, rec.CreatedAt, snap.loc),
				Kind:      "admin_login_new_network",
				Severity:  SecurityAlertCritical,
				Title:     "管理员账号在陌生网络登录成功",
				Lines: []string{
					"账号：" + sanitizeSecurityText(email, 80),
					"IP：" + formatSecurityIP(rec.ClientIP) + "（网段 " + securityNetworkPrefix(rec.ClientIP) + " 近 90 天未用过）",
					securityIPLookupLine(rec.ClientIP),
					"设备：" + sanitizeSecurityText(rec.UserAgent, 80),
					"时间：" + formatSecurityTime(rec.CreatedAt, snap.loc),
					"说明：密码已校验通过。如果是你自己换了代理节点或网络，可以忽略。",
					securityAlertAdvice,
				},
			})
		case snap.cfg.AdminNewIPNotify && !snap.isKnownIP(rec.ClientIP):
			drafts = append(drafts, securityAlertDraft{
				DedupeKey: "admin_ip:" + strings.TrimSpace(rec.ClientIP),
				Kind:      "admin_login_new_ip",
				Severity:  SecurityAlertInfo,
				Title:     "管理员账号从新 IP 登录",
				Lines: []string{
					"账号：" + sanitizeSecurityText(email, 80),
					"IP：" + formatSecurityIP(rec.ClientIP) + "（所在网段 " + securityNetworkPrefix(rec.ClientIP) + " 你用过，这个 IP 是第一次）",
					securityIPLookupLine(rec.ClientIP),
					"设备：" + sanitizeSecurityText(rec.UserAgent, 80),
					"时间：" + formatSecurityTime(rec.CreatedAt, snap.loc),
					"多半是代理换了出口；如果这个时间你没登录，请立即改密码。",
				},
			})
		}
		return drafts
	}

	// 2. 管理员身份的写操作来自陌生网络（中间件观察的兜底）。
	if rec.ActorRole == RoleAdmin && !snap.isKnownNetwork(rec.ClientIP) {
		var userID int64
		if rec.ActorUserID != nil {
			userID = *rec.ActorUserID
		}
		if d, hit := evaluateAdminAccess(snap, SecurityAdminAccess{
			UserID: userID, Email: rec.ActorEmail, ClientIP: rec.ClientIP, UserAgent: rec.UserAgent,
			AuthMethod: rec.AuthMethod, Path: rec.Method + " " + rec.Path, At: rec.CreatedAt,
		}); hit {
			drafts = append(drafts, d)
		}
	}

	// 3. 敏感操作。
	if ok {
		if d, hit := evaluateSensitiveAction(snap, rec); hit {
			drafts = append(drafts, d)
		}
	}
	return drafts
}

func evaluateSensitiveAction(snap securityRuleSnapshot, rec *AuditLog) (securityAlertDraft, bool) {
	severity := SecurityAlertWarning
	kind := "sensitive_action"
	title := ""
	var details []string

	switch rec.Action {
	case "admin.accounts.export":
		title = "后台导出了账号数据（含上游密钥）"
	case "admin.proxies.export":
		title = "后台导出了代理数据"
	case "admin.redeem_codes.export":
		title = "后台导出了兑换码"
	case "admin.backups.download":
		title = "后台下载了数据库备份"
	case "admin.backups.restore":
		title, severity = "后台执行了数据库备份恢复", SecurityAlertCritical
	case "admin.accounts.import":
		title = "后台批量导入了账号"
	case AuditActionAuditLogClear:
		title, severity = "审计日志被清空", SecurityAlertCritical
	case "admin.admin_api_key.regenerate":
		title, severity = "管理员 API Key 被重新生成", SecurityAlertCritical
	case "admin.admin_api_key.delete":
		title = "管理员 API Key 被删除"
	case "admin.users.balance.create":
		amount, _ := auditBodyNumber(rec.RequestBody, "balance")
		if snap.cfg.BalanceAdjustThreshold <= 0 || amount < snap.cfg.BalanceAdjustThreshold {
			return securityAlertDraft{}, false
		}
		title = "大额余额调整"
		op := map[string]string{"add": "增加", "subtract": "扣减", "set": "设为"}[auditBodyString(rec.RequestBody, "operation")]
		if op == "" {
			op = "调整"
		}
		details = append(details,
			"目标用户 ID："+auditRouteParam(rec, "id"),
			fmt.Sprintf("操作：%s %s", op, strconv.FormatFloat(amount, 'f', -1, 64)),
			"备注："+sanitizeSecurityText(auditBodyString(rec.RequestBody, "notes"), 80))
	case "admin.redeem_codes.generate.create":
		title = "后台生成了兑换码"
		count, _ := auditBodyNumber(rec.RequestBody, "count")
		value, _ := auditBodyNumber(rec.RequestBody, "value")
		details = append(details, fmt.Sprintf("数量 %s，面值 %s，类型 %s",
			strconv.FormatFloat(count, 'f', -1, 64), strconv.FormatFloat(value, 'f', -1, 64),
			sanitizeSecurityText(auditBodyString(rec.RequestBody, "type"), 30)))
	case "admin.users.create":
		title = "后台新建了用户"
		if auditBodyString(rec.RequestBody, "role") == RoleAdmin {
			title, severity = "后台新建了管理员账号", SecurityAlertCritical
		}
		details = append(details, "邮箱："+sanitizeSecurityText(auditBodyString(rec.RequestBody, "email"), 80))
	case "admin.users.update":
		switch {
		case auditBodyString(rec.RequestBody, "role") == RoleAdmin:
			title, severity = "用户被设置为管理员", SecurityAlertCritical
		case auditBodyHasKey(rec.RequestBody, "password"):
			title = "后台重置了用户密码"
		default:
			return securityAlertDraft{}, false
		}
		details = append(details, "目标用户 ID："+auditRouteParam(rec, "id"))
	case "admin.payment.providers.create", "admin.payment.providers.update", "admin.payment.providers.delete",
		"admin.payment.config.update":
		title = "支付配置被修改"
	case "admin.settings.update":
		title, severity = "系统设置被修改", SecurityAlertInfo
	}

	if title == "" && rec.ActorRole == RoleAdmin {
		switch rec.Path {
		case "/api/v1/user/totp/disable":
			title, severity = "管理员关闭了两步验证", SecurityAlertCritical
		case "/api/v1/user/password":
			title = "管理员账号修改了密码"
		}
	}
	if title == "" {
		return securityAlertDraft{}, false
	}

	requestID := rec.RequestID
	if requestID == "" {
		requestID = strconv.FormatInt(rec.CreatedAt.UnixNano(), 10)
	}
	lines := []string{
		"操作人：" + sanitizeSecurityText(rec.ActorEmail, 80),
		"IP：" + formatSecurityIP(rec.ClientIP),
	}
	lines = append(lines, details...)
	lines = append(lines, "时间："+formatSecurityTime(rec.CreatedAt, snap.loc))
	return securityAlertDraft{
		DedupeKey: "audit:" + rec.Action + ":" + requestID,
		Kind:      kind,
		Severity:  severity,
		Title:     title,
		Lines:     lines,
	}, true
}

// auditBody 解析已脱敏的请求体 JSON；非 JSON 返回 nil。
func auditBody(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return m
}

func auditBodyString(raw, key string) string {
	if v, ok := auditBody(raw)[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func auditBodyNumber(raw, key string) (float64, bool) {
	switch v := auditBody(raw)[key].(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

func auditBodyHasKey(raw, key string) bool {
	_, ok := auditBody(raw)[key]
	return ok
}

func auditRouteParam(rec *AuditLog, name string) string {
	if rec == nil || rec.Extra == nil {
		return ""
	}
	switch params := rec.Extra["params"].(type) {
	case map[string]any:
		if v, ok := params[name]; ok {
			return sanitizeSecurityText(fmt.Sprint(v), 40)
		}
	case map[string]string:
		return sanitizeSecurityText(params[name], 40)
	}
	return ""
}

// sanitizeSecurityText 压成单行并截断，防止外部输入（如 UA）伪造出额外的告警行。
func sanitizeSecurityText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return "-"
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return value
}

func formatSecurityIP(ip string) string {
	return sanitizeSecurityText(ip, 64)
}

// securityIPLookupLine 单独一行放归属查询链接，企业微信会自动识别成可点击的网址。
func securityIPLookupLine(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	return "查归属：https://ipinfo.io/" + sanitizeSecurityText(ip, 64)
}

func formatSecurityTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		t = time.Now()
	}
	if loc != nil {
		t = t.In(loc)
	}
	return t.Format("2006-01-02 15:04:05")
}
