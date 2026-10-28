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
//   - 渠道余额：leader 定时刷新能识别余额的上游账号，余额按近 3 小时扣费速度预计很快用完、
//     低于提醒线、或已经用完时推送。每个账号每个级别只推一次，检测到充值后重新开始监控；
//   - 渠道额度报错：查不到余额的渠道只能靠上游报错发现，账号因额度不足被停用时推送；
//   - 关注用户余额：管理员指定的用户余额低于提醒线时推送，充值回到提醒线以上后重新开始监控。

const (
	securityAlertKindChannelSpend = "channel_spend"
	securityAlertKindChannelBal   = "channel_balance"
	securityAlertKindChannelQuota = "channel_quota_error"
	securityAlertKindUserBalance  = "user_balance"

	securityAlertBalanceSweepSubject = "sweep:channel_balance"
	securityAlertBalanceSweepTimeout = 4 * time.Minute
	securityAlertBalanceSweepWorkers = 2
	securityAlertBalanceSampleWindow = 3 * time.Hour
	securityAlertBalanceSampleTTL    = 48 * time.Hour
	securityAlertBalanceFailedRetry  = 6 * time.Hour
	securityAlertQuotaErrorWindow    = 10 * time.Minute
)

// channelQuotaErrorPattern 匹配上游「额度不足/欠费」类报错（PostgreSQL 正则，大小写不敏感）。
const channelQuotaErrorPattern = `额度不足|余额不足|欠费|insufficient|quota|balance|credit|payment required|billing|arrear`

// securityAccountCostExpr 渠道成本口径，与利润统计、仪表盘 account_cost 一致。
const securityAccountCostExpr = "COALESCE(u.account_stats_cost, u.total_cost) * COALESCE(u.account_rate_multiplier, 1)"

// 渠道余额告警级别，数值越大越严重。
const (
	channelBalanceOK     = 0
	channelBalanceRunway = 1 // 按扣费速度预计很快用完
	channelBalanceLow    = 2 // 低于提醒线
	channelBalanceEmpty  = 3 // 已用完
)

// isOpsAlertKind 渠道/用户余额类告警，投递时使用「运营告警」标题。
func isOpsAlertKind(kind string) bool {
	return strings.HasPrefix(kind, "channel_") || kind == securityAlertKindUserBalance
}

// channelBalanceReader 上游余额查询（*UpstreamBalanceService 实现，测试可替换）。
type channelBalanceReader interface {
	State(ctx context.Context, id int64, refresh, force bool) (UpstreamBalanceState, error)
}

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
	if threshold := channelLowBalanceThreshold(cfg, unit); threshold > 0 && remaining < threshold {
		return channelBalanceLow
	}
	if cfg.ChannelRunwayHours > 0 && burnPerHour > 0 && remaining/burnPerHour < cfg.ChannelRunwayHours {
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

// balanceBurnPerHour 由按时间排序的余额样本估算每小时扣费；充值造成的上涨不计入，
// 跨度不足 30 分钟时返回 0（未知）。
func balanceBurnPerHour(samples []balanceSample) float64 {
	if len(samples) < 2 {
		return 0
	}
	unit := samples[len(samples)-1].Unit
	var drained float64
	var first, last *balanceSample
	for i := range samples {
		cur := &samples[i]
		if cur.Unit != unit {
			continue
		}
		if last != nil && last.Remaining > cur.Remaining {
			drained += last.Remaining - cur.Remaining
		}
		if first == nil {
			first = cur
		}
		last = cur
	}
	if first == nil || last == nil {
		return 0
	}
	span := last.At.Sub(first.At)
	if span < 30*time.Minute || drained <= 0 {
		return 0
	}
	return drained / span.Hours()
}

func formatBalanceAmount(v float64, unit string) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
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
	if h < 1 {
		return fmt.Sprintf("%.0f 分钟", math.Max(1, h*60))
	}
	return fmt.Sprintf("%.1f 小时", h)
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
	Burn      float64
	Level     int
}

func channelAccountLabel(id int64, name string) string {
	return fmt.Sprintf("#%d %s", id, sanitizeSecurityText(name, 40))
}

// buildChannelBalanceDrafts 把同一上游、同一钱包（余额相近）的账号合并成一条告警。
func buildChannelBalanceDrafts(cfg SecurityAlertConfig, alerts []channelBalanceReading, at time.Time) []securityAlertDraft {
	sort.SliceStable(alerts, func(i, j int) bool {
		a, b := alerts[i], alerts[j]
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		if a.Site != b.Site {
			return a.Site < b.Site
		}
		if a.Unit != b.Unit {
			return a.Unit < b.Unit
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		return a.Remaining < b.Remaining
	})
	var clusters [][]channelBalanceReading
	for _, r := range alerts {
		if n := len(clusters); n > 0 {
			head := clusters[n-1][0]
			if head.Level == r.Level && head.Site == r.Site && head.Unit == r.Unit && head.Scope == r.Scope &&
				math.Abs(r.Remaining-head.Remaining) <= math.Max(0.5, math.Abs(head.Remaining)*0.01) {
				clusters[n-1] = append(clusters[n-1], r)
				continue
			}
		}
		clusters = append(clusters, []channelBalanceReading{r})
	}

	drafts := make([]securityAlertDraft, 0, len(clusters))
	for _, cluster := range clusters {
		head := cluster[0]
		labels := make([]string, 0, len(cluster))
		low, high, burn := head.Remaining, head.Remaining, 0.0
		for _, r := range cluster {
			labels = append(labels, channelAccountLabel(r.AccountID, r.Name))
			low, high, burn = math.Min(low, r.Remaining), math.Max(high, r.Remaining), math.Max(burn, r.Burn)
		}
		amount := formatBalanceAmount(low, head.Unit)
		if highText := formatBalanceAmount(high, head.Unit); highText != amount {
			amount += " ~ " + highText
		}
		switch head.Scope {
		case "token":
			amount += "（令牌额度）"
		case "subscription":
			amount += "（订阅额度）"
		}
		site := head.Site
		if site == "" {
			site = "-"
		}
		lines := []string{
			"上游：" + sanitizeSecurityText(site, 80),
			"账号：" + joinLimited(labels, 4),
			"余额：" + amount,
		}
		if burn > 0 {
			line := fmt.Sprintf("近 3 小时平均每小时扣 %s", formatBalanceAmount(burn, head.Unit))
			if low > 0 {
				line += "，预计 " + formatHours(low/burn) + "后用完"
			}
			lines = append(lines, line)
		}
		d := securityAlertDraft{
			DedupeKey: fmt.Sprintf("channel_balance:%d:%d:%s", head.Level, head.AccountID, securityBucket(at, time.Minute)),
			Kind:      securityAlertKindChannelBal,
			Severity:  SecurityAlertWarning,
		}
		switch head.Level {
		case channelBalanceEmpty:
			d.Severity = SecurityAlertCritical
			d.Title = "渠道余额已用完"
			lines = append(lines, "请求可能已经开始失败，请尽快充值。")
		case channelBalanceLow:
			d.Title = "渠道余额不足"
			lines = append(lines, "提醒线："+formatBalanceAmount(channelLowBalanceThreshold(cfg, head.Unit), head.Unit), "请及时充值，充值后会自动恢复监控。")
		default:
			d.Title = "渠道余额预计很快用完"
			lines = append(lines, fmt.Sprintf("提醒条件：预计 %s内用完。请及时充值，充值后会自动恢复监控。", formatHours(cfg.ChannelRunwayHours)))
		}
		d.Lines = lines
		drafts = append(drafts, d)
	}
	return drafts
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

// maybeStartBalanceSweep 到期时在后台刷新上游余额（不阻塞 tick；跨实例由 claimSweep 协调）。
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
	claimed, err := s.claimSweep(ctx, securityAlertBalanceSweepSubject, time.Duration(minutes)*time.Minute)
	cancel()
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
		s.runBalanceSweep(cfg)
	}()
}

type channelBalanceTarget struct {
	ID       int64
	Name     string
	Platform string
	Site     string
}

func (s *SecurityAlertService) runBalanceSweep(cfg SecurityAlertConfig) {
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
	readings := s.refreshChannelBalances(ctx, targets, start)
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
	s.evaluateChannelBalances(ctx, cfg, readings)
	slog.Info("[SecurityAlert] balance sweep finished", "targets", len(targets), "readings", len(readings), "elapsed", s.now().Sub(start).Round(time.Second))
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
func (s *SecurityAlertService) refreshChannelBalances(ctx context.Context, targets []channelBalanceTarget, start time.Time) []channelBalanceReading {
	var (
		mu       sync.Mutex
		readings []channelBalanceReading
		wg       sync.WaitGroup
	)
	jobs := make(chan channelBalanceTarget)
	for i := 0; i < securityAlertBalanceSweepWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				state, err := s.balances.State(ctx, t.ID, true, false)
				if err != nil {
					continue
				}
				data := state.Snapshot.Data
				if data == nil || data.Remaining == nil || data.Unlimited {
					continue
				}
				// 只用本轮（或 5 分钟缓存内）拿到的结果，不拿过期快照判定。
				if start.Sub(time.Unix(state.Snapshot.FetchedAt, 0)) > 6*time.Minute {
					continue
				}
				mu.Lock()
				readings = append(readings, channelBalanceReading{
					AccountID: t.ID, Name: t.Name, Platform: t.Platform, Site: t.Site,
					Remaining: *data.Remaining, Unit: data.Unit, Scope: data.Scope,
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
		readings[i].Burn = balanceBurnPerHour(samples[readings[i].AccountID])
	}
	return nil
}

func (s *SecurityAlertService) evaluateChannelBalances(ctx context.Context, cfg SecurityAlertConfig, readings []channelBalanceReading) {
	subjects := make([]string, 0, len(readings))
	for _, r := range readings {
		subjects = append(subjects, "account:"+strconv.FormatInt(r.AccountID, 10))
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
	var alerts []channelBalanceReading
	var changed []change
	for i, r := range readings {
		r.Level = evaluateChannelBalanceLevel(cfg, r.Remaining, r.Unit, r.Burn)
		prev := states[subjects[i]]
		level, value, alert := nextBalanceAlertState(prev.Level, prev.Value, r.Level, r.Remaining)
		if alert {
			alerts = append(alerts, r)
		}
		if level != prev.Level || value != prev.Value {
			changed = append(changed, change{subjects[i], balanceAlertState{Level: level, Value: value}})
		}
	}
	// 先写 outbox 再记状态：记状态失败最多重复提醒一次，不会漏报。
	for _, d := range buildChannelBalanceDrafts(cfg, alerts, s.now()) {
		s.emitLogged(ctx, d)
	}
	for _, c := range changed {
		if err := s.saveAlertState(ctx, c.subject, c.state); err != nil {
			slog.Warn("[SecurityAlert] failed to save channel alert state", "subject", c.subject, "error", err)
		}
	}
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
			who := fmt.Sprintf("#%d %s", w.UserID, sanitizeSecurityText(u.Email, 60))
			if w.Note != "" {
				who += "（" + sanitizeSecurityText(w.Note, 60) + "）"
			}
			lines := []string{
				"用户：" + who,
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
