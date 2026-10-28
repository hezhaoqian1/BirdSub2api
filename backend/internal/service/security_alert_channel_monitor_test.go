package service

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func channelMonitorTestFailure(checkedAt time.Time) channelMonitorFailureAlert {
	return channelMonitorFailureAlert{
		MonitorID:           31,
		MonitorName:         "GPT 特价渠道",
		Model:               "gpt-5.6",
		Status:              MonitorStatusFailed,
		Message:             "HTTP 502\nbad gateway",
		CheckedAt:           checkedAt,
		ConsecutiveFailures: 3,
	}
}

func expectChannelMonitorClaim(mock sqlmock.Sqlmock, checkedAt time.Time, won bool) {
	rows := sqlmock.NewRows([]string{"subject"})
	if won {
		rows.AddRow("channel_monitor:31")
	}
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO security_alert_state(subject, level, value, updated_at)")).
		WithArgs("channel_monitor:31", float64(checkedAt.Unix())).
		WillReturnRows(rows)
}

func TestChannelMonitorNotifierAlertsOncePerEpisode(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	notifier := newChannelMonitorAlertNotifier(svc)
	checkedAt := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectChannelMonitorClaim(mock, checkedAt, true)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelMonitor, "warning", "渠道监控连续失败", bodyContains{
			"监控：GPT 特价渠道（#31）",
			"模型：gpt-5.6",
			"连续 3 次检测失败，最近一次 2026-10-29 09:00:00",
			"错误：HTTP 502 bad gateway",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	handled, err := notifier.NotifyFailure(context.Background(), channelMonitorTestFailure(checkedAt))
	require.NoError(t, err)
	require.True(t, handled)

	// 记录已存在：其他实例（或重启前）已推送过这轮故障，不再写告警。
	mock.ExpectBegin()
	expectChannelMonitorClaim(mock, checkedAt, false)
	mock.ExpectRollback()
	handled, err = notifier.NotifyFailure(context.Background(), channelMonitorTestFailure(checkedAt))
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorNotifierSkipsWhenDisabled(t *testing.T) {
	for _, cfg := range []string{`{"enabled":false}`, `{"enabled":true,"channel_monitor_notify":false}`} {
		svc, mock, _ := newUserWatchTestService(t, cfg, "")
		handled, err := newChannelMonitorAlertNotifier(svc).NotifyFailure(context.Background(), channelMonitorTestFailure(time.Now()))
		require.NoError(t, err, cfg)
		require.False(t, handled, cfg)
		require.NoError(t, mock.ExpectationsWereMet(), cfg)
	}
}

func TestChannelMonitorNotifierRecovery(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	notifier := newChannelMonitorAlertNotifier(svc)
	alertedAt := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	latency := 820
	recovery := channelMonitorRecoveryAlert{
		MonitorID: 31, MonitorName: "GPT 特价渠道", Model: "gpt-5.6",
		Status: MonitorStatusOperational, LatencyMs: &latency, CheckedAt: alertedAt.Add(65 * time.Minute),
	}
	deleteState := regexp.QuoteMeta("DELETE FROM security_alert_state WHERE subject = $1 RETURNING value")

	mock.ExpectBegin()
	mock.ExpectQuery(deleteState).WithArgs("channel_monitor:31").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(float64(alertedAt.Unix())))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelMonitor, "info", "渠道监控已恢复", bodyContains{
			"监控：GPT 特价渠道（#31）",
			"当前状态：正常，延迟 820 ms，检测于 2026-10-29 10:05:00",
			"告警时间：2026-10-29 09:00:00，距今约 1 小时 5 分钟",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	handled, err := notifier.NotifyRecovery(context.Background(), recovery)
	require.NoError(t, err)
	require.True(t, handled)

	// 没有未恢复的告警（没告警过，或其他实例已推送恢复）：什么都不发。
	mock.ExpectBegin()
	mock.ExpectQuery(deleteState).WithArgs("channel_monitor:31").WillReturnRows(sqlmock.NewRows([]string{"value"}))
	mock.ExpectRollback()
	handled, err = notifier.NotifyRecovery(context.Background(), recovery)
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorNotifierRecoveryWhenDisabledOnlyClearsState(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, `{"enabled":true,"channel_monitor_notify":false}`, "")
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("DELETE FROM security_alert_state WHERE subject = $1 RETURNING value")).
		WithArgs("channel_monitor:7").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(float64(1)))
	mock.ExpectCommit()
	handled, err := newChannelMonitorAlertNotifier(svc).NotifyRecovery(context.Background(), channelMonitorRecoveryAlert{MonitorID: 7})
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorNotifierForget(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM security_alert_state WHERE subject = $1")).
		WithArgs("channel_monitor:9").WillReturnResult(sqlmock.NewResult(0, 1))
	newChannelMonitorAlertNotifier(svc).Forget(context.Background(), 9)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNewChannelMonitorAlertNotifierWithoutAlertsIsNil(t *testing.T) {
	require.Nil(t, newChannelMonitorAlertNotifier(nil))
	require.Nil(t, newChannelMonitorAlertNotifier(&SecurityAlertService{}))
}

func TestChannelMonitorAlertIsOpsAlert(t *testing.T) {
	require.True(t, isOpsAlertKind(securityAlertKindChannelMonitor))
	msg := renderSecurityAlertMessage("BirdApi", []pendingSecurityAlert{{
		Kind: securityAlertKindChannelMonitor, Severity: SecurityAlertWarning, Title: "渠道监控连续失败", Body: "监控：GPT 特价渠道（#31）",
	}})
	require.True(t, strings.HasPrefix(msg, "【BirdApi 运营告警】"))
	requirePlainText(t, msg)
}

func TestFormatAlertDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second:           "不到 1 分钟",
		12 * time.Minute:           "12 分钟",
		65 * time.Minute:           "1 小时 5 分钟",
		(50*60 + 30) * time.Minute: "2 天 2 小时",
	} {
		require.Equal(t, want, formatAlertDuration(d), d.String())
	}
}
