package service

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
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
	require.True(t, cfg.ChannelMonitorNotify)
	require.Equal(t, 60, cfg.ChannelUrgentMinutes)
	require.Equal(t, 5, cfg.ChannelRejectThreshold)
	require.True(t, cfg.ChannelRechargeNotify)
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
		"urgent too long":  func(c *SecurityAlertConfig) { c.ChannelUrgentMinutes = 1441 },
		"negative reject":  func(c *SecurityAlertConfig) { c.ChannelRejectThreshold = -1 },
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
	cfg := DefaultSecurityAlertConfig() // USD 20, CNY 100, runway 6h, urgent 60 min
	require.Equal(t, channelBalanceEmpty, evaluateChannelBalanceLevel(cfg, -4.6, "USD", 0))
	require.Equal(t, channelBalanceEmpty, evaluateChannelBalanceLevel(cfg, 0, "CNY", 0))
	require.Equal(t, channelBalanceEmpty, evaluateChannelBalanceLevel(cfg, 8.63, "USD", 0), "below the line counts as hitting bottom")
	require.Equal(t, channelBalanceEmpty, evaluateChannelBalanceLevel(cfg, 42, "CNY", 0))
	require.Equal(t, channelBalanceEmpty, evaluateChannelBalanceLevel(cfg, 19.77, "USD", 2), "slow channel below the line: 9.8h left")
	require.Equal(t, channelBalanceOK, evaluateChannelBalanceLevel(cfg, 42, "USD", 0))
	require.Equal(t, channelBalanceRunway, evaluateChannelBalanceLevel(cfg, 478, "USD", 300), "1.6h left at $300/h")
	require.Equal(t, channelBalanceOK, evaluateChannelBalanceLevel(cfg, 478, "USD", 50), "9.6h left")
	require.Equal(t, channelBalanceUrgent, evaluateChannelBalanceLevel(cfg, 140, "USD", 330), "25 min left")
	require.Equal(t, channelBalanceEmpty, evaluateChannelBalanceLevel(cfg, 15, "USD", 166), "below the line outranks urgent")
	require.Equal(t, channelBalanceEmpty, normalizeChannelBalanceLevel(channelBalanceLow), "old 低于提醒线 state reads as 见底")
	require.Equal(t, channelBalanceOK, evaluateChannelBalanceLevel(cfg, 3, "QUOTA", 0), "raw quota has no threshold")
	require.Equal(t, channelBalanceRunway, evaluateChannelBalanceLevel(cfg, 3, "QUOTA", 1))
	cfg.ChannelRunwayHours = 0
	require.Equal(t, channelBalanceOK, evaluateChannelBalanceLevel(cfg, 478, "USD", 300), "runway rule off")
	cfg.ChannelUrgentMinutes = 0
	require.Equal(t, channelBalanceOK, evaluateChannelBalanceLevel(cfg, 140, "USD", 330), "urgent rule off")
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

	level, value, alert = nextBalanceAlertState(level, value, channelBalanceUrgent, 120)
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

	// 2026-11-08 XApi：闲了 2 小时，之后每 10 分钟扣 $55。3 小时平均被摊薄，近 30 分钟是 $330/h。
	var burst []balanceSample
	for i := 0; i <= 18; i++ {
		v := 326.96
		if i > 12 {
			v = 326.96 - float64(i-12)*55
		}
		burst = append(burst, balanceSample{t0.Add(time.Duration(i) * 10 * time.Minute), v, "USD"})
	}
	b := estimateBalanceBurn(burst)
	require.Equal(t, 30*time.Minute, b.Window)
	require.InDelta(t, 330.0, b.PerHour, 0.001)
	require.Equal(t, "按近 30 分钟扣费速度每小时约 $330.00，预计 1 分钟后用完", balanceBurnLine(b, 0.5, "USD"))
	require.Equal(t, "", balanceBurnLine(balanceBurn{PerHour: 0.0028, Window: time.Hour}, 91.29, "CNY"), "no 32937-hour forecasts")
	require.Equal(t, "$0.00", formatBalanceAmount(-0.004, "USD"), "no -$0.00")
}

func reading(id int64, site string, remaining float64, at time.Time, burn float64) channelBalanceReading {
	return channelBalanceReading{AccountID: id, Name: fmt.Sprintf("acct%d", id), Site: site, Remaining: remaining,
		Unit: "USD", Scope: "account", FetchedAt: at, Burn: balanceBurn{PerHour: burn, Window: time.Hour}, Active: true}
}

func TestGroupChannelWallets(t *testing.T) {
	at := time.Date(2026, 11, 7, 17, 35, 0, 0, time.UTC)
	wallets := groupChannelWallets([]channelBalanceReading{
		// Kedaya：两个域名同一个钱包，扣得快，查询时间差 40 秒余额差 $0.77。
		reading(85, "kedaya.ai", 4.66, at.Add(40*time.Second), 166),
		reading(161, "51.222.42.218:17777", 3.99, at, 166),
		reading(172, "51.222.42.218:17777", 3.89, at.Add(-10*time.Second), 166),
		// 同一钱包的两个域名：余额完全一致。
		reading(29, "duoduoai.co", 420.87, at, 0),
		reading(70, "quiteai.autos", 420.87, at, 0),
		// 不同上游碰巧余额接近、且都不怎么扣费：不能合并。
		reading(53, "mfttai.cc", 420.50, at, 0),
		// 同一域名下 1% 以内视为同一钱包，超出则是另一个钱包。
		reading(101, "lcodex.example", 8.64, at, 0),
		reading(98, "lcodex.example", 8.63, at, 0),
		reading(120, "lcodex.example", 14.9, at, 0),
		// 两个不同上游都见底：不跨域名合并。
		reading(77, "yimo.example", -0.3, at, 0),
		reading(99, "xapi.example", -0.3, at, 0),
	})
	byHead := map[int64]channelBalanceWallet{}
	for _, w := range wallets {
		byHead[w.walletHeadID()] = w
	}
	ids := func(w channelBalanceWallet) []int64 {
		var out []int64
		for _, m := range w.Members {
			out = append(out, m.AccountID)
		}
		return out
	}
	require.Len(t, wallets, 7)
	require.Equal(t, []int64{85, 161, 172}, ids(byHead[85]))
	require.Equal(t, []string{"51.222.42.218:17777", "kedaya.ai"}, byHead[85].Sites)
	require.InDelta(t, 3.89, byHead[85].Low, 1e-9)
	require.Equal(t, []int64{29, 70}, ids(byHead[29]))
	require.Equal(t, []int64{53}, ids(byHead[53]))
	require.Equal(t, []int64{98, 101}, ids(byHead[98]))
	require.Equal(t, []int64{120}, ids(byHead[120]))
	require.Equal(t, []int64{77}, ids(byHead[77]))
	require.Equal(t, []int64{99}, ids(byHead[99]))
}

func TestBuildChannelBalanceDrafts(t *testing.T) {
	cfg := DefaultSecurityAlertConfig()
	at := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	wallet := groupChannelWallets([]channelBalanceReading{
		reading(101, "lcodex.example", 8.64, at, 0),
		reading(98, "lcodex.example", 8.63, at, 0),
	})[0]
	low := buildChannelBalanceDraft(cfg, wallet, channelBalanceEmpty, at)
	require.Equal(t, "渠道余额见底", low.Title)
	require.Equal(t, SecurityAlertCritical, low.Severity)
	body := low.body()
	require.Contains(t, body, "上游：lcodex.example")
	require.Contains(t, body, "#98 acct98、#101 acct101")
	require.Contains(t, body, "余额：$8.63 ~ $8.64")
	require.Contains(t, body, "余额低于提醒线 $20.00")
	require.Contains(t, body, "之后不再重复提醒，充值后会通知。")

	fast := groupChannelWallets([]channelBalanceReading{reading(99, "xapi.example", 140.52, at, 330)})[0]
	fast.Burn.Window = 30 * time.Minute
	fast.Drained, fast.DrainedSpan = 360, 24*time.Hour
	urgent := buildChannelBalanceDraft(cfg, fast, channelBalanceUrgent, at)
	require.Equal(t, "渠道余额即将用完", urgent.Title)
	require.Equal(t, SecurityAlertCritical, urgent.Severity)
	require.Contains(t, urgent.body(), "按近 30 分钟扣费速度每小时约 $330.00，预计 26 分钟后用完")
	require.Contains(t, urgent.body(), "近 24 小时共扣 $360.00，按这个用量约够 9.4 小时")
	require.Contains(t, urgent.body(), "预计 60 分钟内用完")
	require.Contains(t, urgent.body(), "又用掉两成以上、并且仍在快速扣费才会再提醒")

	runway := buildChannelBalanceDraft(cfg, fast, channelBalanceRunway, at)
	require.Equal(t, "渠道余额预计很快用完", runway.Title)
	require.Contains(t, runway.body(), "预计 6.0 小时内用完")

	empty := buildChannelBalanceDraft(cfg, groupChannelWallets([]channelBalanceReading{reading(77, "yimo.example", -4.6, at, 0)})[0], channelBalanceEmpty, at)
	require.Equal(t, "渠道余额已用完", empty.Title)
	require.Equal(t, SecurityAlertCritical, empty.Severity)
	require.Contains(t, empty.body(), "余额：-$4.60")

	// 告急后的再提醒：带上次提醒的时间和余额、近 30 分钟速度和一天的用量。
	loc := time.FixedZone("CST", 8*3600)
	yimo := groupChannelWallets([]channelBalanceReading{reading(150, "api.ymocode.com", 127.89, at, 320)})[0]
	yimo.Pace30, yimo.Drained, yimo.DrainedSpan = 160, 855, 24*time.Hour
	remindAt := time.Date(2026, 11, 8, 13, 53, 0, 0, loc)
	remind := buildChannelBalanceRemindDraft(yimo, balanceAlertState{Level: channelBalanceUrgent, Value: 213.33, At: time.Date(2026, 11, 8, 5, 21, 0, 0, time.UTC)}, remindAt, loc)
	require.Equal(t, "渠道余额即将用完（再次提醒）", remind.Title)
	require.Equal(t, SecurityAlertCritical, remind.Severity)
	require.Contains(t, remind.body(), "余额：$127.89（13:21 提醒时 $213.33，还没充值）")
	require.Contains(t, remind.body(), "按近 30 分钟扣费速度每小时约 $160.00，预计 48 分钟后用完")
	require.Contains(t, remind.body(), "近 24 小时共扣 $855.00，按这个用量约够 3.6 小时")
	nextDay := buildChannelBalanceRemindDraft(yimo, balanceAlertState{Value: 213.33, At: time.Date(2026, 11, 8, 5, 21, 0, 0, time.UTC)}, remindAt.Add(20*time.Hour), loc)
	require.Contains(t, nextDay.body(), "（11-08 13:21 提醒时 $213.33，还没充值）")

	// 充值通知不再写「按近 30 分钟速度预计几天后用完」，改写一天的用量。
	prev := 87.65
	recharged := groupChannelWallets([]channelBalanceReading{reading(150, "api.ymocode.com", 585.40, at, 5.52)})[0]
	recharged.Prev, recharged.Drained, recharged.DrainedSpan = &prev, 1049.12, 24*time.Hour
	recharge := buildChannelRechargeDraft(cfg, recharged, channelBalanceOK, at)
	require.Contains(t, recharge.body(), "余额：$87.65 → $585.40")
	require.Contains(t, recharge.body(), "近 24 小时共扣 $1049.12，按这个用量约够 13.4 小时")
	require.NotContains(t, recharge.body(), "扣费速度")

	for _, d := range []securityAlertDraft{low, urgent, runway, empty, remind, recharge} {
		requirePlainText(t, d.body())
	}
	for _, d := range []securityAlertDraft{low, urgent, runway, empty, remind} {
		require.Equal(t, securityAlertKindChannelBal, d.Kind)
	}
}

func TestBalanceDayLine(t *testing.T) {
	require.Equal(t, "近 24 小时共扣 $1049.12，按这个用量约够 13.4 小时", balanceDayLine(1049.12, 24*time.Hour, 585.40, "USD"))
	require.Equal(t, "近 5 小时共扣 ¥50.00，按这个用量约够 9.7 小时", balanceDayLine(50, 5*time.Hour+20*time.Minute, 91.29, "CNY"))
	require.Equal(t, "近 24 小时共扣 $10.00", balanceDayLine(10, 24*time.Hour, -1, "USD"), "no runway once empty")
	require.Equal(t, "", balanceDayLine(0.001, 24*time.Hour, 50, "USD"))
	require.Equal(t, "", balanceDayLine(10, 30*time.Minute, 50, "USD"), "too little history")
}

// yimoWallet 2026-11-08 Yimo（api.ymocode.com）钱包在某一时刻的读数。
func yimoWallet(low, burn, pace30 float64, prev *float64) channelBalanceWallet {
	w := groupChannelWallets([]channelBalanceReading{reading(150, "api.ymocode.com", low, time.Time{}, burn)})[0]
	w.Pace30, w.Prev = pace30, prev
	return w
}

func f64(v float64) *float64 { return &v }

// TestDecideChannelBalanceYimoDay 按 2026-11-08 Yimo 的真实走势：告急后流量停了一夜不提醒，早上流量
// 回来再提醒，见底推一次后不再重复，充值后通知。
func TestDecideChannelBalanceYimoDay(t *testing.T) {
	cfg := DefaultSecurityAlertConfig()
	t0 := time.Date(2026, 11, 8, 5, 21, 0, 0, time.UTC)
	var st balanceAlertState
	step := func(at time.Time, w channelBalanceWallet) channelBalanceAction {
		s := decideChannelBalance(cfg, st, w, false, at)
		st = s.State
		return s.Action
	}

	require.Equal(t, channelBalancePush, step(t0, yimoWallet(213.33, 320, 320, f64(297.51))), "40 min left → 告急")
	require.Equal(t, balanceAlertState{Level: channelBalanceUrgent, Value: 213.33, At: t0}, st)
	require.Equal(t, channelBalanceNoop, step(t0.Add(10*time.Minute), yimoWallet(160, 320, 320, f64(180))), "less than 30 min since the push")
	require.Equal(t, channelBalanceRemind, step(t0.Add(32*time.Minute), yimoWallet(127.89, 320, 160, f64(140))), "40% spent, still draining fast")
	require.Equal(t, 127.89, st.Value)
	require.Equal(t, channelBalanceNoop, step(t0.Add(70*time.Minute), yimoWallet(109.74, 60, 30, f64(110))), "only 14% spent since the reminder")
	require.Equal(t, channelBalanceNoop, step(t0.Add(15*time.Hour), yimoWallet(92.37, 3, 1, f64(92.47))), "night trickle: 28% spent but 92h at the current pace")
	require.Equal(t, channelBalanceRemind, step(t0.Add(16*time.Hour), yimoWallet(70, 300, 300, f64(90))), "traffic is back in the morning")
	require.Equal(t, channelBalancePush, step(t0.Add(16*time.Hour+10*time.Minute), yimoWallet(15, 300, 300, f64(40))), "below $20 → 见底")
	require.Equal(t, channelBalanceEmpty, st.Level)
	require.Equal(t, channelBalanceNoop, step(t0.Add(17*time.Hour), yimoWallet(0.5, 1, 1, f64(0.6))), "见底 is not repeated")
	require.Equal(t, channelBalanceNoop, step(t0.Add(18*time.Hour), yimoWallet(-0.01, 1, 1, f64(0.5))), "nor is 已用完 after 见底")
	require.Equal(t, channelBalanceRecharge, step(t0.Add(19*time.Hour), yimoWallet(585.40, 5.52, 5.52, f64(-0.01))))
	require.Equal(t, balanceAlertState{Level: channelBalanceOK, Value: 585.40, At: t0.Add(19 * time.Hour)}, st)
}

func TestDecideChannelBalanceEdgeCases(t *testing.T) {
	cfg := DefaultSecurityAlertConfig()
	now := time.Date(2026, 11, 9, 4, 33, 0, 0, time.UTC)
	urgent := balanceAlertState{Level: channelBalanceUrgent, Value: 213.33, At: now.Add(-15 * time.Hour)}

	// 充了一部分、仍低于推送时的余额：也认作充值；仍然告急时写在充值通知里，不另推。
	s := decideChannelBalance(cfg, urgent, yimoWallet(187.65, 300, 300, f64(87.65)), false, now)
	require.Equal(t, channelBalanceRecharge, s.Action)
	require.Equal(t, balanceAlertState{Level: channelBalanceUrgent, Value: 187.65, At: now}, s.State)

	// 同一账号读数的小抖动不是充值。
	s = decideChannelBalance(cfg, urgent, yimoWallet(187.0, 0, 0, f64(185.64)), false, now)
	require.Equal(t, channelBalanceNoop, s.Action)
	require.Equal(t, urgent, s.State)

	// 采样断了没有上一次读数：和推送时的余额比。
	s = decideChannelBalance(cfg, urgent, yimoWallet(500, 0, 0, nil), false, now)
	require.Equal(t, channelBalanceRecharge, s.Action)

	// 没告警过的钱包充值：不通知；充完仍很快会用完则按级别推送。
	s = decideChannelBalance(cfg, balanceAlertState{}, yimoWallet(150, 300, 300, f64(10)), false, now)
	require.Equal(t, channelBalancePush, s.Action)
	require.Equal(t, channelBalanceUrgent, s.State.Level)

	// 近 24 小时没流量：只记状态。
	idle := yimoWallet(15, 0, 0, f64(15))
	idle.Active = false
	s = decideChannelBalance(cfg, balanceAlertState{}, idle, false, now)
	require.Equal(t, channelBalanceNoop, s.Action)
	require.Equal(t, channelBalanceEmpty, s.State.Level)

	// 已推过「上游开始拒绝」：不再推更低级别，也不再提醒；见底照常推。
	s = decideChannelBalance(cfg, balanceAlertState{Level: channelBalanceRunway, Value: 194.74}, yimoWallet(81.36, 330, 330, f64(90)), true, now)
	require.Equal(t, channelBalanceNoop, s.Action)
	require.Equal(t, channelBalanceUrgent, s.State.Level)
	s = decideChannelBalance(cfg, urgent, yimoWallet(80, 300, 300, f64(90)), true, now)
	require.Equal(t, channelBalanceNoop, s.Action, "no reminder while the upstream is rejecting")
	s = decideChannelBalance(cfg, urgent, yimoWallet(12, 300, 300, f64(20)), true, now)
	require.Equal(t, channelBalancePush, s.Action)

	// 升级前的「低于提醒线」旧状态读成见底：nat6 ¥91 不会再推一次。
	nat6 := groupChannelWallets([]channelBalanceReading{{AccountID: 149, Remaining: 91.29, Unit: "CNY", Scope: "account", Active: true, Prev: f64(91.29)}})[0]
	s = decideChannelBalance(cfg, balanceAlertState{Level: channelBalanceLow, Value: 91.29}, nat6, false, now)
	require.Equal(t, channelBalanceNoop, s.Action)
	require.Equal(t, channelBalanceEmpty, s.State.Level)

	// 偏低不重复提醒。
	runway := balanceAlertState{Level: channelBalanceRunway, Value: 726, At: now.Add(-2 * time.Hour)}
	s = decideChannelBalance(cfg, runway, yimoWallet(441.87, 110, 110, f64(460)), false, now)
	require.Equal(t, channelBalanceNoop, s.Action)
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

func TestRunBalanceSweepAlertsOncePerWallet(t *testing.T) {
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
		88:  balanceState(5, "USD", now),
		149: balanceState(6252750, "QUOTA", now),             // 换算参数偶发读取失败时的原始额度
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
			AddRow(int64(88), "闲置渠道", "openai", "https://idle.example", "auto", "{}").
			AddRow(int64(98), "lcodex 不降智", "openai", "https://lcodex.example/v1", "auto", "{}").
			AddRow(int64(101), "lcodex GPT 官key", "openai", "https://lcodex.example/v1", "auto", "{}").
			AddRow(int64(149), "nat6 Kimi", "kimi", "https://pro.nat6.example", "new_api", "{}"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_balance_samples")).
		WillReturnResult(sqlmock.NewResult(0, 6))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_balance_samples")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "observed_at", "remaining", "unit"}).
			AddRow(int64(50), now.Add(-2*time.Hour), 1114.9, "USD").
			AddRow(int64(50), now.Add(-time.Hour), 914.9, "USD").
			AddRow(int64(50), now, 714.9, "USD").
			AddRow(int64(149), now.Add(-10*time.Minute), 91.29, "CNY").
			AddRow(int64(149), now, 6252750.0, "QUOTA"))
	mock.ExpectQuery(regexp.QuoteMeta("SUM(GREATEST(prev - remaining, 0))")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "unit", "drained", "first"}).
			AddRow(int64(50), "USD", 400.0, now.Add(-2*time.Hour)).
			AddRow(int64(149), "CNY", 0.0, now.Add(-10*time.Minute)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM unnest($1::bigint[]) AS a")).
		WillReturnRows(sqlmock.NewRows([]string{"a"}).AddRow(int64(50)).AddRow(int64(77)).AddRow(int64(98)).AddRow(int64(101)).AddRow(int64(149)))
	expectEvalLock(mock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject = ANY")).
		WillReturnRows(sqlmock.NewRows(channelStateCols).
			AddRow("account:101", channelBalanceLow, 8.70, now.Add(-24*time.Hour)).
			AddRow("account:149", channelBalanceLow, 91.29, now.Add(-48*time.Hour)))
	expectAlert := func(title, severity string, body ...string) {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
			WithArgs(sqlmock.AnyArg(), securityAlertKindChannelBal, severity, title, bodyContains(body)).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}
	// #98/#101 同一钱包，#101 推过旧版「余额不足」（读成见底）→ 不再推；#88 近 24 小时没有流量 → 只记状态；
	// #149 本轮读到的是原始额度（币种变了）→ 不判定、不当作充值。
	expectAlert("渠道余额已用完", "critical", "#77 Yimo CCMAX", "余额：-$4.60", "之后不再重复提醒")
	expectAlert("渠道余额预计很快用完", "warning", "#50 Sixoner GPT 企业", "按近 1 小时扣费速度每小时约 $200.00，预计 3.6 小时后用完",
		"近 2 小时共扣 $400.00，按这个用量约够 3.6 小时")
	for _, st := range []struct {
		subject string
		level   int
		value   float64
	}{{"account:77", channelBalanceEmpty, -4.6}, {"account:88", channelBalanceEmpty, 5}, {"account:98", channelBalanceEmpty, 8.70}, {"account:50", channelBalanceRunway, 714.9}} {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).
			WithArgs(st.subject, st.level, st.value).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	svc.runBalanceSweep(DefaultSecurityAlertConfig(), nil)
	require.NoError(t, mock.ExpectationsWereMet())
	head, ok := svc.walletOfAccount(101)
	require.True(t, ok)
	require.Equal(t, int64(98), head)
}

func TestEvaluateChannelBalancesRechargeAndRejectSuppression(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 11, 7, 17, 46, 0, 0, time.UTC)
	svc := NewSecurityAlertService(db, nil, nil)
	svc.now = func() time.Time { return now }
	cfg := DefaultSecurityAlertConfig()

	prev := 3.99
	kedaya := []channelBalanceReading{
		reading(85, "kedaya.ai", 3343.52, now.Add(20*time.Second), 160),
		reading(161, "51.222.42.218:17777", 3343.11, now, 160),
	}
	kedaya[1].Prev = &prev
	// XApi 正在被上游拒绝，余额掉到 1 小时内：不再推「即将用完」，但记状态。
	xapi := reading(99, "xapi.example", 81.36, now, 330)
	expectEvalLock(mock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject = ANY")).
		WillReturnRows(sqlmock.NewRows(channelStateCols).
			AddRow("account:85", channelBalanceUrgent, 4.66, now.Add(-time.Hour)).
			AddRow("account:161", channelBalanceUrgent, 3.99, now.Add(-time.Hour)).
			AddRow("account:99", channelBalanceRunway, 194.74, now.Add(-time.Hour)).
			AddRow("reject:99", 1, float64(now.Unix()), now.Add(-time.Minute)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelRecharge, "info", "渠道已充值", bodyContains{
			"上游：51.222.42.218:17777、kedaya.ai", "账号：#85 acct85、#161 acct161", "余额：$3.99 → $3343.11 ~ $3343.52", "已恢复监控。",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	for _, st := range []struct {
		subject string
		level   int
		value   float64
	}{{"account:99", channelBalanceUrgent, 81.36}, {"account:85", channelBalanceOK, 3343.11}, {"account:161", channelBalanceOK, 3343.11}} {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).
			WithArgs(st.subject, st.level, st.value).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	svc.evaluateChannelBalances(context.Background(), cfg, append(kedaya, xapi), false)
	require.NoError(t, mock.ExpectationsWereMet())
}

var channelStateCols = []string{"subject", "level", "value", "updated_at"}

func expectEvalLock(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET LOCAL lock_timeout")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(channelBalanceEvalLock).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// TestEvaluateChannelBalancesRemindsUrgentWallet 告急后没充值、又用掉两成且仍在快速扣费：再提醒一次，
// 消息带上次提醒的时间（北京时间）和余额。
func TestEvaluateChannelBalancesRemindsUrgentWallet(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	pushedAt := time.Date(2026, 11, 8, 5, 21, 0, 0, time.UTC)
	now := pushedAt.Add(32 * time.Minute)
	svc := NewSecurityAlertService(db, nil, nil)
	svc.now = func() time.Time { return now }

	members := []channelBalanceReading{
		reading(54, "api.ymocode.com", 127.89, now, 320),
		reading(76, "api.ymocode.com", 127.89, now, 320),
		reading(150, "api.ymocode.com", 127.89, now, 320),
	}
	for i := range members {
		members[i].Pace30, members[i].Drained, members[i].DrainedSpan = 160, 855, 24*time.Hour
		members[i].Prev = f64(140)
	}
	expectEvalLock(mock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject = ANY")).
		WillReturnRows(sqlmock.NewRows(channelStateCols).
			AddRow("account:54", channelBalanceUrgent, 213.33, pushedAt).
			AddRow("account:76", channelBalanceUrgent, 213.33, pushedAt).
			AddRow("account:150", channelBalanceUrgent, 213.33, pushedAt))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelBal, "critical", "渠道余额即将用完（再次提醒）", bodyContains{
			"账号：#54 acct54、#76 acct76、#150 acct150",
			"余额：$127.89（13:21 提醒时 $213.33，还没充值）",
			"按近 30 分钟扣费速度每小时约 $160.00",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	for _, subject := range []string{"account:54", "account:76", "account:150"} {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).
			WithArgs(subject, channelBalanceUrgent, 127.89).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	svc.evaluateChannelBalances(context.Background(), DefaultSecurityAlertConfig(), members, true)
	require.NoError(t, mock.ExpectationsWereMet())

	// 状态写不进去时整轮回滚（告警也不落库），下一轮重来，不会出现推了却没记状态。
	expectEvalLock(mock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject = ANY")).
		WillReturnRows(sqlmock.NewRows(channelStateCols).AddRow("account:54", channelBalanceUrgent, 213.33, pushedAt))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state")).WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()
	svc.evaluateChannelBalances(context.Background(), DefaultSecurityAlertConfig(), members[:1], true)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExpandHotBalanceTargets(t *testing.T) {
	svc := NewSecurityAlertService(nil, nil, nil)
	// 上次全量刷新：Kedaya 两个域名是同一个钱包。
	svc.rememberWallets([]channelBalanceWallet{{Members: []channelBalanceReading{{AccountID: 85}, {AccountID: 161}}}}, true)
	targets := []channelBalanceTarget{
		{ID: 54, Site: "api.ymocode.com"}, {ID: 76, Site: "api.ymocode.com"}, {ID: 150, Site: "api.ymocode.com"},
		{ID: 85, Site: "kedaya.ai"}, {ID: 161, Site: "51.222.42.218:17777"},
		{ID: 99, Site: "xapi-key.net"},
	}
	var ids []int64
	for _, tg := range svc.expandHotBalanceTargets(targets, []int64{150, 161}) {
		ids = append(ids, tg.ID)
	}
	require.Equal(t, []int64{54, 76, 150, 85, 161}, ids, "same site + same wallet, nothing else")
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
