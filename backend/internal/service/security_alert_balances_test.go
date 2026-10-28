package service

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// bodyContains 匹配 outbox 写入的告警正文。
type bodyContains []string

func (b bodyContains) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, want := range b {
		if !strings.Contains(s, want) {
			return false
		}
	}
	return true
}

func requirePlainText(t *testing.T, text string) {
	t.Helper()
	for _, markup := range []string{"**", "> ", "<font", "###", "]("} {
		require.NotContains(t, text, markup)
	}
}

func TestSecurityAlertConfigLegacyJSONKeepsNewDefaults(t *testing.T) {
	cfg := DefaultSecurityAlertConfig()
	legacy := `{"enabled":true,"trusted_cidrs":["39.144.0.0/16"],"balance_adjust_threshold":500,"daily_digest_hour":9}`
	require.NoError(t, json.Unmarshal([]byte(legacy), &cfg))
	cfg.normalize()
	require.Equal(t, 100.0, cfg.ChannelSpendFloor)
	require.Equal(t, 10, cfg.ChannelBalanceCheckMinutes)
	require.True(t, cfg.ChannelQuotaErrorNotify)
	require.NotNil(t, cfg.UserBalanceWatches)
	require.NoError(t, cfg.Validate())
}

func TestSecurityAlertConfigValidateBalanceRules(t *testing.T) {
	cfg := DefaultSecurityAlertConfig()
	cfg.UserBalanceWatches = []UserBalanceWatch{{UserID: 12, Threshold: 50, Note: "大客户"}}
	require.NoError(t, cfg.Validate())

	for name, mutate := range map[string]func(*SecurityAlertConfig){
		"duplicate user": func(c *SecurityAlertConfig) {
			c.UserBalanceWatches = append(c.UserBalanceWatches, UserBalanceWatch{UserID: 12, Threshold: 5})
		},
		"zero threshold":   func(c *SecurityAlertConfig) { c.UserBalanceWatches[0].Threshold = 0 },
		"bad user id":      func(c *SecurityAlertConfig) { c.UserBalanceWatches[0].UserID = 0 },
		"long note":        func(c *SecurityAlertConfig) { c.UserBalanceWatches[0].Note = strings.Repeat("长", 61) },
		"interval too low": func(c *SecurityAlertConfig) { c.ChannelBalanceCheckMinutes = 2 },
		"negative floor":   func(c *SecurityAlertConfig) { c.ChannelSpendFloor = -1 },
		"runway too long":  func(c *SecurityAlertConfig) { c.ChannelRunwayHours = 200 },
	} {
		c := cfg
		c.UserBalanceWatches = append([]UserBalanceWatch{}, cfg.UserBalanceWatches...)
		mutate(&c)
		require.Error(t, c.Validate(), name)
	}
	off := cfg
	off.ChannelBalanceCheckMinutes = 0
	require.NoError(t, off.Validate(), "0 turns the sweep off")
}

func TestEvaluateChannelBalanceLevel(t *testing.T) {
	cfg := DefaultSecurityAlertConfig() // USD 20, CNY 100, runway 6h
	require.Equal(t, channelBalanceEmpty, evaluateChannelBalanceLevel(cfg, -4.6, "USD", 0))
	require.Equal(t, channelBalanceEmpty, evaluateChannelBalanceLevel(cfg, 0, "CNY", 0))
	require.Equal(t, channelBalanceLow, evaluateChannelBalanceLevel(cfg, 8.63, "USD", 0))
	require.Equal(t, channelBalanceLow, evaluateChannelBalanceLevel(cfg, 42, "CNY", 0))
	require.Equal(t, channelBalanceOK, evaluateChannelBalanceLevel(cfg, 42, "USD", 0))
	require.Equal(t, channelBalanceRunway, evaluateChannelBalanceLevel(cfg, 478, "USD", 300), "1.6h left at $300/h")
	require.Equal(t, channelBalanceOK, evaluateChannelBalanceLevel(cfg, 478, "USD", 50), "9.6h left")
	require.Equal(t, channelBalanceOK, evaluateChannelBalanceLevel(cfg, 3, "QUOTA", 0), "raw quota has no threshold")
	require.Equal(t, channelBalanceRunway, evaluateChannelBalanceLevel(cfg, 3, "QUOTA", 1))
	cfg.ChannelRunwayHours = 0
	require.Equal(t, channelBalanceOK, evaluateChannelBalanceLevel(cfg, 478, "USD", 300), "runway rule off")
}

func TestNextBalanceAlertState(t *testing.T) {
	level, value, alert := nextBalanceAlertState(0, 0, channelBalanceRunway, 478)
	require.True(t, alert)
	require.Equal(t, channelBalanceRunway, level)
	require.Equal(t, 478.0, value)

	level, value, alert = nextBalanceAlertState(level, value, channelBalanceRunway, 300)
	require.False(t, alert, "same level is pushed once")
	level, value, alert = nextBalanceAlertState(level, value, channelBalanceOK, 290)
	require.False(t, alert)
	require.Equal(t, channelBalanceRunway, level, "traffic calming down without a recharge does not re-arm")

	level, value, alert = nextBalanceAlertState(level, value, channelBalanceLow, 15)
	require.True(t, alert, "escalation is pushed")
	level, value, alert = nextBalanceAlertState(level, value, channelBalanceEmpty, -0.5)
	require.True(t, alert)
	require.Equal(t, -0.5, value)

	level, value, alert = nextBalanceAlertState(level, value, channelBalanceEmpty, -0.49)
	require.False(t, alert)
	require.Equal(t, channelBalanceEmpty, level, "rounding noise is not a recharge")

	level, value, alert = nextBalanceAlertState(level, value, channelBalanceOK, 500)
	require.False(t, alert)
	require.Equal(t, channelBalanceOK, level, "recharge re-arms")
	require.Equal(t, 500.0, value)

	_, _, alert = nextBalanceAlertState(level, value, channelBalanceLow, 10)
	require.True(t, alert, "low again after the recharge")
}

func TestBalanceBurnPerHour(t *testing.T) {
	t0 := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	require.Zero(t, balanceBurnPerHour(nil))
	require.Zero(t, balanceBurnPerHour([]balanceSample{{t0, 10, "USD"}, {t0.Add(10 * time.Minute), 5, "USD"}}), "span too short")

	samples := []balanceSample{
		{t0, 100, "USD"},
		{t0.Add(time.Hour), 60, "USD"},
		{t0.Add(70 * time.Minute), 560, "USD"}, // recharge
		{t0.Add(2 * time.Hour), 540, "USD"},
	}
	require.InDelta(t, 30.0, balanceBurnPerHour(samples), 0.001, "(40+20)/2h, recharge ignored")

	mixed := []balanceSample{{t0, 999, "QUOTA"}, {t0.Add(time.Hour), 100, "USD"}, {t0.Add(2 * time.Hour), 90, "USD"}}
	require.InDelta(t, 10.0, balanceBurnPerHour(mixed), 0.001, "only samples in the latest unit count")
}

func TestBuildChannelBalanceDraftsGroupsSharedWallets(t *testing.T) {
	cfg := DefaultSecurityAlertConfig()
	at := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	drafts := buildChannelBalanceDrafts(cfg, []channelBalanceReading{
		{AccountID: 101, Name: "lcodex GPT 官key", Site: "lcodex.example", Remaining: 8.64, Unit: "USD", Scope: "account", Level: channelBalanceLow},
		{AccountID: 98, Name: "lcodex 不降智", Site: "lcodex.example", Remaining: 8.63, Unit: "USD", Scope: "account", Level: channelBalanceLow},
		{AccountID: 120, Name: "lcodex Gemini", Site: "lcodex.example", Remaining: 14.9, Unit: "USD", Scope: "account", Level: channelBalanceLow},
		{AccountID: 77, Name: "Yimo CCMAX", Site: "yimo.example", Remaining: -4.6, Unit: "USD", Scope: "account", Level: channelBalanceEmpty},
		{AccountID: 150, Name: "Yimo 163", Site: "yimo.example", Remaining: 478, Unit: "USD", Scope: "account", Burn: 300, Level: channelBalanceRunway},
	}, at)
	require.Len(t, drafts, 4)

	require.Equal(t, "渠道余额已用完", drafts[0].Title)
	require.Equal(t, SecurityAlertCritical, drafts[0].Severity)
	require.Contains(t, drafts[0].body(), "余额：-$4.60")

	shared := drafts[1].body()
	require.Equal(t, "渠道余额不足", drafts[1].Title)
	require.Contains(t, shared, "上游：lcodex.example")
	require.Contains(t, shared, "#98 lcodex 不降智、#101 lcodex GPT 官key")
	require.Contains(t, shared, "余额：$8.63 ~ $8.64")
	require.Contains(t, shared, "提醒线：$20.00")
	require.NotContains(t, shared, "#120", "a different wallet on the same site is reported separately")
	require.Contains(t, drafts[2].body(), "#120 lcodex Gemini")

	runway := drafts[3].body()
	require.Equal(t, "渠道余额预计很快用完", drafts[3].Title)
	require.Contains(t, runway, "平均每小时扣 $300.00，预计 1.6 小时后用完")

	for _, d := range drafts {
		require.Equal(t, securityAlertKindChannelBal, d.Kind)
		requirePlainText(t, d.body())
	}
}

func TestGroupSecurityAlertsSeparatesOpsAlerts(t *testing.T) {
	events := []pendingSecurityAlert{
		{ID: 1, Kind: securityAlertKindChannelBal, Severity: SecurityAlertWarning, Title: "渠道余额不足"},
		{ID: 2, Kind: "admin_new_network", Severity: SecurityAlertCritical, Title: "陌生网络正在访问管理后台"},
		{ID: 3, Kind: securityAlertKindUserBalance, Severity: SecurityAlertInfo, Title: "关注用户余额低于提醒线"},
	}
	groups := groupSecurityAlerts("BirdApi", events)
	require.Len(t, groups, 2)
	require.Equal(t, int64(2), groups[0][0].ID)
	require.True(t, strings.HasPrefix(renderSecurityAlertMessage("BirdApi", groups[0]), "【BirdApi 安全告警】"))
	require.Len(t, groups[1], 2)
	require.True(t, strings.HasPrefix(renderSecurityAlertMessage("BirdApi", groups[1]), "【BirdApi 运营告警】"))
}

type fakeChannelBalances map[int64]UpstreamBalanceState

func (f fakeChannelBalances) State(_ context.Context, id int64, refresh, _ bool) (UpstreamBalanceState, error) {
	if !refresh {
		return UpstreamBalanceState{}, errors.New("sweep must refresh")
	}
	st, ok := f[id]
	if !ok {
		return UpstreamBalanceState{}, errors.New("余额接口返回 HTTP 404")
	}
	return st, nil
}

func balanceState(remaining float64, unit string, fetched time.Time) UpstreamBalanceState {
	return UpstreamBalanceState{Snapshot: UpstreamBalanceSnapshot{
		Data:      &UpstreamBalanceData{Scope: "account", Remaining: &remaining, Unit: unit, Source: "sub2api"},
		FetchedAt: fetched.Unix(),
	}}
}

func TestRunBalanceSweepAlertsOncePerLevel(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	now := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	svc := NewSecurityAlertService(db, nil, nil)
	svc.now = func() time.Time { return now }
	svc.balances = fakeChannelBalances{
		98:  balanceState(8.63, "USD", now),
		101: balanceState(8.63, "USD", now),
		50:  balanceState(714.9, "USD", now),
		77:  balanceState(-4.6, "USD", now),
		33:  balanceState(544, "USD", now.Add(-2*time.Hour)), // stale cached snapshot
	}

	failedRecently, _ := json.Marshal(UpstreamBalanceSnapshot{Error: "HTTP 404", AttemptAt: now.Add(-time.Hour).Unix()})
	targetCols := []string{"id", "name", "platform", "base_url", "mode", "snapshot"}
	mock.ExpectQuery(regexp.QuoteMeta("FROM accounts a LEFT JOIN upstream_balance_accounts b")).
		WillReturnRows(sqlmock.NewRows(targetCols).
			AddRow(int64(33), "多多 GPT", "openai", "https://duo.example/v1", "auto", "{}").
			AddRow(int64(50), "Sixoner GPT 企业", "openai", "https://sixoner.example", "auto", "{}").
			AddRow(int64(60), "SISUO Kiro", "anthropic", "https://sisuo.example", "", "").
			AddRow(int64(69), "BMCCA", "openai", "https://bmcca.example", "auto", string(failedRecently)).
			AddRow(int64(70), "关掉余额", "openai", "https://off.example", "disabled", "{}").
			AddRow(int64(77), "Yimo CCMAX", "anthropic", "https://yimo.example/", "auto", "{}").
			AddRow(int64(98), "lcodex 不降智", "openai", "https://lcodex.example/v1", "auto", "{}").
			AddRow(int64(101), "lcodex GPT 官key", "openai", "https://lcodex.example/v1", "auto", "{}"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_balance_samples")).
		WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_balance_samples")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "observed_at", "remaining", "unit"}).
			AddRow(int64(50), now.Add(-2*time.Hour), 1114.9, "USD").
			AddRow(int64(50), now.Add(-time.Hour), 914.9, "USD").
			AddRow(int64(50), now, 714.9, "USD"))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject = ANY")).
		WillReturnRows(sqlmock.NewRows([]string{"subject", "level", "value"}).
			AddRow("account:101", channelBalanceLow, 8.70))
	expectAlert := func(title, severity string, body ...string) {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
			WithArgs(sqlmock.AnyArg(), securityAlertKindChannelBal, severity, title, bodyContains(body)).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}
	expectAlert("渠道余额已用完", "critical", "#77 Yimo CCMAX", "余额：-$4.60")
	expectAlert("渠道余额不足", "warning", "上游：lcodex.example", "账号：#98 lcodex 不降智\n")
	expectAlert("渠道余额预计很快用完", "warning", "#50 Sixoner GPT 企业", "平均每小时扣 $200.00，预计 3.6 小时后用完")
	for _, st := range []struct {
		subject string
		level   int
		value   float64
	}{{"account:50", channelBalanceRunway, 714.9}, {"account:77", channelBalanceEmpty, -4.6}, {"account:98", channelBalanceLow, 8.63}} {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).
			WithArgs(st.subject, st.level, st.value).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	svc.runBalanceSweep(DefaultSecurityAlertConfig())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckUserBalanceWatches(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	now := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	svc := NewSecurityAlertService(db, nil, nil)
	svc.now = func() time.Time { return now }
	snap := securityTestSnapshot(t)
	snap.cfg.UserBalanceWatches = []UserBalanceWatch{{UserID: 12, Threshold: 50, Note: "大客户"}, {UserID: 13, Threshold: 10}, {UserID: 14, Threshold: 10}}
	userCols := []string{"id", "email", "username", "balance"}
	stateCols := []string{"subject", "level", "value"}

	// 第一次：用户 12 低于提醒线 → 推送并记状态；13 正常；14 不存在。
	mock.ExpectQuery(regexp.QuoteMeta("FROM users WHERE id = ANY")).
		WillReturnRows(sqlmock.NewRows(userCols).AddRow(int64(12), "big@example.com", "", 8.2).AddRow(int64(13), "ok@example.com", "", 100.0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state")).WillReturnRows(sqlmock.NewRows(stateCols))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(SUM(actual_cost), 0) FROM usage_logs WHERE user_id")).
		WithArgs(int64(12), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"s"}).AddRow(24.0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindUserBalance, "info", "关注用户余额低于提醒线",
			bodyContains{"用户：#12 big@example.com（大客户）", "当前余额 $8.20，提醒线 $50.00", "近 24 小时消费 $24.00，按此速度约 8.2 小时后用完"}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).WithArgs("user:12", 1, 8.2).
		WillReturnResult(sqlmock.NewResult(0, 1))
	svc.checkUserBalanceWatches(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet())

	// 第二次：继续消费，不重复推送。
	mock.ExpectQuery(regexp.QuoteMeta("FROM users WHERE id = ANY")).
		WillReturnRows(sqlmock.NewRows(userCols).AddRow(int64(12), "big@example.com", "", 7.0).AddRow(int64(13), "ok@example.com", "", 100.0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state")).
		WillReturnRows(sqlmock.NewRows(stateCols).AddRow("user:12", 1, 8.2))
	svc.checkUserBalanceWatches(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet())

	// 第三次：充值回到提醒线以上 → 静默重新开始监控。
	mock.ExpectQuery(regexp.QuoteMeta("FROM users WHERE id = ANY")).
		WillReturnRows(sqlmock.NewRows(userCols).AddRow(int64(12), "big@example.com", "", 60.0).AddRow(int64(13), "ok@example.com", "", 100.0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state")).
		WillReturnRows(sqlmock.NewRows(stateCols).AddRow("user:12", 1, 8.2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).WithArgs("user:12", 0, 60.0).
		WillReturnResult(sqlmock.NewResult(0, 1))
	svc.checkUserBalanceWatches(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckChannelSpend(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	now := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	svc := NewSecurityAlertService(db, nil, nil)
	svc.now = func() time.Time { return now }
	svc.usageBaselineAt = now
	svc.channelSpendBaseline = map[int64]float64{77: 300, 99: 50}
	snap := securityTestSnapshot(t)

	mock.ExpectQuery(regexp.QuoteMeta("HAVING SUM(")).WithArgs(sqlmock.AnyArg(), 100.0).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "name", "platform", "cost", "revenue", "requests"}).
			AddRow(int64(77), "Yimo CCMAX", "anthropic", 500.0, 600.0, 1000). // below 3x P95
			AddRow(int64(99), "XApi Claude", "anthropic", 200.0, 240.0, 300))
	mock.ExpectQuery(regexp.QuoteMeta("GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 3")).WithArgs(int64(99), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "email", "cost"}).AddRow(int64(12), "big@example.com", 180.0))
	walletSnap, _ := json.Marshal(balanceState(452.15, "USD", now.Add(-12*time.Minute)).Snapshot)
	mock.ExpectQuery(regexp.QuoteMeta("FROM upstream_balance_accounts WHERE account_id")).WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"snapshot"}).AddRow(string(walletSnap)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelSpend, "warning", "渠道大额消费", bodyContains{
			"渠道：#99 XApi Claude（anthropic）",
			"近 1 小时成本 $200.00（300 次请求，用户扣费 $240.00）",
			"单小时 P95 $50.00（本小时是它的 4.0 倍）",
			"主要用户：#12 big@example.com $180.00",
			"上游余额 $452.15（12 分钟前查询）",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))

	svc.checkChannelSpend(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckChannelQuotaErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	svc := NewSecurityAlertService(db, nil, nil)
	snap := securityTestSnapshot(t)
	mock.ExpectQuery(regexp.QuoteMeta("status = 'error' AND updated_at > $1 AND error_message ~* $2")).
		WithArgs(sqlmock.AnyArg(), channelQuotaErrorPattern).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "platform", "error_message"}).
			AddRow(int64(21), "zzone kiro", "anthropic", "Access forbidden (403): 用户额度不足, 剩余额度: ＄0.353878"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelQuota, "critical", "渠道因额度不足已停用",
			bodyContains{"渠道：#21 zzone kiro（anthropic）", "上游报错：Access forbidden (403): 用户额度不足"}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	svc.checkChannelQuotaErrors(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet())

	snap.cfg.ChannelQuotaErrorNotify = false
	svc.checkChannelQuotaErrors(context.Background(), snap)
	require.NoError(t, mock.ExpectationsWereMet(), "disabled rule issues no query")
}
