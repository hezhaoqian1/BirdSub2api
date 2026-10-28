package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	weComRobotTimeout = 8 * time.Second
	weComRobotMaxBody = 64 * 1024
	// 企业微信 text 消息内容上限 2048 字节，留出截断提示的余量。
	weComRobotMaxContentBytes = 2000
	securityAlertDeliverBatch = 20
)

type weComRobotSender struct {
	client *http.Client
}

func newWeComRobotSender() *weComRobotSender {
	return &weComRobotSender{client: &http.Client{
		Timeout: weComRobotTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// ValidateWeComRobotWebhook 校验企业微信群机器人地址，错误信息不包含 key。
func ValidateWeComRobotWebhook(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("invalid wecom webhook URL")
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "qyapi.weixin.qq.com") || u.Path != "/cgi-bin/webhook/send" {
		return errors.New("wecom webhook must use https://qyapi.weixin.qq.com/cgi-bin/webhook/send")
	}
	if strings.TrimSpace(u.Query().Get("key")) == "" {
		return errors.New("wecom webhook is missing key")
	}
	return nil
}

// MaskWeComRobotWebhook 只保留 key 首尾 4 位。
func MaskWeComRobotWebhook(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "***"
	}
	key := u.Query().Get("key")
	masked := "***"
	if len(key) > 8 {
		masked = key[:4] + "****" + key[len(key)-4:]
	}
	return u.Scheme + "://" + u.Host + u.Path + "?key=" + masked
}

func (w *weComRobotSender) Send(ctx context.Context, webhook, content string) error {
	if err := ValidateWeComRobotWebhook(webhook); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": truncateUTF8Bytes(content, weComRobotMaxContentBytes)},
	})
	if err != nil {
		return fmt.Errorf("marshal wecom alert: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(webhook), bytes.NewReader(body))
	if err != nil {
		return errors.New("create wecom alert request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		// url.Error 会带上完整 URL（含 key），只保留底层错误。
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("send wecom alert: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, weComRobotMaxBody))
	if err != nil {
		return fmt.Errorf("read wecom alert response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("wecom alert returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("decode wecom alert response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("wecom alert rejected: code=%d message=%s", result.ErrCode, truncateRunes(result.ErrMsg, 120))
	}
	return nil
}

func truncateUTF8Bytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "\n…（内容过长已截断）"
}

type pendingSecurityAlert struct {
	ID        int64
	Kind      string
	Severity  SecurityAlertSeverity
	Title     string
	Body      string
	CreatedAt time.Time
}

func securitySeverityLabel(sev SecurityAlertSeverity) string {
	switch sev {
	case SecurityAlertCritical:
		return "🔴 严重"
	case SecurityAlertWarning:
		return "🟠 注意"
	default:
		return "🔵 通知"
	}
}

func renderSecurityAlertEvent(ev pendingSecurityAlert) string {
	head := securitySeverityLabel(ev.Severity) + " · " + sanitizeSecurityText(ev.Title, 100)
	if strings.TrimSpace(ev.Body) == "" {
		return head
	}
	return head + "\n" + ev.Body
}

// renderSecurityAlertMessage 把若干条告警拼成一条企业微信纯文本消息。
// 渠道/用户余额类告警（同一组内类别一致）用「运营告警」标题。
func renderSecurityAlertMessage(site string, events []pendingSecurityAlert) string {
	parts := make([]string, 0, len(events)+1)
	if len(events) > 0 && isOpsAlertKind(events[0].Kind) {
		parts = append(parts, fmt.Sprintf("【%s 运营告警】", site))
	} else {
		parts = append(parts, fmt.Sprintf("【%s 安全告警】", site))
	}
	for _, ev := range events {
		parts = append(parts, renderSecurityAlertEvent(ev))
	}
	return strings.Join(parts, "\n\n")
}

// groupSecurityAlerts 按类别（安全/运营）和企业微信单条消息大小上限分组。
func groupSecurityAlerts(site string, events []pendingSecurityAlert) [][]pendingSecurityAlert {
	ordered := make([]pendingSecurityAlert, 0, len(events))
	for _, ops := range []bool{false, true} {
		for _, ev := range events {
			if isOpsAlertKind(ev.Kind) == ops {
				ordered = append(ordered, ev)
			}
		}
	}
	var groups [][]pendingSecurityAlert
	var current []pendingSecurityAlert
	for _, ev := range ordered {
		candidate := append(append([]pendingSecurityAlert{}, current...), ev)
		if len(current) > 0 && (isOpsAlertKind(current[0].Kind) != isOpsAlertKind(ev.Kind) ||
			len(renderSecurityAlertMessage(site, candidate)) > weComRobotMaxContentBytes) {
			groups = append(groups, current)
			current = []pendingSecurityAlert{ev}
			continue
		}
		current = candidate
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

// deliverPending 投递 outbox 中待发送的告警。
func (s *SecurityAlertService) deliverPending(ctx context.Context) {
	s.mu.Lock()
	webhook := s.webhook
	s.mu.Unlock()
	if webhook == "" || s.sender == nil {
		return
	}
	workerID := uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, severity, title, body, created_at
 FROM security_alert_events
 WHERE sent_at IS NULL AND attempts < $1 AND next_attempt_at <= NOW()
   AND (locked_at IS NULL OR locked_at < NOW() - INTERVAL '2 minutes')
 ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`, securityAlertMaxAttempts, securityAlertDeliverBatch)
	if err != nil {
		slog.Warn("[SecurityAlert] failed to load pending alerts", "error", err)
		return
	}
	var list []pendingSecurityAlert
	for rows.Next() {
		var ev pendingSecurityAlert
		var severity string
		if rows.Scan(&ev.ID, &ev.Kind, &severity, &ev.Title, &ev.Body, &ev.CreatedAt) == nil {
			ev.Severity = SecurityAlertSeverity(severity)
			list = append(list, ev)
		}
	}
	_ = rows.Close()
	if len(list) == 0 {
		return
	}
	for _, ev := range list {
		if _, err := tx.ExecContext(ctx, `UPDATE security_alert_events SET locked_at = NOW(), locked_by = $2 WHERE id = $1`, ev.ID, workerID); err != nil {
			return
		}
	}
	if tx.Commit() != nil {
		return
	}

	site := s.siteName(ctx)
	for _, group := range groupSecurityAlerts(site, list) {
		ids := make([]int64, 0, len(group))
		for _, ev := range group {
			ids = append(ids, ev.ID)
		}
		sendErr := s.sender.Send(ctx, webhook, renderSecurityAlertMessage(site, group))
		for _, id := range ids {
			if sendErr == nil {
				_, _ = s.db.ExecContext(ctx, `UPDATE security_alert_events
 SET sent_at = NOW(), attempts = attempts + 1, last_error = '', locked_at = NULL, locked_by = ''
 WHERE id = $1 AND locked_by = $2`, id, workerID)
			} else {
				_, _ = s.db.ExecContext(ctx, `UPDATE security_alert_events
 SET attempts = attempts + 1, last_error = $3,
     next_attempt_at = NOW() + INTERVAL '1 minute' * LEAST(30, POWER(2, attempts)),
     locked_at = NULL, locked_by = ''
 WHERE id = $1 AND locked_by = $2`, id, workerID, truncateRunes(sendErr.Error(), 500))
			}
		}
		if sendErr != nil {
			slog.Warn("[SecurityAlert] failed to deliver alerts", "count", len(ids), "error", sendErr)
		}
	}
}
