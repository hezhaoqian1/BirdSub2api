package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// 渠道监控告警走安全告警的企业微信机器人（运营告警）。每个实例都会跑同一批监控，
// security_alert_state 里的 channel_monitor:<id> 记录保证一轮故障只推一次告警、一次恢复。

const securityAlertKindChannelMonitor = "channel_monitor"

func channelMonitorAlertSubject(monitorID int64) string {
	return "channel_monitor:" + strconv.FormatInt(monitorID, 10)
}

type channelMonitorWeComNotifier struct {
	alerts *SecurityAlertService
}

func newChannelMonitorAlertNotifier(alerts *SecurityAlertService) channelMonitorAlertNotifier {
	if alerts == nil || alerts.db == nil {
		return nil
	}
	return &channelMonitorWeComNotifier{alerts: alerts}
}

// channelMonitorNotifyEnabled 告警总开关和渠道监控开关都打开时才推送。
func (s *SecurityAlertService) channelMonitorNotifyEnabled(ctx context.Context) bool {
	s.refreshConfig(ctx, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Enabled && s.cfg.ChannelMonitorNotify
}

// NotifyFailure 抢到这轮故障的告警记录后写入 outbox；记录已存在说明其他实例推送过。
func (n *channelMonitorWeComNotifier) NotifyFailure(ctx context.Context, alert channelMonitorFailureAlert) (bool, error) {
	s := n.alerts
	if !s.channelMonitorNotifyEnabled(ctx) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, channelMonitorAlertTimeout)
	defer cancel()
	checkedAt := alert.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = s.now()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var subject string
	err = tx.QueryRowContext(ctx, `INSERT INTO security_alert_state(subject, level, value, updated_at)
 VALUES ($1, 1, $2, NOW()) ON CONFLICT (subject) DO NOTHING RETURNING subject`,
		channelMonitorAlertSubject(alert.MonitorID), float64(checkedAt.Unix())).Scan(&subject)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	message := "未返回错误详情"
	if strings.TrimSpace(alert.Message) != "" {
		message = sanitizeSecurityText(alert.Message, channelMonitorAlertMessageRunes)
	}
	if err := emitSecurityAlert(ctx, tx, securityAlertDraft{
		Kind:     securityAlertKindChannelMonitor,
		Severity: SecurityAlertWarning,
		Title:    "渠道监控连续失败",
		Lines: []string{
			"监控：" + channelMonitorLabel(alert.MonitorID, alert.MonitorName),
			"模型：" + sanitizeSecurityText(alert.Model, 100),
			fmt.Sprintf("连续 %d 次%s，最近一次 %s", alert.ConsecutiveFailures,
				channelMonitorStatusLabel(alert.Status), formatSecurityTime(checkedAt, s.loc)),
			"错误：" + message,
		},
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// NotifyRecovery 删掉这轮故障的告警记录并推送恢复；没有记录（未告警或其他实例已推送恢复）时什么都不做。
// 告警关闭时只清记录不推送。
func (n *channelMonitorWeComNotifier) NotifyRecovery(ctx context.Context, alert channelMonitorRecoveryAlert) (bool, error) {
	s := n.alerts
	notify := s.channelMonitorNotifyEnabled(ctx)
	ctx, cancel := context.WithTimeout(ctx, channelMonitorAlertTimeout)
	defer cancel()
	checkedAt := alert.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = s.now()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var alertedUnix float64
	err = tx.QueryRowContext(ctx, `DELETE FROM security_alert_state WHERE subject = $1 RETURNING value`,
		channelMonitorAlertSubject(alert.MonitorID)).Scan(&alertedUnix)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if notify {
		status := channelMonitorStatusLabel(alert.Status)
		if alert.LatencyMs != nil {
			status += fmt.Sprintf("，延迟 %d ms", *alert.LatencyMs)
		}
		lines := []string{
			"监控：" + channelMonitorLabel(alert.MonitorID, alert.MonitorName),
			"模型：" + sanitizeSecurityText(alert.Model, 100),
			"当前状态：" + status + "，检测于 " + formatSecurityTime(checkedAt, s.loc),
		}
		if alertedUnix > 0 {
			alertedAt := time.Unix(int64(alertedUnix), 0)
			lines = append(lines, "告警时间："+formatSecurityTime(alertedAt, s.loc)+
				"，距今约 "+formatAlertDuration(checkedAt.Sub(alertedAt)))
		}
		if err := emitSecurityAlert(ctx, tx, securityAlertDraft{
			Kind:     securityAlertKindChannelMonitor,
			Severity: SecurityAlertInfo,
			Title:    "渠道监控已恢复",
			Lines:    lines,
		}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (n *channelMonitorWeComNotifier) Forget(ctx context.Context, monitorID int64) {
	ctx, cancel := context.WithTimeout(ctx, channelMonitorAlertTimeout)
	defer cancel()
	if _, err := n.alerts.db.ExecContext(ctx, `DELETE FROM security_alert_state WHERE subject = $1`,
		channelMonitorAlertSubject(monitorID)); err != nil {
		slog.Warn("[SecurityAlert] failed to clear channel monitor alert state", "monitor_id", monitorID, "error", err)
	}
}

func channelMonitorLabel(monitorID int64, name string) string {
	return fmt.Sprintf("%s（#%d）", sanitizeSecurityText(name, 100), monitorID)
}

func channelMonitorStatusLabel(status string) string {
	switch status {
	case MonitorStatusOperational:
		return "正常"
	case MonitorStatusDegraded:
		return "降级"
	case MonitorStatusFailed:
		return "检测失败"
	case MonitorStatusError:
		return "请求出错"
	default:
		return sanitizeSecurityText(status, 30)
	}
}

// formatAlertDuration 把时长写成「N 分钟」「H 小时 M 分钟」「D 天 H 小时」。
func formatAlertDuration(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	switch {
	case minutes < 1:
		return "不到 1 分钟"
	case minutes < 60:
		return fmt.Sprintf("%d 分钟", minutes)
	case minutes < 24*60:
		return fmt.Sprintf("%d 小时 %d 分钟", minutes/60, minutes%60)
	default:
		return fmt.Sprintf("%d 天 %d 小时", minutes/(24*60), minutes%(24*60)/60)
	}
}
