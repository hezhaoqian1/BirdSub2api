package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

// 渠道与用户余额告警（和安全告警共用企业微信机器人与 outbox，消息标题为「运营告警」）：
//   - 渠道大额消费：渠道（上游账号）近 1 小时成本超过下限，且超过它过去 7 天单小时 P95 的若干倍；
//   - 渠道余额：leader 定时刷新能识别余额的上游账号，按钱包（同一上游钱包下的多个账号，可能挂在
//     不同域名下）判定：按最近扣费速度预计几小时内用完 → 低于提醒线 → 预计 1 小时内用完 → 已用完，
//     每个钱包每个级别只推一次；检测到充值后推一条「渠道已充值」并重新开始监控。快用完的钱包
//     每 2 分钟复查一次。近 24 小时没有流量的渠道只记状态、进日报，不单独推送；
//   - 上游开始拒绝：上游 5 分钟内多次以「余额/额度不足」拒绝请求时立即推送（见
//     security_alert_balance_reject.go），这是最早的信号，也覆盖查不到余额的渠道；
//   - 渠道额度报错：账号因额度不足被停用时推送；
//   - 关注用户余额：管理员指定的用户余额低于提醒线时推送，充值回到提醒线以上后重新开始监控。

const (
	securityAlertKindChannelSpend    = "channel_spend"
	securityAlertKindChannelBal      = "channel_balance"
	securityAlertKindChannelRecharge = "channel_recharge"
	securityAlertKindChannelQuota    = "channel_quota_error"
	securityAlertKindUserBalance     = "user_balance"

	securityAlertBalanceSweepSubject    = "sweep:channel_balance"
	securityAlertBalanceHotSweepSubject = "sweep:channel_balance_hot"
	securityAlertBalanceHotSweepEvery   = 2 * time.Minute
	securityAlertBalanceHotWindow       = 6 * time.Hour
	securityAlertBalanceHotMax          = 20
	securityAlertBalanceSweepTimeout    = 4 * time.Minute
	securityAlertBalanceSweepWorkers    = 2
	securityAlertBalanceSampleWindow    = 3 * time.Hour
	securityAlertBalanceSampleTTL       = 48 * time.Hour
	securityAlertBalanceFailedRetry     = 6 * time.Hour
	securityAlertBalanceIdleWindow      = 24 * time.Hour
	securityAlertQuotaErrorWindow       = 10 * time.Minute
)

// channelQuotaErrorPattern 匹配上游「额度不足/欠费」类报错（PostgreSQL 正则，大小写不敏感）。
const channelQuotaErrorPattern = `额度不足|余额不足|欠费|insufficient|quota|balance|credit|payment required|billing|arrear`

// securityAccountCostExpr 渠道成本口径，与利润统计、仪表盘 account_cost 一致。
const securityAccountCostExpr = "COALESCE(u.account_stats_cost, u.total_cost) * COALESCE(u.account_rate_multiplier, 1)"

// 渠道余额告警级别，数值越大越严重，存于 security_alert_state.level。
// 加入「即将用完」之前「已用完」是 3；旧的 3 会被读成「即将用完」，余额仍 ≤ 0 时会再升级一次。
const (
	channelBalanceOK     = 0
	channelBalanceRunway = 1 // 按扣费速度预计 ChannelRunwayHours 内用完
	channelBalanceLow    = 2 // 低于固定提醒线（多见于流量小的渠道）
	channelBalanceUrgent = 3 // 按扣费速度预计 ChannelUrgentMinutes 内用完
	channelBalanceEmpty  = 4 // 已用完
)

// isOpsAlertKind 渠道/用户余额类告警，投递时使用「运营告警」标题。
func isOpsAlertKind(kind string) bool {
	return strings.HasPrefix(kind, "channel_") || kind == securityAlertKindUserBalance || kind == securityAlertKindUserWatch
}

// channelBalanceReader 上游余额查询（*UpstreamBalanceService 实现，测试可替换）。
type channelBalanceReader interface {
	State(ctx context.Context, id int64, refresh, force bool) (UpstreamBalanceState, error)
}

func accountAlertSubject(id int64) string { return "account:" + strconv.FormatInt(id, 10) }

// ---- 纯函数：级别判定与状态迁移 ----

func channelLowBalanceThreshold(cfg SecurityAlertConfig, unit string) float64 {
	switch strings.ToUpper(unit) {
	case "USD":
		return cfg.ChannelLowBalanceUSD
	case "CNY":
		return cfg.ChannelLowBalanceCNY
	default:
		return 0 // 未换算的原始额度没有可比的提醒线，只看扣费速度
	}
}

// evaluateChannelBalanceLevel 根据余额与扣费速度（每小时，0 表示未知）判定级别。
func evaluateChannelBalanceLevel(cfg SecurityAlertConfig, remaining float64, unit string, burnPerHour float64) int {
	if remaining <= 0 {
		return channelBalanceEmpty
	}
	hours := math.Inf(1)
	if burnPerHour > 0 {
		hours = remaining / burnPerHour
	}
	if cfg.ChannelUrgentMinutes > 0 && hours*60 < float64(cfg.ChannelUrgentMinutes) {
		return channelBalanceUrgent
	}
	if threshold := channelLowBalanceThreshold(cfg, unit); threshold > 0 && remaining < threshold {
		return channelBalanceLow
	}
	if cfg.ChannelRunwayHours > 0 && hours < cfg.ChannelRunwayHours {
		return channelBalanceRunway
	}
	return channelBalanceOK
}

// balanceRecharged 余额比上次记录明显增加，视为充值。
func balanceRecharged(prev, cur float64) bool {
	return cur > prev+math.Max(0.01, math.Abs(prev)*0.01)
}

// nextBalanceAlertState 计算新的告警状态：级别升高时告警并记下当时的余额；级别降低只在
// 检测到充值时生效（避免流量忽高忽低时反复推送）。
func nextBalanceAlertState(prevLevel int, prevValue float64, level int, value float64) (int, float64, bool) {
	switch {
	case level > prevLevel:
		return level, value, true
	case level < prevLevel && balanceRecharged(prevValue, value):
		return level, value, false
	default:
		return prevLevel, prevValue, false
	}
}

type balanceSample struct {
	At        time.Time
	Remaining float64
	Unit      string
}

// balanceBurnWindows 估算扣费速度的时间窗口，取其中最快的一个。只看 3 小时平均会把突发流量摊薄：
// 2026-11-08 XApi 05:10 起每小时扣约 $330，3 小时平均只有 $46，推送说还能撑 4.2 小时，实际约 35 分钟。
var balanceBurnWindows = []time.Duration{30 * time.Minute, time.Hour, 3 * time.Hour}

// balanceBurnMinSpan 窗口内样本跨度不足该值时不估算，避免两三个点的抖动被当成速度。
const balanceBurnMinSpan = 20 * time.Minute

// balanceBurn 扣费速度：每小时扣多少，以及取自哪个时间窗口。
type balanceBurn struct {
	PerHour float64
	Window  time.Duration
}

// estimateBalanceBurn 由按时间排序的余额样本估算扣费速度：各窗口内累计下降 / 时间跨度，取最大值。
// 充值造成的上涨不计入；只看和最新样本同币种的样本。
func estimateBalanceBurn(samples []balanceSample) balanceBurn {
	if len(samples) < 2 {
		return balanceBurn{}
	}
	last := samples[len(samples)-1]
	var best balanceBurn
	for _, window := range balanceBurnWindows {
		since := last.At.Add(-window - time.Minute) // 定时采样有几十秒抖动
		var drained float64
		var first, prev *balanceSample
		for i := range samples {
			cur := &samples[i]
			if cur.Unit != last.Unit || cur.At.Before(since) {
				continue
			}
			if prev != nil && prev.Remaining > cur.Remaining {
				drained += prev.Remaining - cur.Remaining
			}
			if first == nil {
				first = cur
			}
			prev = cur
		}
		if first == nil || drained <= 0 {
			continue
		}
		span := last.At.Sub(first.At)
		if span < balanceBurnMinSpan {
			continue
		}
		// 相差不到 1% 时保留较短的窗口（显示「近 30 分钟」比「近 3 小时」更有参考价值）。
		if rate := drained / span.Hours(); rate > best.PerHour*1.01 {
			best = balanceBurn{PerHour: rate, Window: window}
		}
	}
	return best
}

// balanceBurnPerHour 只取速度（每小时），0 表示未知。
func balanceBurnPerHour(samples []balanceSample) float64 {
	return estimateBalanceBurn(samples).PerHour
}

func formatBalanceAmount(v float64, unit string) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	if v < 0.005 {
		sign = "" // 不显示「-$0.00」
	}
	switch strings.ToUpper(unit) {
	case "USD":
		return fmt.Sprintf("%s$%.2f", sign, v)
	case "CNY":
		return fmt.Sprintf("%s¥%.2f", sign, v)
	default:
		return fmt.Sprintf("%s%.2f %s", sign, v, sanitizeSecurityText(unit, 16))
	}
}

func formatHours(h float64) string {
	switch {
	case h < 1:
		return fmt.Sprintf("%.0f 分钟", math.Max(1, h*60))
	case h < 48:
		return fmt.Sprintf("%.1f 小时", h)
	default:
		return fmt.Sprintf("%.0f 天", h/24)
	}
}

func formatBurnWindow(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%.0f 分钟", d.Minutes())
	}
	return fmt.Sprintf("%.0f 小时", d.Hours())
}

// balanceBurnLine 「按近 30 分钟扣费速度每小时约 $330.00，预计 15 分钟后用完」；速度未知时返回空。
func balanceBurnLine(burn balanceBurn, remaining float64, unit string) string {
	if burn.PerHour < 0.01 {
		return ""
	}
	line := fmt.Sprintf("按近 %s扣费速度每小时约 %s", formatBurnWindow(burn.Window), formatBalanceAmount(burn.PerHour, unit))
	if remaining > 0 {
		if hours := remaining / burn.PerHour; hours < 30*24 {
			line += "，预计 " + formatHours(hours) + "后用完"
		}
	}
	return line
}

func joinLimited(items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, "、")
	}
	return strings.Join(items[:limit], "、") + fmt.Sprintf(" 等 %d 个", len(items))
}

func balanceSiteHost(baseURL string) string {
	root, err := upstreamBalanceRoot(baseURL)
	if err != nil {
		return ""
	}
	if u, err := url.Parse(root); err == nil {
		return u.Host
	}
	return ""
}

// channelBalanceReading 一次刷新得到的渠道余额。
type channelBalanceReading struct {
	AccountID int64
	Name      string
	Platform  string
	Site      string
	Remaining float64
	Unit      string
	Scope     string
	FetchedAt time.Time
	Burn      balanceBurn
	// Prev 上一次同币种采样的余额（用于「充值前 → 充值后」），没有时为 nil。
	Prev *float64
	// UnitChanged 和上一次采样的币种不同（如换算参数偶发读取失败），本轮不参与判定。
	UnitChanged bool
	// Active 近 24 小时有请求。
	Active bool
}

func channelAccountLabel(id int64, name string) string {
	return fmt.Sprintf("#%d %s", id, sanitizeSecurityText(name, 40))
}

// channelBalanceWallet 同一个上游钱包：其下的账号（可能挂在不同域名下）合并成一条告警、共用一个级别。
type channelBalanceWallet struct {
	Members []channelBalanceReading
	Sites   []string
	Unit    string
	Scope   string
	Low     float64
	High    float64
	Burn    balanceBurn
	Prev    *float64
	Active  bool
}

// sameBalanceWallet 判断 r 是否和 head 属于同一个钱包：同币种、同额度类型、余额足够接近。
// 同一轮里各账号的查询时间相差几十秒到几分钟，扣得快的钱包按扣费速度放宽容差；burnPerHour 取两边
// 较慢的一个（同一钱包两边速度一样），不扣费的渠道不会因为别的钱包扣得快就被并进去。
func sameBalanceWallet(head, r channelBalanceReading, burnPerHour float64) bool {
	if head.Unit != r.Unit || head.Scope != r.Scope {
		return false
	}
	gap := head.FetchedAt.Sub(r.FetchedAt)
	if gap < 0 {
		gap = -gap
	}
	drift := burnPerHour * (gap + 30*time.Second).Hours() * 1.5
	tolerance := math.Max(drift, math.Max(0.5, math.Abs(head.Remaining)*0.01))
	if head.Site != r.Site {
		// 跨域名只合并账户钱包，且容差收紧：不同上游的余额碰巧接近（尤其都见底时）不能并成一个。
		if head.Scope != "account" || math.Abs(head.Remaining) < 1 {
			return false
		}
		tolerance = math.Max(drift, 0.05)
	}
	return math.Abs(r.Remaining-head.Remaining) <= tolerance
}

// groupChannelWallets 把读数归并成钱包（按余额从低到高，与钱包里余额最低的账号比较）。
func groupChannelWallets(readings []channelBalanceReading) []channelBalanceWallet {
	sorted := append([]channelBalanceReading(nil), readings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Unit != b.Unit {
			return a.Unit < b.Unit
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Remaining != b.Remaining {
			return a.Remaining < b.Remaining
		}
		return a.AccountID < b.AccountID
	})
	var wallets []channelBalanceWallet
	for _, r := range sorted {
		placed := false
		for i := range wallets {
			w := &wallets[i]
			if sameBalanceWallet(w.Members[0], r, math.Min(w.Burn.PerHour, r.Burn.PerHour)) {
				w.Members = append(w.Members, r)
				if r.Burn.PerHour > w.Burn.PerHour {
					w.Burn = r.Burn
				}
				placed = true
				break
			}
		}
		if !placed {
			wallets = append(wallets, channelBalanceWallet{Members: []channelBalanceReading{r}, Burn: r.Burn})
		}
	}
	for i := range wallets {
		w := &wallets[i]
		head := w.Members[0]
		w.Unit, w.Scope, w.Low, w.High, w.Prev = head.Unit, head.Scope, head.Remaining, head.Remaining, head.Prev
		seen := map[string]bool{}
		for _, m := range w.Members {
			w.High = math.Max(w.High, m.Remaining)
			w.Active = w.Active || m.Active
			site := m.Site
			if site == "" {
				site = "-"
			}
			if !seen[site] {
				seen[site] = true
				w.Sites = append(w.Sites, site)
			}
		}
		sort.Strings(w.Sites)
		sort.SliceStable(w.Members, func(a, b int) bool { return w.Members[a].AccountID < w.Members[b].AccountID })
	}
	return wallets
}

// walletHeadID 钱包的代表账号（ID 最小的成员），用于去重键。
func (w channelBalanceWallet) walletHeadID() int64 { return w.Members[0].AccountID }

func (w channelBalanceWallet) headerLines() []string {
	labels := make([]string, 0, len(w.Members))
	for _, m := range w.Members {
		labels = append(labels, channelAccountLabel(m.AccountID, m.Name))
	}
	sites := make([]string, 0, len(w.Sites))
	for _, s := range w.Sites {
		sites = append(sites, sanitizeSecurityText(s, 80))
	}
	return []string{"上游：" + strings.Join(sites, "、"), "账号：" + joinLimited(labels, 6)}
}

func (w channelBalanceWallet) amountText() string {
	amount := formatBalanceAmount(w.Low, w.Unit)
	if highText := formatBalanceAmount(w.High, w.Unit); highText != amount {
		amount += " ~ " + highText
	}
	switch w.Scope {
	case "token":
		amount += "（令牌额度）"
	case "subscription":
		amount += "（订阅额度）"
	}
	return amount
}

// buildChannelBalanceDraft 一个钱包升到 level 时的告警。
func buildChannelBalanceDraft(cfg SecurityAlertConfig, w channelBalanceWallet, level int, at time.Time) securityAlertDraft {
	lines := append(w.headerLines(), "余额："+w.amountText())
	if line := balanceBurnLine(w.Burn, w.Low, w.Unit); line != "" {
		lines = append(lines, line)
	}
	d := securityAlertDraft{
		DedupeKey: fmt.Sprintf("channel_balance:%d:%d:%s", level, w.walletHeadID(), securityBucket(at, time.Minute)),
		Kind:      securityAlertKindChannelBal,
		Severity:  SecurityAlertWarning,
	}
	switch level {
	case channelBalanceEmpty:
		d.Severity = SecurityAlertCritical
		d.Title = "渠道余额已用完"
		lines = append(lines, "请求可能已经开始失败，请尽快充值。")
	case channelBalanceUrgent:
		d.Severity = SecurityAlertCritical
		d.Title = "渠道余额即将用完"
		lines = append(lines, fmt.Sprintf("提醒条件：按最近扣费速度预计 %d 分钟内用完。请尽快充值，充值后会自动恢复监控。", cfg.ChannelUrgentMinutes))
	case channelBalanceLow:
		d.Title = "渠道余额不足"
		lines = append(lines, "提醒线："+formatBalanceAmount(channelLowBalanceThreshold(cfg, w.Unit), w.Unit), "请及时充值，充值后会自动恢复监控。")
	default:
		d.Title = "渠道余额预计很快用完"
		lines = append(lines, fmt.Sprintf("提醒条件：按最近扣费速度预计 %s内用完。请及时充值，充值后会自动恢复监控。", formatHours(cfg.ChannelRunwayHours)))
	}
	d.Lines = lines
	return d
}

// buildChannelRechargeDraft 告警过的钱包检测到充值。
func buildChannelRechargeDraft(cfg SecurityAlertConfig, w channelBalanceWallet, level int, at time.Time) securityAlertDraft {
	lines := w.headerLines()
	if w.Prev != nil {
		lines = append(lines, fmt.Sprintf("余额：%s → %s", formatBalanceAmount(*w.Prev, w.Unit), w.amountText()))
	} else {
		lines = append(lines, "充值后余额："+w.amountText())
	}
	if line := balanceBurnLine(w.Burn, w.Low, w.Unit); line != "" {
		lines = append(lines, line)
	}
	switch level {
	case channelBalanceOK:
		lines = append(lines, "已恢复监控。")
	case channelBalanceRunway:
		lines = append(lines, fmt.Sprintf("注意：按最近扣费速度仍预计 %s内用完。", formatHours(cfg.ChannelRunwayHours)))
	case channelBalanceLow:
		lines = append(lines, "注意：充值后仍低于提醒线 "+formatBalanceAmount(channelLowBalanceThreshold(cfg, w.Unit), w.Unit)+"。")
	default:
		lines = append(lines, fmt.Sprintf("注意：按最近扣费速度仍预计 %d 分钟内用完。", cfg.ChannelUrgentMinutes))
	}
	return securityAlertDraft{
		DedupeKey: fmt.Sprintf("channel_recharge:%d:%s", w.walletHeadID(), securityBucket(at, time.Minute)),
		Kind:      securityAlertKindChannelRecharge,
		Severity:  SecurityAlertInfo,
		Title:     "渠道已充值",
		Lines:     lines,
	}
}

// ---- 告警状态表 ----

type balanceAlertState struct {
	Level int
	Value float64
}

func (s *SecurityAlertService) loadAlertStates(ctx context.Context, subjects []string) (map[string]balanceAlertState, error) {
	states := make(map[string]balanceAlertState, len(subjects))
	if len(subjects) == 0 {
		return states, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT subject, level, value FROM security_alert_state WHERE subject = ANY($1)`, pq.Array(subjects))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var subject string
		var st balanceAlertState
		if err := rows.Scan(&subject, &st.Level, &st.Value); err != nil {
			return nil, err
		}
		states[subject] = st
	}
	return states, rows.Err()
}

func (s *SecurityAlertService) saveAlertState(ctx context.Context, subject string, st balanceAlertState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO security_alert_state(subject, level, value, updated_at)
 VALUES ($1, $2, $3, NOW())
 ON CONFLICT (subject) DO UPDATE SET level = EXCLUDED.level, value = EXCLUDED.value, updated_at = NOW()`,
		subject, st.Level, st.Value)
	return err
}

// claimSweep 在所有实例间按间隔领取一次周期任务。
func (s *SecurityAlertService) claimSweep(ctx context.Context, subject string, every time.Duration) (bool, error) {
	var got string
	err := s.db.QueryRowContext(ctx, `INSERT INTO security_alert_state(subject, updated_at) VALUES ($1, NOW())
 ON CONFLICT (subject) DO UPDATE SET updated_at = NOW()
 WHERE security_alert_state.updated_at <= NOW() - ($2::int * INTERVAL '1 second')
 RETURNING subject`, subject, int(every.Seconds())-20).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ---- 渠道余额定时刷新 ----

// maybeStartBalanceSweep 到期时在后台刷新上游余额（不阻塞 tick；跨实例由 claimSweep 协调）：
// 全量每 ChannelBalanceCheckMinutes 一次；快用完、已用完或正被上游拒绝的钱包每 2 分钟复查一次。
func (s *SecurityAlertService) maybeStartBalanceSweep(snap securityRuleSnapshot) {
	minutes := snap.cfg.ChannelBalanceCheckMinutes
	if s.balances == nil || minutes <= 0 {
		return
	}
	now := s.now()
	if now.Sub(s.lastBalanceSweepCheck) < time.Minute || s.balanceSweepInProgress.Load() {
		return
	}
	s.lastBalanceSweepCheck = now
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var only []int64
	claimed, err := s.claimSweep(ctx, securityAlertBalanceSweepSubject, time.Duration(minutes)*time.Minute)
	if err == nil && !claimed {
		only, err = s.loadHotBalanceAccounts(ctx, now)
		if err == nil && len(only) > 0 {
			claimed, err = s.claimSweep(ctx, securityAlertBalanceHotSweepSubject, securityAlertBalanceHotSweepEvery)
		}
	}
	if err != nil {
		slog.Warn("[SecurityAlert] failed to claim balance sweep", "error", err)
		return
	}
	if !claimed || !s.balanceSweepInProgress.CompareAndSwap(false, true) {
		return
	}
	cfg := snap.cfg
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.balanceSweepInProgress.Store(false)
		s.runBalanceSweep(cfg, only)
	}()
}

// loadHotBalanceAccounts 近 6 小时进入「即将用完 / 已用完」或正被上游以余额不足拒绝的账号。
func (s *SecurityAlertService) loadHotBalanceAccounts(ctx context.Context, now time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT split_part(subject, ':', 2)::bigint FROM security_alert_state
 WHERE updated_at > $1 AND ((subject LIKE 'account:%' AND level >= $2) OR (subject LIKE 'reject:%' AND level > 0))
 LIMIT $3`, now.Add(-securityAlertBalanceHotWindow), channelBalanceUrgent, securityAlertBalanceHotMax)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type channelBalanceTarget struct {
	ID       int64
	Name     string
	Platform string
	Site     string
}

// runBalanceSweep 刷新余额并判定；only 非空时只复查这些账号（快用完的钱包），且绕过 5 分钟缓存。
func (s *SecurityAlertService) runBalanceSweep(cfg SecurityAlertConfig, only []int64) {
	ctx, cancel := context.WithTimeout(context.Background(), securityAlertBalanceSweepTimeout)
	defer cancel()
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[SecurityAlert] balance sweep panicked", "panic", r)
		}
	}()

	start := s.now()
	targets, err := s.loadBalanceSweepTargets(ctx, start)
	if err != nil {
		slog.Warn("[SecurityAlert] failed to load balance sweep targets", "error", err)
		return
	}
	if only != nil {
		wanted := make(map[int64]bool, len(only))
		for _, id := range only {
			wanted[id] = true
		}
		kept := targets[:0]
		for _, t := range targets {
			if wanted[t.ID] {
				kept = append(kept, t)
			}
		}
		targets = kept
	}
	readings := s.refreshChannelBalances(ctx, targets, start, only != nil)
	if len(readings) == 0 {
		return
	}
	// 后续写库与判定不受刷新超时影响。
	ctx, cancel2 := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel2()
	if err := s.recordBalanceSamples(ctx, readings); err != nil {
		slog.Warn("[SecurityAlert] failed to record balance samples", "error", err)
	}
	if err := s.fillBurnRates(ctx, readings); err != nil {
		slog.Warn("[SecurityAlert] failed to load balance samples", "error", err)
	}
	s.fillChannelActivity(ctx, readings)
	s.evaluateChannelBalances(ctx, cfg, readings, only == nil)
	slog.Info("[SecurityAlert] balance sweep finished", "hot", only != nil, "targets", len(targets), "readings", len(readings), "elapsed", s.now().Sub(start).Round(time.Second))
}

// loadBalanceSweepTargets 选出参与调度、配置了 Base URL 的 API Key 账号；关闭余额查询的跳过，
// 从未查询成功且近 6 小时失败过的也跳过（避免反复请求不支持余额接口的上游）。
func (s *SecurityAlertService) loadBalanceSweepTargets(ctx context.Context, now time.Time) ([]channelBalanceTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.name, a.platform, COALESCE(a.credentials->>'base_url', ''),
 COALESCE(b.config->>'mode', ''), COALESCE(b.snapshot::text, '')
 FROM accounts a LEFT JOIN upstream_balance_accounts b ON b.account_id = a.id
 WHERE a.deleted_at IS NULL AND a.type = 'apikey' AND a.status = 'active' AND a.schedulable
   AND COALESCE(a.credentials->>'api_key', '') <> '' AND COALESCE(a.credentials->>'base_url', '') <> ''
 ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var targets []channelBalanceTarget
	for rows.Next() {
		var t channelBalanceTarget
		var baseURL, mode, rawSnapshot string
		if err := rows.Scan(&t.ID, &t.Name, &t.Platform, &baseURL, &mode, &rawSnapshot); err != nil {
			return nil, err
		}
		if mode == "disabled" {
			continue
		}
		if rawSnapshot != "" {
			var snap UpstreamBalanceSnapshot
			if json.Unmarshal([]byte(rawSnapshot), &snap) == nil && snap.Data == nil && snap.Error != "" &&
				now.Sub(time.Unix(snap.AttemptAt, 0)) < securityAlertBalanceFailedRetry {
				continue
			}
		}
		t.Site = balanceSiteHost(baseURL)
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// refreshChannelBalances 并发刷新余额（只占用余额查询 4 个名额中的 2 个，管理端仍可查询）。
func (s *SecurityAlertService) refreshChannelBalances(ctx context.Context, targets []channelBalanceTarget, start time.Time, force bool) []channelBalanceReading {
	var (
		mu       sync.Mutex
		readings []channelBalanceReading
		wg       sync.WaitGroup
	)
	maxAge := 6 * time.Minute
	if force {
		maxAge = time.Minute
	}
	jobs := make(chan channelBalanceTarget)
	for i := 0; i < securityAlertBalanceSweepWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				state, err := s.balances.State(ctx, t.ID, true, force)
				if err != nil {
					continue
				}
				data := state.Snapshot.Data
				if data == nil || data.Remaining == nil || data.Unlimited {
					continue
				}
				// 只用本轮（或 5 分钟缓存内）拿到的结果，不拿过期快照判定。
				fetched := time.Unix(state.Snapshot.FetchedAt, 0)
				if start.Sub(fetched) > maxAge {
					continue
				}
				mu.Lock()
				readings = append(readings, channelBalanceReading{
					AccountID: t.ID, Name: t.Name, Platform: t.Platform, Site: t.Site,
					Remaining: *data.Remaining, Unit: data.Unit, Scope: data.Scope, FetchedAt: fetched,
				})
				mu.Unlock()
			}
		}()
	}
	for _, t := range targets {
		select {
		case jobs <- t:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	sort.Slice(readings, func(i, j int) bool { return readings[i].AccountID < readings[j].AccountID })
	return readings
}

func (s *SecurityAlertService) recordBalanceSamples(ctx context.Context, readings []channelBalanceReading) error {
	now := s.now().Unix()
	ids := make([]int64, 0, len(readings))
	ats := make([]int64, 0, len(readings))
	values := make([]float64, 0, len(readings))
	units := make([]string, 0, len(readings))
	for _, r := range readings {
		ids = append(ids, r.AccountID)
		ats = append(ats, now)
		values = append(values, r.Remaining)
		units = append(units, r.Unit)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO security_alert_balance_samples(account_id, observed_at, remaining, unit)
 SELECT a, to_timestamp(t), r, u FROM unnest($1::bigint[], $2::bigint[], $3::float8[], $4::text[]) AS x(a, t, r, u)
 ON CONFLICT DO NOTHING`, pq.Array(ids), pq.Array(ats), pq.Array(values), pq.Array(units))
	return err
}

// fillBurnRates 读取近 3 小时样本（含本轮刚写入的一条），估算扣费速度，并找出上一条样本。
func (s *SecurityAlertService) fillBurnRates(ctx context.Context, readings []channelBalanceReading) error {
	ids := make([]int64, 0, len(readings))
	for _, r := range readings {
		ids = append(ids, r.AccountID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT account_id, observed_at, remaining, unit FROM security_alert_balance_samples
 WHERE account_id = ANY($1) AND observed_at > $2 ORDER BY account_id, observed_at`,
		pq.Array(ids), s.now().Add(-securityAlertBalanceSampleWindow))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	samples := map[int64][]balanceSample{}
	for rows.Next() {
		var id int64
		var sm balanceSample
		if err := rows.Scan(&id, &sm.At, &sm.Remaining, &sm.Unit); err != nil {
			return err
		}
		samples[id] = append(samples[id], sm)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range readings {
		list := samples[readings[i].AccountID]
		readings[i].Burn = estimateBalanceBurn(list)
		// 最后一条是本轮刚写入的读数；再往前一条是上一次采样。
		if n := len(list); n >= 2 {
			prev := list[n-2]
			if prev.Unit != readings[i].Unit {
				readings[i].UnitChanged = true
			} else {
				v := prev.Remaining
				readings[i].Prev = &v
			}
		}
	}
	return nil
}

// fillChannelActivity 标记近 24 小时有请求的账号；查询失败时全部按有流量处理（宁可多推）。
func (s *SecurityAlertService) fillChannelActivity(ctx context.Context, readings []channelBalanceReading) {
	ids := make([]int64, 0, len(readings))
	for _, r := range readings {
		ids = append(ids, r.AccountID)
	}
	active := map[int64]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT a FROM unnest($1::bigint[]) AS a
 WHERE EXISTS (SELECT 1 FROM usage_logs u WHERE u.account_id = a AND u.created_at > $2)`,
		pq.Array(ids), s.now().Add(-securityAlertBalanceIdleWindow))
	if err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				active[id] = true
			}
		}
		err = rows.Err()
		_ = rows.Close()
	}
	if err != nil {
		slog.Warn("[SecurityAlert] channel activity query failed", "error", err)
		for i := range readings {
			readings[i].Active = true
		}
		return
	}
	for i := range readings {
		readings[i].Active = active[readings[i].AccountID]
	}
}

// evaluateChannelBalances 按钱包判定级别并推送。同一钱包的账号共用一个级别（取成员里最高的旧级别），
// 推送后所有成员一起记状态，避免同一钱包隔一轮又推一次。
func (s *SecurityAlertService) evaluateChannelBalances(ctx context.Context, cfg SecurityAlertConfig, readings []channelBalanceReading, full bool) {
	usable := make([]channelBalanceReading, 0, len(readings))
	for _, r := range readings {
		if !r.UnitChanged {
			usable = append(usable, r)
		}
	}
	wallets := groupChannelWallets(usable)
	subjects := make([]string, 0, 2*len(usable))
	for _, r := range usable {
		subjects = append(subjects, accountAlertSubject(r.AccountID), rejectAlertSubject(r.AccountID))
	}
	states, err := s.loadAlertStates(ctx, subjects)
	if err != nil {
		slog.Warn("[SecurityAlert] failed to load channel alert states", "error", err)
		return
	}
	type change struct {
		subject string
		state   balanceAlertState
	}
	now := s.now()
	var drafts []securityAlertDraft
	var changed []change
	for _, w := range wallets {
		var prev balanceAlertState
		rejecting := false
		for _, m := range w.Members {
			if st := states[accountAlertSubject(m.AccountID)]; st.Level > prev.Level {
				prev = st
			}
			rejecting = rejecting || states[rejectAlertSubject(m.AccountID)].Level > 0
		}
		raw := evaluateChannelBalanceLevel(cfg, w.Low, w.Unit, w.Burn.PerHour)
		level, value, alert := nextBalanceAlertState(prev.Level, prev.Value, raw, w.Low)
		switch {
		case alert && !w.Active:
			// 近 24 小时没有流量：只记状态，日报里列出，不单独推送。
		case alert && rejecting && level < channelBalanceEmpty:
			// 已推过「上游开始拒绝」，更低级别的余额提醒不再重复。
		case alert:
			drafts = append(drafts, buildChannelBalanceDraft(cfg, w, level, now))
		case level < prev.Level && prev.Level >= channelBalanceLow && cfg.ChannelRechargeNotify && w.Active:
			drafts = append(drafts, buildChannelRechargeDraft(cfg, w, level, now))
		}
		for _, m := range w.Members {
			subject := accountAlertSubject(m.AccountID)
			if st := states[subject]; st.Level != level || st.Value != value {
				changed = append(changed, change{subject, balanceAlertState{Level: level, Value: value}})
			}
		}
	}
	s.rememberWallets(wallets, full)
	// 先写 outbox 再记状态：记状态失败最多重复提醒一次，不会漏报。
	for _, d := range drafts {
		s.emitLogged(ctx, d)
	}
	for _, c := range changed {
		if err := s.saveAlertState(ctx, c.subject, c.state); err != nil {
			slog.Warn("[SecurityAlert] failed to save channel alert state", "subject", c.subject, "error", err)
		}
	}
}

// rememberWallets 记下账号 → 钱包代表账号，供「上游开始拒绝」把同一钱包的账号合并成一条。
// 全量刷新时整体替换；只复查部分账号时合并。
func (s *SecurityAlertService) rememberWallets(wallets []channelBalanceWallet, full bool) {
	s.walletMu.Lock()
	defer s.walletMu.Unlock()
	if full || s.walletOf == nil {
		s.walletOf = map[int64]int64{}
	}
	for _, w := range wallets {
		for _, m := range w.Members {
			s.walletOf[m.AccountID] = w.walletHeadID()
		}
	}
}

func (s *SecurityAlertService) walletOfAccount(id int64) (int64, bool) {
	s.walletMu.Lock()
	defer s.walletMu.Unlock()
	head, ok := s.walletOf[id]
	return head, ok
}

// ---- 渠道大额消费 ----

// checkChannelSpend 渠道近 1 小时成本超过下限，且超过它过去 7 天单小时 P95 的若干倍。
// 订阅制账号（OAuth / Setup Token）按月付费，不参与。
func (s *SecurityAlertService) checkChannelSpend(ctx context.Context, snap securityRuleSnapshot) {
	cfg := snap.cfg
	if cfg.ChannelSpendFloor <= 0 || cfg.ChannelSpendMultiplier <= 0 || !s.ensureUsageBaseline(ctx) {
		return
	}
	now := s.now()
	since := now.Add(-time.Hour)
	type candidate struct {
		id             int64
		name, platform string
		cost, revenue  float64
		requests       int
		baseline       float64
	}
	rows, err := s.db.QueryContext(ctx, `SELECT u.account_id, COALESCE(a.name, ''), COALESCE(a.platform, ''),
 SUM(`+securityAccountCostExpr+`), COALESCE(SUM(u.actual_cost), 0), COUNT(*)
 FROM usage_logs u LEFT JOIN accounts a ON a.id = u.account_id
 WHERE u.created_at > $1 AND COALESCE(a.type, '') NOT IN ('oauth', 'setup-token')
 GROUP BY 1, 2, 3
 HAVING SUM(`+securityAccountCostExpr+`) >= $2`, since, cfg.ChannelSpendFloor)
	if err != nil {
		slog.Warn("[SecurityAlert] channel spend query failed", "error", err)
		return
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.id, &c.name, &c.platform, &c.cost, &c.revenue, &c.requests) != nil {
			continue
		}
		c.baseline = s.channelSpendBaseline[c.id]
		if c.baseline > 0 && c.cost < c.baseline*cfg.ChannelSpendMultiplier {
			continue
		}
		candidates = append(candidates, c)
	}
	_ = rows.Close()

	for _, c := range candidates {
		dedupeKey := "channel_spend:" + strconv.FormatInt(c.id, 10) + ":" + securityBucket(now, 3*time.Hour)
		baselineText := "过去 7 天该渠道没有消费记录"
		if c.baseline > 0 {
			baselineText = fmt.Sprintf("过去 7 天该渠道单小时 P95 $%.2f（本小时是它的 %.1f 倍）", c.baseline, c.cost/c.baseline)
		}
		lines := []string{
			fmt.Sprintf("渠道：%s（%s）", channelAccountLabel(c.id, c.name), sanitizeSecurityText(c.platform, 20)),
			fmt.Sprintf("近 1 小时成本 $%.2f（%d 次请求，用户扣费 $%.2f）", c.cost, c.requests, c.revenue),
			baselineText,
		}
		if top := s.channelTopUsers(ctx, c.id, since); top != "" {
			lines = append(lines, "主要用户："+top)
		}
		if wallet := s.channelWalletLine(ctx, c.id); wallet != "" {
			lines = append(lines, wallet)
		}
		lines = append(lines, "如非预期，请检查这些用户的用量，必要时先停用该渠道。")
		s.emitLogged(ctx, securityAlertDraft{
			DedupeKey: dedupeKey,
			Kind:      securityAlertKindChannelSpend,
			Severity:  SecurityAlertWarning,
			Title:     "渠道大额消费",
			Lines:     lines,
		})
	}
}

func (s *SecurityAlertService) channelTopUsers(ctx context.Context, accountID int64, since time.Time) string {
	rows, err := s.db.QueryContext(ctx, `SELECT u.user_id, COALESCE(us.email, ''), SUM(`+securityAccountCostExpr+`)
 FROM usage_logs u LEFT JOIN users us ON us.id = u.user_id
 WHERE u.account_id = $1 AND u.created_at > $2
 GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 3`, accountID, since)
	if err != nil {
		return ""
	}
	defer func() { _ = rows.Close() }()
	var parts []string
	for rows.Next() {
		var userID int64
		var email string
		var cost float64
		if rows.Scan(&userID, &email, &cost) == nil {
			parts = append(parts, fmt.Sprintf("#%d %s $%.2f", userID, sanitizeSecurityText(email, 40), cost))
		}
	}
	return strings.Join(parts, "、")
}

// channelWalletLine 最近一次查到的上游余额（没有则返回空）。
func (s *SecurityAlertService) channelWalletLine(ctx context.Context, accountID int64) string {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(snapshot::text, '') FROM upstream_balance_accounts WHERE account_id = $1`, accountID).Scan(&raw); err != nil || raw == "" {
		return ""
	}
	var snap UpstreamBalanceSnapshot
	if json.Unmarshal([]byte(raw), &snap) != nil || snap.Data == nil || snap.Data.Remaining == nil || snap.FetchedAt == 0 {
		return ""
	}
	age := s.now().Sub(time.Unix(snap.FetchedAt, 0))
	ageText := "刚刚"
	switch {
	case age >= 48*time.Hour:
		ageText = fmt.Sprintf("%d 天前", int(age.Hours()/24))
	case age >= time.Hour:
		ageText = fmt.Sprintf("%d 小时前", int(age.Hours()))
	case age >= time.Minute:
		ageText = fmt.Sprintf("%d 分钟前", int(age.Minutes()))
	}
	return fmt.Sprintf("上游余额 %s（%s查询）", formatBalanceAmount(*snap.Data.Remaining, snap.Data.Unit), ageText)
}

// ---- 渠道额度报错 ----

// checkChannelQuotaErrors 账号因上游额度不足/欠费被置为错误状态时推送。
func (s *SecurityAlertService) checkChannelQuotaErrors(ctx context.Context, snap securityRuleSnapshot) {
	if !snap.cfg.ChannelQuotaErrorNotify {
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, platform, COALESCE(error_message, '')
 FROM accounts
 WHERE deleted_at IS NULL AND status = 'error' AND updated_at > $1 AND error_message ~* $2`,
		s.now().Add(-securityAlertQuotaErrorWindow), channelQuotaErrorPattern)
	if err != nil {
		slog.Warn("[SecurityAlert] channel quota error query failed", "error", err)
		return
	}
	type hit struct {
		id             int64
		name, platform string
		message        string
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if rows.Scan(&h.id, &h.name, &h.platform, &h.message) == nil {
			hits = append(hits, h)
		}
	}
	_ = rows.Close()
	for _, h := range hits {
		// 按报错内容去重：改账号名等操作不会重复推送，同一账号再次欠费（报错带新 request id）会再推。
		sum := fnv.New64a()
		_, _ = sum.Write([]byte(h.message))
		s.emitLogged(ctx, securityAlertDraft{
			DedupeKey: fmt.Sprintf("channel_quota_error:%d:%x", h.id, sum.Sum64()),
			Kind:      securityAlertKindChannelQuota,
			Severity:  SecurityAlertCritical,
			Title:     "渠道因额度不足已停用",
			Lines: []string{
				fmt.Sprintf("渠道：%s（%s）", channelAccountLabel(h.id, h.name), sanitizeSecurityText(h.platform, 20)),
				"上游报错：" + sanitizeSecurityText(h.message, 160),
				"该渠道已停止调度。充值后请到账号管理里恢复该账号。",
			},
		})
	}
}

// ---- 关注用户余额 ----

type watchedUser struct {
	Email    string
	Username string
	Balance  float64
}

func (s *SecurityAlertService) loadWatchedUsers(ctx context.Context, watches []UserBalanceWatch) (map[int64]watchedUser, error) {
	users := make(map[int64]watchedUser, len(watches))
	if len(watches) == 0 || s.db == nil {
		return users, nil
	}
	ids := make([]int64, 0, len(watches))
	for _, w := range watches {
		ids = append(ids, w.UserID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(email, ''), COALESCE(username, ''), balance::float8
 FROM users WHERE id = ANY($1) AND deleted_at IS NULL`, pq.Array(ids))
	if err != nil {
		return users, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var u watchedUser
		if err := rows.Scan(&id, &u.Email, &u.Username, &u.Balance); err != nil {
			return users, err
		}
		users[id] = u
	}
	return users, rows.Err()
}

func userBalanceLevel(balance, threshold float64) int {
	switch {
	case balance <= 0:
		return 2
	case balance < threshold:
		return 1
	default:
		return 0
	}
}

// checkUserBalanceWatches 关注用户余额低于提醒线（或用完）时推送一次，充值后重新开始监控。
func (s *SecurityAlertService) checkUserBalanceWatches(ctx context.Context, snap securityRuleSnapshot) {
	watches := snap.cfg.UserBalanceWatches
	if len(watches) == 0 {
		return
	}
	users, err := s.loadWatchedUsers(ctx, watches)
	if err != nil {
		slog.Warn("[SecurityAlert] watched user query failed", "error", err)
		return
	}
	subjects := make([]string, 0, len(watches))
	for _, w := range watches {
		subjects = append(subjects, "user:"+strconv.FormatInt(w.UserID, 10))
	}
	states, err := s.loadAlertStates(ctx, subjects)
	if err != nil {
		slog.Warn("[SecurityAlert] failed to load user alert states", "error", err)
		return
	}
	now := s.now()
	for i, w := range watches {
		u, ok := users[w.UserID]
		if !ok {
			continue
		}
		prev := states[subjects[i]]
		level, value, alert := nextBalanceAlertState(prev.Level, prev.Value, userBalanceLevel(u.Balance, w.Threshold), u.Balance)
		if alert {
			lines := []string{
				"用户：" + watchedUserLabel(w.UserID, u.Email, w.Note),
				fmt.Sprintf("当前余额 %s，提醒线 %s", formatBalanceAmount(u.Balance, "USD"), formatBalanceAmount(w.Threshold, "USD")),
			}
			var spend float64
			if s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(actual_cost), 0) FROM usage_logs WHERE user_id = $1 AND created_at > $2`,
				w.UserID, now.Add(-24*time.Hour)).Scan(&spend) == nil && spend > 0 {
				line := fmt.Sprintf("近 24 小时消费 $%.2f", spend)
				if u.Balance > 0 {
					line += "，按此速度约 " + formatHours(u.Balance/(spend/24)) + "后用完"
				}
				lines = append(lines, line)
			}
			d := securityAlertDraft{
				DedupeKey: fmt.Sprintf("user_balance:%d:%d:%s", w.UserID, level, securityBucket(now, time.Minute)),
				Kind:      securityAlertKindUserBalance,
				Severity:  SecurityAlertInfo,
				Title:     "关注用户余额低于提醒线",
				Lines:     lines,
			}
			if level >= 2 {
				d.Severity = SecurityAlertWarning
				d.Title = "关注用户余额已用完"
			}
			s.emitLogged(ctx, d)
		}
		if level != prev.Level || value != prev.Value {
			if err := s.saveAlertState(ctx, subjects[i], balanceAlertState{Level: level, Value: value}); err != nil {
				slog.Warn("[SecurityAlert] failed to save user alert state", "subject", subjects[i], "error", err)
			}
		}
	}
}

// ---- 日报 ----

// buildBalanceDigestLines 日报里的渠道成本、余额告警中的渠道与关注用户。
func (s *SecurityAlertService) buildBalanceDigestLines(ctx context.Context, cfg SecurityAlertConfig, start, end time.Time) []string {
	var lines []string

	rows, err := s.db.QueryContext(ctx, `SELECT u.account_id, COALESCE(a.name, ''), SUM(`+securityAccountCostExpr+`) AS c,
 SUM(SUM(`+securityAccountCostExpr+`)) OVER ()
 FROM usage_logs u LEFT JOIN accounts a ON a.id = u.account_id
 WHERE u.created_at >= $1 AND u.created_at < $2
 GROUP BY 1, 2 ORDER BY c DESC LIMIT 3`, start, end)
	if err == nil {
		var total float64
		var top []string
		for rows.Next() {
			var id int64
			var name string
			var cost float64
			if rows.Scan(&id, &name, &cost, &total) == nil {
				top = append(top, fmt.Sprintf("%s $%.2f", channelAccountLabel(id, name), cost))
			}
		}
		_ = rows.Close()
		if len(top) > 0 {
			lines = append(lines, fmt.Sprintf("渠道成本 $%.2f，最高：%s", total, strings.Join(top, "、")))
		}
	} else {
		slog.Warn("[SecurityAlert] digest channel cost query failed", "error", err)
	}

	rows, err = s.db.QueryContext(ctx, `SELECT a.id, a.name, COALESCE(b.snapshot::text, '')
 FROM security_alert_state st
 JOIN accounts a ON st.subject = 'account:' || a.id::text
 LEFT JOIN upstream_balance_accounts b ON b.account_id = a.id
 WHERE st.subject LIKE 'account:%' AND st.level > 0
   AND a.deleted_at IS NULL AND a.status = 'active' AND a.schedulable
   AND COALESCE(b.config->>'mode', '') <> 'disabled'
 ORDER BY st.level DESC, a.id LIMIT 50`)
	if err == nil {
		var items []string
		for rows.Next() {
			var id int64
			var name, raw string
			if rows.Scan(&id, &name, &raw) != nil {
				continue
			}
			item := channelAccountLabel(id, name)
			var snap UpstreamBalanceSnapshot
			if json.Unmarshal([]byte(raw), &snap) == nil && snap.Data != nil && snap.Data.Remaining != nil {
				item += " " + formatBalanceAmount(*snap.Data.Remaining, snap.Data.Unit)
			}
			items = append(items, item)
		}
		_ = rows.Close()
		if len(items) > 0 {
			lines = append(lines, fmt.Sprintf("余额告警中的渠道 %d 个：%s", len(items), joinLimited(items, 4)))
		}
	} else {
		slog.Warn("[SecurityAlert] digest channel balance query failed", "error", err)
	}

	if len(cfg.UserBalanceWatches) > 0 {
		users, err := s.loadWatchedUsers(ctx, cfg.UserBalanceWatches)
		if err == nil {
			var below []string
			for _, w := range cfg.UserBalanceWatches {
				if u, ok := users[w.UserID]; ok && u.Balance < w.Threshold {
					below = append(below, fmt.Sprintf("#%d %s", w.UserID, formatBalanceAmount(u.Balance, "USD")))
				}
			}
			line := fmt.Sprintf("关注用户 %d 人", len(cfg.UserBalanceWatches))
			if len(below) > 0 {
				line += fmt.Sprintf("，%d 人低于提醒线：%s", len(below), joinLimited(below, 4))
			} else {
				line += "，余额都在提醒线以上"
			}
			lines = append(lines, line)
		}
	}
	return lines
}
