package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *SecurityAlertService) runTicker() {
	defer s.wg.Done()
	ticker := time.NewTicker(securityAlertTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.runTick()
		}
	}
}

// runTick 抢到 leader 锁的实例执行周期检查并投递 outbox。
func (s *SecurityAlertService) runTick() {
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 2*time.Second)
	release, ok := tryAcquireSingletonLeaderLock(lockCtx, s.lockCache, s.db, securityAlertLeaderLockKey, s.instanceID, securityAlertLeaderLockTTL)
	lockCancel()
	if !ok {
		return
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), securityAlertTickTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[SecurityAlert] tick panicked", "panic", r)
		}
	}()

	snap := s.snapshot(ctx)
	now := s.now()
	if snap.cfg.Enabled {
		if now.Sub(s.lastLoginCheck) >= securityAlertLoginCheckEvery {
			s.lastLoginCheck = now
			s.checkLoginFailures(ctx, snap)
			s.checkChannelQuotaErrors(ctx, snap)
			s.checkUserBalanceWatches(ctx, snap)
		}
		if now.Sub(s.lastUsageCheck) >= securityAlertUsageCheckEvery {
			s.lastUsageCheck = now
			s.checkUsageAnomalies(ctx, snap)
			s.checkChannelSpend(ctx, snap)
			s.checkSignupBurst(ctx, snap)
		}
		s.maybeStartBalanceSweep(snap)
		s.maybeDailyDigest(ctx, snap)
	}
	s.deliverPending(ctx)
	if now.Sub(s.lastCleanup) >= securityAlertCleanupEvery {
		s.lastCleanup = now
		if _, err := s.db.ExecContext(ctx, `DELETE FROM security_alert_events WHERE created_at < $1`, now.Add(-securityAlertRetention)); err != nil {
			slog.Warn("[SecurityAlert] cleanup failed", "error", err)
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM security_alert_balance_samples WHERE observed_at < $1`, now.Add(-securityAlertBalanceSampleTTL)); err != nil {
			slog.Warn("[SecurityAlert] balance sample cleanup failed", "error", err)
		}
	}
}

func securityBucket(t time.Time, size time.Duration) string {
	return strconv.FormatInt(t.Truncate(size).Unix(), 10)
}

// checkLoginFailures 统计近 15 分钟的登录失败：管理员账号被尝试、单 IP 爆破。
func (s *SecurityAlertService) checkLoginFailures(ctx context.Context, snap securityRuleSnapshot) {
	now := s.now()
	since := now.Add(-15 * time.Minute)
	if snap.cfg.AdminLoginFailThreshold > 0 && len(snap.adminEmails) > 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT LOWER(COALESCE(substring(request_body from '"email":"([^"]*)"'), '')) AS email,
 COUNT(*), string_agg(DISTINCT client_ip, ', ')
 FROM audit_logs
 WHERE created_at > $1 AND action = 'auth.login' AND status_code IN (400, 401, 403, 429)
 GROUP BY 1`, since)
		if err != nil {
			slog.Warn("[SecurityAlert] admin login failure query failed", "error", err)
		} else {
			for rows.Next() {
				var email, ips string
				var count int
				if rows.Scan(&email, &count, &ips) != nil {
					continue
				}
				if !snap.isAdminEmail(email) || count < snap.cfg.AdminLoginFailThreshold {
					continue
				}
				s.emitLogged(ctx, securityAlertDraft{
					DedupeKey: "login_fail_admin:" + email + ":" + securityBucket(now, 30*time.Minute),
					Kind:      "admin_login_failures",
					Severity:  SecurityAlertCritical,
					Title:     "管理员账号密码正在被尝试",
					Lines: []string{
						"账号：" + sanitizeSecurityText(email, 80),
						fmt.Sprintf("15 分钟内登录失败 %d 次", count),
						"来源 IP：" + sanitizeSecurityText(ips, 200),
						"如果不是你本人输错密码，建议立即修改密码并开启两步验证。",
					},
				})
			}
			_ = rows.Close()
		}
	}

	if snap.cfg.IPLoginFailThreshold > 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT client_ip, COUNT(*),
 COUNT(DISTINCT substring(request_body from '"email":"([^"]*)"'))
 FROM audit_logs
 WHERE created_at > $1 AND action IN ('auth.login', 'auth.login.2fa')
   AND status_code IN (400, 401, 403, 429) AND client_ip <> ''
 GROUP BY client_ip HAVING COUNT(*) >= $2
 ORDER BY 2 DESC LIMIT 20`, since, snap.cfg.IPLoginFailThreshold)
		if err != nil {
			slog.Warn("[SecurityAlert] ip login failure query failed", "error", err)
			return
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var ip string
			var count, accounts int
			if rows.Scan(&ip, &count, &accounts) != nil {
				continue
			}
			s.emitLogged(ctx, securityAlertDraft{
				DedupeKey: "login_fail_ip:" + ip + ":" + securityBucket(now, time.Hour),
				Kind:      "ip_login_failures",
				Severity:  SecurityAlertWarning,
				Title:     "登录接口疑似被爆破",
				Lines: []string{
					"IP：" + formatSecurityIP(ip),
					securityIPLookupLine(ip),
					fmt.Sprintf("15 分钟内登录失败 %d 次，涉及 %d 个账号", count, accounts),
				},
			})
		}
	}
}

// checkUsageAnomalies 用户消费突增、API Key 被大量不同 IP 使用（疑似泄露/转卖）。
func (s *SecurityAlertService) checkUsageAnomalies(ctx context.Context, snap securityRuleSnapshot) {
	spendOn := snap.cfg.SpendSpikeFloor > 0 && snap.cfg.SpendSpikeMultiplier > 0
	keyOn := snap.cfg.KeyIPSpreadThreshold > 0
	if !spendOn && !keyOn || !s.ensureUsageBaseline(ctx) {
		return
	}
	now := s.now()
	since := now.Add(-time.Hour)

	if spendOn {
		rows, err := s.db.QueryContext(ctx, `SELECT u.user_id, COALESCE(us.email, ''), COALESCE(SUM(u.actual_cost), 0),
 COUNT(*), COUNT(DISTINCT u.ip_address)
 FROM usage_logs u LEFT JOIN users us ON us.id = u.user_id
 WHERE u.created_at > $1
 GROUP BY 1, 2`, since)
		if err != nil {
			slog.Warn("[SecurityAlert] spend query failed", "error", err)
		} else {
			for rows.Next() {
				var userID int64
				var email string
				var cost float64
				var requests, ips int
				if rows.Scan(&userID, &email, &cost, &requests, &ips) != nil {
					continue
				}
				baseline := s.userSpendBaseline[userID]
				if cost < snap.cfg.SpendSpikeFloor || (baseline > 0 && cost < baseline*snap.cfg.SpendSpikeMultiplier) {
					continue
				}
				baselineText := "过去 7 天没有消费记录"
				if baseline > 0 {
					baselineText = fmt.Sprintf("过去 7 天单小时最高 $%.2f（本小时是它的 %.1f 倍）", baseline, cost/baseline)
				}
				s.emitLogged(ctx, securityAlertDraft{
					DedupeKey: "spend_spike:" + strconv.FormatInt(userID, 10) + ":" + securityBucket(now, 3*time.Hour),
					Kind:      "spend_spike",
					Severity:  SecurityAlertWarning,
					Title:     "用户消费异常飙升",
					Lines: []string{
						fmt.Sprintf("用户：#%d %s", userID, sanitizeSecurityText(email, 80)),
						fmt.Sprintf("近 1 小时消费 $%.2f（%d 次请求，%d 个 IP）", cost, requests, ips),
						baselineText,
						"如非预期，请核对该用户的余额来源，必要时先禁用账号。",
					},
				})
			}
			_ = rows.Close()
		}
	}

	if keyOn {
		rows, err := s.db.QueryContext(ctx, `SELECT u.api_key_id, u.user_id, COALESCE(us.email, ''),
 COUNT(DISTINCT u.ip_address), COUNT(*)
 FROM usage_logs u LEFT JOIN users us ON us.id = u.user_id
 WHERE u.created_at > $1 AND u.api_key_id IS NOT NULL
 GROUP BY 1, 2, 3
 HAVING COUNT(DISTINCT u.ip_address) >= $2`, since, snap.cfg.KeyIPSpreadThreshold)
		if err != nil {
			slog.Warn("[SecurityAlert] key ip spread query failed", "error", err)
			return
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var keyID, userID int64
			var email string
			var ips, requests int
			if rows.Scan(&keyID, &userID, &email, &ips, &requests) != nil {
				continue
			}
			baseline := s.keyIPBaseline[keyID]
			if baseline > 0 && ips < baseline*2 {
				continue
			}
			s.emitLogged(ctx, securityAlertDraft{
				DedupeKey: "key_ip_spread:" + strconv.FormatInt(keyID, 10) + ":" + securityBucket(now, 6*time.Hour),
				Kind:      "key_ip_spread",
				Severity:  SecurityAlertWarning,
				Title:     "API Key 疑似泄露（大量不同 IP 在用）",
				Lines: []string{
					fmt.Sprintf("Key ID：%d，用户：#%d %s", keyID, userID, sanitizeSecurityText(email, 80)),
					fmt.Sprintf("近 1 小时 %d 个不同 IP，%d 次请求", ips, requests),
					fmt.Sprintf("过去 7 天单小时最多 %d 个 IP", baseline),
				},
			})
		}
	}
}

// ensureUsageBaseline 每小时刷新一次用户、API Key、渠道的 7 天用量基线。
func (s *SecurityAlertService) ensureUsageBaseline(ctx context.Context) bool {
	now := s.now()
	if !s.usageBaselineAt.IsZero() && now.Sub(s.usageBaselineAt) < securityAlertUsageBaselineTTL {
		return true
	}
	if err := s.loadUsageBaseline(ctx); err != nil {
		slog.Warn("[SecurityAlert] usage baseline query failed", "error", err)
		return false
	}
	s.usageBaselineAt = now
	return true
}

func (s *SecurityAlertService) loadUsageBaseline(ctx context.Context) error {
	now := s.now()
	from, to := now.Add(-7*24*time.Hour), now.Add(-time.Hour)
	spend := map[int64]float64{}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, MAX(c) FROM (
 SELECT user_id, date_trunc('hour', created_at) h, SUM(actual_cost) c
 FROM usage_logs WHERE created_at > $1 AND created_at <= $2 GROUP BY 1, 2) t
 GROUP BY 1`, from, to)
	if err != nil {
		return err
	}
	for rows.Next() {
		var userID int64
		var c sql.NullFloat64
		if rows.Scan(&userID, &c) == nil && c.Valid {
			spend[userID] = c.Float64
		}
	}
	_ = rows.Close()

	keyIPs := map[int64]int{}
	rows, err = s.db.QueryContext(ctx, `SELECT api_key_id, MAX(n) FROM (
 SELECT api_key_id, date_trunc('hour', created_at) h, COUNT(DISTINCT ip_address) n
 FROM usage_logs WHERE created_at > $1 AND created_at <= $2 AND api_key_id IS NOT NULL GROUP BY 1, 2) t
 GROUP BY 1`, from, to)
	if err != nil {
		return err
	}
	for rows.Next() {
		var keyID int64
		var n int
		if rows.Scan(&keyID, &n) == nil {
			keyIPs[keyID] = n
		}
	}
	_ = rows.Close()

	// 渠道成本用 P95 而不是最大值：大渠道偶尔一小时冲高很常见，用最大值会把真正的异常也盖住。
	channelSpend := map[int64]float64{}
	rows, err = s.db.QueryContext(ctx, `SELECT account_id, percentile_cont(0.95) WITHIN GROUP (ORDER BY c) FROM (
 SELECT u.account_id, date_trunc('hour', u.created_at) h, SUM(`+securityAccountCostExpr+`) c
 FROM usage_logs u WHERE u.created_at > $1 AND u.created_at <= $2 GROUP BY 1, 2) t
 GROUP BY 1`, from, to)
	if err != nil {
		return err
	}
	for rows.Next() {
		var accountID int64
		var c sql.NullFloat64
		if rows.Scan(&accountID, &c) == nil && c.Valid {
			channelSpend[accountID] = c.Float64
		}
	}
	_ = rows.Close()

	s.userSpendBaseline = spend
	s.keyIPBaseline = keyIPs
	s.channelSpendBaseline = channelSpend
	return nil
}

// checkSignupBurst 近 1 小时注册量突增（批量注册薅羊毛/刷邀请奖励）。
func (s *SecurityAlertService) checkSignupBurst(ctx context.Context, snap securityRuleSnapshot) {
	if snap.cfg.SignupBurstThreshold <= 0 {
		return
	}
	now := s.now()
	var count, ips int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT client_ip) FROM audit_logs
 WHERE created_at > $1 AND action = 'auth.register' AND status_code BETWEEN 200 AND 299`, now.Add(-time.Hour)).Scan(&count, &ips)
	if err != nil {
		slog.Warn("[SecurityAlert] signup burst query failed", "error", err)
		return
	}
	if count < snap.cfg.SignupBurstThreshold {
		return
	}
	s.emitLogged(ctx, securityAlertDraft{
		DedupeKey: "signup_burst:" + securityBucket(now, time.Hour),
		Kind:      "signup_burst",
		Severity:  SecurityAlertWarning,
		Title:     "注册量异常",
		Lines: []string{
			fmt.Sprintf("近 1 小时新注册 %d 个账号，来自 %d 个 IP", count, ips),
			"如非推广活动带来的，请检查是否有人批量注册刷奖励。",
		},
	})
}

// maybeDailyDigest 每天固定时间发一条日报，同时充当告警通道的心跳。
func (s *SecurityAlertService) maybeDailyDigest(ctx context.Context, snap securityRuleSnapshot) {
	hour := snap.cfg.DailyDigestHour
	if hour < 0 || hour > 23 {
		return
	}
	now := s.now().In(s.loc)
	if now.Hour() != hour {
		return
	}
	dedupeKey := "digest:" + now.Format("2006-01-02")
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM security_alert_events WHERE dedupe_key = $1)`, dedupeKey).Scan(&exists); err != nil || exists {
		return
	}
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc)
	start := end.AddDate(0, 0, -1)
	lines, err := s.buildDailyDigest(ctx, snap, start, end)
	if err != nil {
		slog.Warn("[SecurityAlert] daily digest failed", "error", err)
		return
	}
	s.emitLogged(ctx, securityAlertDraft{
		DedupeKey: dedupeKey,
		Kind:      securityAlertKindDigest,
		Severity:  SecurityAlertInfo,
		Title:     "每日安全日报（" + start.Format("01-02") + "）",
		Lines:     lines,
	})
}

func (s *SecurityAlertService) buildDailyDigest(ctx context.Context, snap securityRuleSnapshot, start, end time.Time) ([]string, error) {
	// 管理员登录成功/失败（按邮箱归并）。
	adminLogins, adminLoginFails, totalLoginFails := 0, 0, 0
	rows, err := s.db.QueryContext(ctx, `SELECT LOWER(COALESCE(substring(request_body from '"email":"([^"]*)"'), '')),
 status_code BETWEEN 200 AND 299, COUNT(*)
 FROM audit_logs WHERE created_at >= $1 AND created_at < $2 AND action = 'auth.login'
 GROUP BY 1, 2`, start, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var email string
		var success bool
		var count int
		if rows.Scan(&email, &success, &count) != nil {
			continue
		}
		admin := snap.isAdminEmail(email)
		switch {
		case success && admin:
			adminLogins += count
		case !success && admin:
			adminLoginFails += count
			totalLoginFails += count
		case !success:
			totalLoginFails += count
		}
	}
	_ = rows.Close()

	// 管理员使用过的网段。
	prefixSet := map[string]struct{}{}
	adminOps := 0
	rows, err = s.db.QueryContext(ctx, `SELECT client_ip, COUNT(*) FROM audit_logs
 WHERE created_at >= $1 AND created_at < $2 AND actor_role = 'admin' GROUP BY 1`, start, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ip string
		var count int
		if rows.Scan(&ip, &count) == nil {
			adminOps += count
			if p := securityNetworkPrefix(ip); p != "" {
				prefixSet[p] = struct{}{}
			}
		}
	}
	_ = rows.Close()
	prefixes := make([]string, 0, len(prefixSet))
	for p := range prefixSet {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	prefixText := "无"
	if len(prefixes) > 0 {
		shown := prefixes
		if len(shown) > 8 {
			shown = shown[:8]
		}
		prefixText = strings.Join(shown, "、")
		if len(prefixes) > len(shown) {
			prefixText += fmt.Sprintf(" 等 %d 个", len(prefixes))
		}
	}

	var balanceOps, signups int
	if err := s.db.QueryRowContext(ctx, `SELECT
 COUNT(*) FILTER (WHERE action = 'admin.users.balance.create' AND status_code BETWEEN 200 AND 299),
 COUNT(*) FILTER (WHERE action = 'auth.register' AND status_code BETWEEN 200 AND 299)
 FROM audit_logs WHERE created_at >= $1 AND created_at < $2`, start, end).Scan(&balanceOps, &signups); err != nil {
		return nil, err
	}

	var critical, warning int
	if err := s.db.QueryRowContext(ctx, `SELECT
 COUNT(*) FILTER (WHERE severity = 'critical'), COUNT(*) FILTER (WHERE severity = 'warning')
 FROM security_alert_events WHERE created_at >= $1 AND created_at < $2 AND kind <> $3`,
		start, end, securityAlertKindDigest).Scan(&critical, &warning); err != nil {
		return nil, err
	}

	var spend float64
	var requests int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(actual_cost), 0), COUNT(*) FROM usage_logs
 WHERE created_at >= $1 AND created_at < $2`, start, end).Scan(&spend, &requests); err != nil {
		return nil, err
	}

	lines := []string{
		fmt.Sprintf("告警：严重 %d 条，注意 %d 条", critical, warning),
		fmt.Sprintf("管理员登录成功 %d 次，失败 %d 次；全站登录失败 %d 次", adminLogins, adminLoginFails, totalLoginFails),
		fmt.Sprintf("后台操作 %d 次，来自网段：%s", adminOps, prefixText),
		fmt.Sprintf("后台余额调整 %d 次；新注册 %d 人", balanceOps, signups),
		fmt.Sprintf("全站消费 $%.2f（%d 次请求）", spend, requests),
	}
	lines = append(lines, s.buildBalanceDigestLines(ctx, snap.cfg, start, end)...)
	return append(lines, "这条日报每天都会发，哪天没收到说明告警通道出了问题。"), nil
}

func (s *SecurityAlertService) emitLogged(ctx context.Context, d securityAlertDraft) {
	if err := s.emit(ctx, d); err != nil {
		slog.Warn("[SecurityAlert] failed to record alert", "kind", d.Kind, "error", err)
	}
}
