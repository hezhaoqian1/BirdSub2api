package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 上游开始因余额不足拒绝请求：比定时查余额更早、也覆盖查不到余额的渠道。
//
// sub2api / New API 类上游按在途请求预扣费用，余额还剩一些时就开始对部分请求返回
// 403/429「insufficient balance」，网关多半能切到其他渠道兜住，客户看不出来，余额接口
// 也还显示有钱。2026-11 的记录里，这类拒绝比「余额不足 / 已用完」推送早 10–50 分钟。
//
// 规则：同一钱包（余额刷新时识别；没识别出的按上游域名）5 分钟内被拒达到
// ChannelRejectThreshold 次时推送一次；连续 30 分钟没有再被拒才重新开始监控。
//
// 余额明显充足却被拒，多半是上游自己的号池或某个模型额度不足（2026-11-07 zzone gemini-4-argon
// 一天被拒 664 次，我们在 zzone 的钱包还有 $593），所以消息里带上余额和涉及的模型，让人判断该充值还是找上游。

const (
	securityAlertKindChannelReject = "channel_balance_reject"

	securityAlertRejectWindow = 5 * time.Minute
	securityAlertRejectQuiet  = 30 * time.Minute
)

// channelBalanceRejectPattern 上游「余额/额度不足」类报错（PostgreSQL 正则，大小写不敏感）。
// 只认明确的余额措辞：Gemini 的「Resource has been exhausted (e.g. check quota)」、
// 「TPM quota exhausted」是限流，不能算。
const channelBalanceRejectPattern = `insufficient[ _]+(user[ _]+|account[ _]+)?(balance|quota|credits?|funds?)|余额不足|额度不足|欠费|credit balance is too low|top up your`

func rejectAlertSubject(id int64) string { return "reject:" + strconv.FormatInt(id, 10) }

type channelRejectHit struct {
	AccountID int64
	Name      string
	Site      string
	Recent    int // 近 5 分钟被拒次数
	Visible   int // 其中客户看到报错的次数（没被切换兜住）
	Message   string
	Models    string // 被拒请求涉及的模型（逗号分隔）
	Snapshot  string
}

// checkChannelBalanceRejects 每分钟检查一次（leader）。
func (s *SecurityAlertService) checkChannelBalanceRejects(ctx context.Context, snap securityRuleSnapshot) {
	threshold := snap.cfg.ChannelRejectThreshold
	if threshold <= 0 || s.db == nil {
		return
	}
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `SELECT e.account_id, COALESCE(MAX(a.name), ''), COALESCE(MAX(a.credentials->>'base_url'), ''),
 COUNT(*) FILTER (WHERE e.created_at > $2),
 COUNT(*) FILTER (WHERE e.created_at > $2 AND e.status_code >= 400),
 (ARRAY_AGG(e.upstream_error_message ORDER BY e.created_at DESC))[1],
 COALESCE(STRING_AGG(DISTINCT NULLIF(e.model, ''), ','), ''),
 COALESCE(MAX(b.snapshot::text), '')
 FROM ops_error_logs e
 LEFT JOIN accounts a ON a.id = e.account_id
 LEFT JOIN upstream_balance_accounts b ON b.account_id = e.account_id
 WHERE e.created_at > $1 AND e.account_id IS NOT NULL AND e.upstream_error_message ~* $3
 GROUP BY e.account_id`, now.Add(-securityAlertRejectQuiet), now.Add(-securityAlertRejectWindow), channelBalanceRejectPattern)
	if err != nil {
		slog.Warn("[SecurityAlert] channel reject query failed", "error", err)
		return
	}
	hits := map[int64]channelRejectHit{}
	for rows.Next() {
		var h channelRejectHit
		var baseURL string
		if rows.Scan(&h.AccountID, &h.Name, &baseURL, &h.Recent, &h.Visible, &h.Message, &h.Models, &h.Snapshot) == nil {
			h.Site = balanceSiteHost(baseURL)
			hits[h.AccountID] = h
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		slog.Warn("[SecurityAlert] channel reject query failed", "error", err)
		return
	}

	active, err := s.loadActiveRejectStates(ctx)
	if err != nil {
		slog.Warn("[SecurityAlert] failed to load channel reject states", "error", err)
		return
	}
	groups := map[string][]channelRejectHit{}
	for _, h := range hits {
		key := s.rejectGroupKey(h.AccountID, h.Site)
		groups[key] = append(groups[key], h)
	}
	// 同一钱包 30 分钟没再被拒：静默重新开始监控。
	for id := range active {
		if _, ok := hits[id]; ok {
			continue
		}
		if head, ok := s.walletOfAccount(id); ok {
			if _, busy := groups["wallet:"+strconv.FormatInt(head, 10)]; busy {
				continue
			}
		}
		if err := s.saveAlertState(ctx, rejectAlertSubject(id), balanceAlertState{}); err != nil {
			slog.Warn("[SecurityAlert] failed to reset channel reject state", "account_id", id, "error", err)
		}
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var empty map[int64]bool // 已推过「见底 / 已用完」的账号，用到时才查
	for _, key := range keys {
		group := groups[key]
		recent, alreadyAlerted := 0, false
		for _, h := range group {
			recent += h.Recent
			alreadyAlerted = alreadyAlerted || active[h.AccountID]
		}
		var mark []int64
		switch {
		case alreadyAlerted:
			// 同一次事件里新被拒的账号也记上，避免先被拒的账号恢复后又推一次。
			for _, h := range group {
				if h.Recent > 0 && !active[h.AccountID] {
					mark = append(mark, h.AccountID)
				}
			}
		case recent >= threshold:
			if empty == nil {
				if empty, err = s.loadEmptyBalanceAccounts(ctx); err != nil {
					slog.Warn("[SecurityAlert] failed to load empty channel balances", "error", err)
					empty = map[int64]bool{}
				}
			}
			// 钱包已经推过「见底 / 已用完」：被拒是意料之中，只记状态不再推送。
			if !s.rejectGroupAlreadyEmpty(group, empty) {
				s.emitLogged(ctx, s.buildChannelRejectDraft(group, now))
			}
			for _, h := range group {
				mark = append(mark, h.AccountID)
			}
		}
		sort.Slice(mark, func(i, j int) bool { return mark[i] < mark[j] })
		for _, id := range mark {
			if err := s.saveAlertState(ctx, rejectAlertSubject(id), balanceAlertState{Level: 1, Value: float64(now.Unix())}); err != nil {
				slog.Warn("[SecurityAlert] failed to save channel reject state", "account_id", id, "error", err)
			}
		}
	}
}

// rejectGroupKey 同一钱包（余额刷新时识别）合并；没识别出的按上游域名合并。
func (s *SecurityAlertService) rejectGroupKey(id int64, site string) string {
	if head, ok := s.walletOfAccount(id); ok {
		return "wallet:" + strconv.FormatInt(head, 10)
	}
	if site == "" {
		return "account:" + strconv.FormatInt(id, 10)
	}
	return "site:" + site
}

// loadEmptyBalanceAccounts 余额状态处于「见底 / 已用完」（含旧版「低于提醒线」）的账号。
func (s *SecurityAlertService) loadEmptyBalanceAccounts(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject FROM security_alert_state WHERE subject LIKE 'account:%' AND level IN ($1, $2)`,
		channelBalanceLow, channelBalanceEmpty)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	empty := map[int64]bool{}
	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			return nil, err
		}
		if id, err := strconv.ParseInt(strings.TrimPrefix(subject, "account:"), 10, 64); err == nil {
			empty[id] = true
		}
	}
	return empty, rows.Err()
}

// rejectGroupAlreadyEmpty 组里任一账号（或它所在钱包的代表账号）已推过「见底 / 已用完」。
func (s *SecurityAlertService) rejectGroupAlreadyEmpty(group []channelRejectHit, empty map[int64]bool) bool {
	for _, h := range group {
		if empty[h.AccountID] {
			return true
		}
		if head, ok := s.walletOfAccount(h.AccountID); ok && empty[head] {
			return true
		}
	}
	return false
}

func (s *SecurityAlertService) loadActiveRejectStates(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject FROM security_alert_state WHERE subject LIKE 'reject:%' AND level > 0`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	active := map[int64]bool{}
	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			return nil, err
		}
		if id, err := strconv.ParseInt(strings.TrimPrefix(subject, "reject:"), 10, 64); err == nil {
			active[id] = true
		}
	}
	return active, rows.Err()
}

func (s *SecurityAlertService) buildChannelRejectDraft(group []channelRejectHit, now time.Time) securityAlertDraft {
	sort.Slice(group, func(i, j int) bool { return group[i].AccountID < group[j].AccountID })
	var labels, sites []string
	seenSite := map[string]bool{}
	recent, visible := 0, 0
	message := ""
	newest := 0
	var models []string
	seenModel := map[string]bool{}
	for _, h := range group {
		for _, m := range strings.Split(h.Models, ",") {
			if m = strings.TrimSpace(m); m != "" && !seenModel[m] {
				seenModel[m] = true
				models = append(models, sanitizeSecurityText(m, 60))
			}
		}
		if h.Recent > 0 {
			labels = append(labels, channelAccountLabel(h.AccountID, h.Name))
		}
		site := h.Site
		if site == "" {
			site = "-"
		}
		if h.Recent > 0 && !seenSite[site] {
			seenSite[site] = true
			sites = append(sites, sanitizeSecurityText(site, 80))
		}
		recent += h.Recent
		visible += h.Visible
		if h.Recent > newest {
			newest, message = h.Recent, h.Message
		}
	}
	sort.Strings(sites)
	sort.Strings(models)
	counts := fmt.Sprintf("近 5 分钟被上游拒绝 %d 次", recent)
	if visible > 0 {
		counts += fmt.Sprintf("（客户看到报错 %d 次，其余已切到其他渠道）", visible)
	} else {
		counts += "（都已切到其他渠道，客户暂未受影响）"
	}
	lines := []string{
		"上游：" + strings.Join(sites, "、"),
		"账号：" + joinLimited(labels, 6),
		counts,
		"上游报错：" + sanitizeSecurityText(message, 120),
	}
	if len(models) > 0 {
		lines = append(lines, "模型："+joinLimited(models, 4))
	}
	if wallet := s.freshestWalletLine(group, now); wallet != "" {
		lines = append(lines, wallet)
	}
	lines = append(lines,
		"余额不多时，上游会按在途请求预扣而提前拒绝，请尽快充值；余额明显充足时，多半是上游自己的号池或该模型额度不足，请联系上游。",
		"连续 30 分钟不再被拒后自动恢复监控。")
	return securityAlertDraft{
		DedupeKey: fmt.Sprintf("channel_balance_reject:%d:%s", group[0].AccountID, securityBucket(now, time.Minute)),
		Kind:      securityAlertKindChannelReject,
		Severity:  SecurityAlertCritical,
		Title:     "上游开始因余额不足拒绝请求",
		Lines:     lines,
	}
}

// freshestWalletLine 组内最近一次查到的上游余额。
func (s *SecurityAlertService) freshestWalletLine(group []channelRejectHit, now time.Time) string {
	var best *UpstreamBalanceSnapshot
	for _, h := range group {
		if h.Snapshot == "" {
			continue
		}
		var snap UpstreamBalanceSnapshot
		if json.Unmarshal([]byte(h.Snapshot), &snap) != nil || snap.Data == nil || snap.Data.Remaining == nil || snap.FetchedAt == 0 {
			continue
		}
		if best == nil || snap.FetchedAt > best.FetchedAt {
			cp := snap
			best = &cp
		}
	}
	if best == nil {
		return "上游余额：查不到"
	}
	age := now.Sub(time.Unix(best.FetchedAt, 0))
	ageText := "刚刚查询"
	switch {
	case age >= time.Hour:
		ageText = fmt.Sprintf("%d 小时前查询", int(age.Hours()))
	case age >= time.Minute:
		ageText = fmt.Sprintf("%d 分钟前查询", int(age.Minutes()))
	}
	return fmt.Sprintf("上游余额：%s（%s）", formatBalanceAmount(*best.Data.Remaining, best.Data.Unit), ageText)
}
