package service

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestChannelBalanceRejectPattern(t *testing.T) {
	re := regexp.MustCompile("(?i)" + channelBalanceRejectPattern)
	for _, msg := range []string{
		"insufficient balance",
		"Insufficient account balance",
		"Insufficient user quota, remaining quota: ＄-116.274312",
		"Insufficient user balance. Please top up your user balance.",
		"用户额度不足, 剩余额度: ＄-15.763594",
		"insufficient_quota",
		"Your credit balance is too low to access the Anthropic API.",
	} {
		require.True(t, re.MatchString(msg), msg)
	}
	for _, msg := range []string{
		"Resource has been exhausted (e.g. check quota).",
		"TPM quota exhausted. No spare capacity is currently available",
		"Upstream response stream was interrupted",
	} {
		require.False(t, re.MatchString(msg), msg)
	}
}

func TestCheckChannelBalanceRejects(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 11, 7, 17, 27, 0, 0, time.UTC)
	svc := NewSecurityAlertService(db, nil, nil)
	svc.now = func() time.Time { return now }
	svc.rememberWallets([]channelBalanceWallet{{Members: []channelBalanceReading{{AccountID: 85}, {AccountID: 161}}}}, true)
	snap := securityTestSnapshot(t)

	wallet, _ := json.Marshal(balanceState(26.89, "USD", now.Add(-2*time.Minute)).Snapshot)
	hitCols := []string{"account_id", "name", "base_url", "recent", "visible", "message", "models", "snapshot"}
	mock.ExpectQuery(regexp.QuoteMeta("FROM ops_error_logs e")).
		WithArgs(now.Add(-30*time.Minute), now.Add(-5*time.Minute), channelBalanceRejectPattern).
		WillReturnRows(sqlmock.NewRows(hitCols).
			AddRow(int64(85), "可达鸭 Claude", "https://kedaya.ai", 3, 0, "insufficient balance", "claude-opus-4-7", "").
			AddRow(int64(99), "XApi Claude", "https://xapi-key.net", 2, 2, "insufficient balance", "claude-opus-4-7", "").
			AddRow(int64(161), "可达鸭 DeepSeek", "http://51.222.42.218:17777", 4, 1, "insufficient balance", "deepseek-v4-pro,deepseek-v4-flash", string(wallet)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject LIKE 'reject:%'")).
		WillReturnRows(sqlmock.NewRows([]string{"subject"}).AddRow("reject:155"))
	// #155 30 分钟没再被拒 → 静默重新开始监控。
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).WithArgs("reject:155", 0, 0.0).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// #85 + #161 是同一个钱包（两个域名），合计 7 次 ≥ 5 → 推一条；#99 只有 2 次，不推。
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelReject, "critical", "上游开始因余额不足拒绝请求", bodyContains{
			"上游：51.222.42.218:17777、kedaya.ai",
			"账号：#85 可达鸭 Claude、#161 可达鸭 DeepSeek",
			"近 5 分钟被上游拒绝 7 次（客户看到报错 1 次，其余已切到其他渠道）",
			"上游报错：insufficient balance",
			"上游余额：$26.89（2 分钟前查询）",
			"模型：claude-opus-4-7、deepseek-v4-flash、deepseek-v4-pro",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	for _, subject := range []string{"reject:85", "reject:161"} {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).WithArgs(subject, 1, float64(now.Unix())).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	svc.checkChannelBalanceRejects(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet())

	// 下一分钟同一钱包仍在被拒（#161 已经安静了，但 #85 还在）：不重复推送、不复位；XApi 新被拒的不受影响。
	mock.ExpectQuery(regexp.QuoteMeta("FROM ops_error_logs e")).
		WillReturnRows(sqlmock.NewRows(hitCols).
			AddRow(int64(85), "可达鸭 Claude", "https://kedaya.ai", 9, 0, "insufficient balance", "claude-opus-4-7", ""))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject LIKE 'reject:%'")).
		WillReturnRows(sqlmock.NewRows([]string{"subject"}).AddRow("reject:85").AddRow("reject:161"))
	svc.checkChannelBalanceRejects(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet())

	// 整个钱包 30 分钟没再被拒：两个账号都复位。
	mock.ExpectQuery(regexp.QuoteMeta("FROM ops_error_logs e")).WillReturnRows(sqlmock.NewRows(hitCols))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject LIKE 'reject:%'")).
		WillReturnRows(sqlmock.NewRows([]string{"subject"}).AddRow("reject:85"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).WithArgs("reject:85", 0, 0.0).
		WillReturnResult(sqlmock.NewResult(0, 1))
	svc.checkChannelBalanceRejects(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet())

	snap.cfg.ChannelRejectThreshold = 0
	svc.checkChannelBalanceRejects(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet(), "disabled rule issues no query")
}
