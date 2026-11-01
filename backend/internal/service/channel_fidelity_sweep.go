package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 渠道保真体检的调度、落库与告警。挂在安全告警服务上：抢到 leader 锁的实例按配置的间隔
// 发起一轮体检（跨实例由 claimSweep 协调），不通过的目标隔 RecheckSeconds 复测，
// 复测没有通过（仍不通过，或请求出错/超时无法证明已恢复）才写入告警 outbox；security_alert_state 里的 fidelity:<账号>:<上游模型>
// 记录保证一轮故障只推一次告警、一次恢复。停止调度的账号照常体检，但只记录不告警。

const (
	securityAlertKindChannelFidelity = "channel_fidelity"
	fidelitySweepSubject             = "sweep:channel_fidelity"
	fidelitySweepTimeout             = 15 * time.Minute
	fidelityRetention                = 7 * 24 * time.Hour
	fidelityHistoryWindow            = 24 * time.Hour
	fidelityProbeConcurrency         = 3
	fidelityStateTimeout             = 10 * time.Second
)

// SettingKeyChannelFidelityConfig 保真体检配置（JSON）。
const SettingKeyChannelFidelityConfig = "channel_fidelity_config"

func fidelityAlertSubject(accountID int64, upstreamModel string) string {
	subject := "fidelity:" + strconv.FormatInt(accountID, 10) + ":" + upstreamModel
	if len(subject) > 100 {
		subject = subject[:100]
	}
	return subject
}

func fidelityKey(accountID int64, upstreamModel string) string {
	return strconv.FormatInt(accountID, 10) + "\x00" + upstreamModel
}

// SetChannelFidelityProber 注入体检请求器；未注入时体检功能不可用。
func (s *SecurityAlertService) SetChannelFidelityProber(p *channelFidelityProber) {
	if s == nil || p == nil {
		return
	}
	s.fidelity = p
}

// ---- 配置 ----

func (s *SecurityAlertService) loadFidelityConfig(ctx context.Context) (ChannelFidelityConfig, error) {
	cfg := DefaultChannelFidelityConfig()
	if s.settings == nil || s.settings.settingRepo == nil {
		return cfg, nil
	}
	vals, err := s.settings.settingRepo.GetMultiple(ctx, []string{SettingKeyChannelFidelityConfig})
	if err != nil {
		return cfg, err
	}
	if raw := strings.TrimSpace(vals[SettingKeyChannelFidelityConfig]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return DefaultChannelFidelityConfig(), fmt.Errorf("decode channel fidelity config: %w", err)
		}
	}
	cfg.normalize()
	return cfg, nil
}

// GetFidelityConfig 读取保真体检配置。
func (s *SecurityAlertService) GetFidelityConfig(ctx context.Context) (ChannelFidelityConfig, error) {
	return s.loadFidelityConfig(ctx)
}

// UpdateFidelityConfig 把部分配置合并到当前配置上并保存。
func (s *SecurityAlertService) UpdateFidelityConfig(ctx context.Context, patch json.RawMessage) (ChannelFidelityConfig, error) {
	cfg, err := s.loadFidelityConfig(ctx)
	if err != nil {
		return cfg, err
	}
	if len(patch) > 0 && string(patch) != "null" {
		if err := json.Unmarshal(patch, &cfg); err != nil {
			return cfg, fmt.Errorf("invalid config: %w", err)
		}
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if s.settings == nil || s.settings.settingRepo == nil {
		return cfg, errors.New("settings are not available")
	}
	if err := s.settings.settingRepo.Set(ctx, SettingKeyChannelFidelityConfig, string(raw)); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// fidelityNotifyEnabled 告警总开关和保真告警开关都打开时才推送。
func (s *SecurityAlertService) fidelityNotifyEnabled(ctx context.Context) bool {
	s.refreshConfig(ctx, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Enabled && s.cfg.ChannelFidelityNotify
}

// ---- 调度 ----

// maybeStartFidelitySweep 到期时在后台跑一轮体检（不阻塞 tick；跨实例由 claimSweep 协调）。
func (s *SecurityAlertService) maybeStartFidelitySweep(ctx context.Context) {
	if s.fidelity == nil {
		return
	}
	now := s.now()
	if now.Sub(s.lastFidelitySweepCheck) < time.Minute || s.fidelitySweepInProgress.Load() {
		return
	}
	s.lastFidelitySweepCheck = now
	cfg, err := s.loadFidelityConfig(ctx)
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to load config", "error", err)
		return
	}
	if !cfg.Enabled {
		return
	}
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	claimed, err := s.claimSweep(claimCtx, fidelitySweepSubject, time.Duration(cfg.IntervalMinutes)*time.Minute)
	cancel()
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to claim sweep", "error", err)
		return
	}
	if claimed {
		s.startFidelitySweep(cfg)
	}
}

// RunFidelitySweepNow 立即在后台跑一轮体检（管理端手动触发，不受定时开关影响）。
// 本实例已有一轮在跑时返回 false。
func (s *SecurityAlertService) RunFidelitySweepNow(ctx context.Context) (bool, error) {
	if s.fidelity == nil {
		return false, errors.New("channel fidelity probe is not available")
	}
	cfg, err := s.loadFidelityConfig(ctx)
	if err != nil {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO security_alert_state(subject, updated_at) VALUES ($1, NOW())
 ON CONFLICT (subject) DO UPDATE SET updated_at = NOW()`, fidelitySweepSubject); err != nil {
		slog.Warn("[ChannelFidelity] failed to mark manual sweep", "error", err)
	}
	return s.startFidelitySweep(cfg), nil
}

func (s *SecurityAlertService) startFidelitySweep(cfg ChannelFidelityConfig) bool {
	if !s.fidelitySweepInProgress.CompareAndSwap(false, true) {
		return false
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.fidelitySweepInProgress.Store(false)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[ChannelFidelity] sweep panicked", "panic", r)
			}
		}()
		s.runFidelitySweep(cfg)
	}()
	return true
}

func (s *SecurityAlertService) runFidelitySweep(cfg ChannelFidelityConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), fidelitySweepTimeout)
	defer cancel()
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	targets, err := s.fidelity.allTargets(ctx, cfg)
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to list targets", "error", err)
		return
	}
	if len(targets) == 0 {
		return
	}
	s.attachFidelityCanaries(ctx, targets)
	baselines := s.loadFidelityBaselines(ctx)
	first := s.probeFidelityTargets(ctx, targets, baselines, cfg.PromptTokenTolerance)

	var retry []int
	for i := range targets {
		r := &first[i]
		s.recordFidelityCheck(ctx, targets[i], r, baselines)
		switch r.Status {
		case fidelityStatusPass:
			s.fidelityRecovered(ctx, targets[i], *r)
		case fidelityStatusFail:
			if cfg.RecheckSeconds <= 0 {
				s.fidelityAlert(ctx, s.autoPauseConfirmedGemini(ctx, cfg, targets[i], *r), nil, *r)
			} else {
				retry = append(retry, i)
			}
		}
	}
	if len(retry) == 0 {
		return
	}
	select {
	case <-time.After(time.Duration(cfg.RecheckSeconds) * time.Second):
	case <-ctx.Done():
		return
	}
	retryTargets := make([]fidelityTarget, 0, len(retry))
	for _, i := range retry {
		retryTargets = append(retryTargets, targets[i])
	}
	second := s.probeFidelityTargets(ctx, retryTargets, baselines, cfg.PromptTokenTolerance)
	for j, i := range retry {
		r := &second[j]
		r.IsRecheck = true
		s.recordFidelityCheck(ctx, targets[i], r, baselines)
		if r.Status == fidelityStatusPass {
			s.fidelityRecovered(ctx, targets[i], *r)
			continue
		}
		// 复测不通过，或复测请求出错/超时（强制思考的上游常常拖到超时）：
		// 首次的不通过没有被推翻，按确认处理。自动暂停只在复测真的不通过时做。
		s.fidelityAlert(ctx, s.autoPauseConfirmedGemini(ctx, cfg, targets[i], *r), &first[i], *r)
	}
}

// probeFidelityTargets 并发（最多 3 个）体检，结果与 targets 一一对应。
func (s *SecurityAlertService) probeFidelityTargets(ctx context.Context, targets []fidelityTarget, baselines map[string]int, tolerance int) []ChannelFidelityCheck {
	results := make([]ChannelFidelityCheck, len(targets))
	sem := make(chan struct{}, fidelityProbeConcurrency)
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var baseline *int
			if v, ok := baselines[fidelityKey(targets[i].Account.ID, targets[i].UpstreamModel)]; ok {
				b := v
				baseline = &b
			}
			results[i] = s.fidelity.probe(ctx, targets[i], baseline, tolerance)
		}(i)
	}
	wg.Wait()
	return results
}

// ---- 落库 ----

const fidelityCheckColumns = `id, account_id, model, upstream_model, checked_at, status, is_recheck,
 system_check, thinking_check, json_check, answer_check, prompt_check,
 prompt_tokens, baseline_prompt_tokens, completion_tokens, reasoning_tokens, reasoning_chars,
 latency_ms, http_status, message, response_excerpt,
 probe_kind, signature_check, media_check, canary_check, meta_check, signature_prefix`

func scanFidelityCheck(scan func(dest ...any) error) (ChannelFidelityCheck, error) {
	var c ChannelFidelityCheck
	var prompt, baseline, completion, reasoning, latency, status sql.NullInt64
	err := scan(&c.ID, &c.AccountID, &c.Model, &c.UpstreamModel, &c.CheckedAt, &c.Status, &c.IsRecheck,
		&c.SystemCheck, &c.ThinkingCheck, &c.JSONCheck, &c.AnswerCheck, &c.PromptCheck,
		&prompt, &baseline, &completion, &reasoning, &c.ReasoningChars,
		&latency, &status, &c.Message, &c.ResponseExcerpt,
		&c.ProbeKind, &c.SignatureCheck, &c.MediaCheck, &c.CanaryCheck, &c.MetaCheck, &c.SignaturePrefix)
	c.PromptTokens = nullIntPtr(prompt)
	c.BaselinePromptTokens = nullIntPtr(baseline)
	c.CompletionTokens = nullIntPtr(completion)
	c.ReasoningTokens = nullIntPtr(reasoning)
	c.LatencyMs = nullIntPtr(latency)
	c.HTTPStatus = nullIntPtr(status)
	return c, err
}

func nullIntPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func intPtrArg(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// recordFidelityCheck 写入一条体检记录；全部通过且还没有基线时把这次的 prompt_tokens 记为基线。
func (s *SecurityAlertService) recordFidelityCheck(ctx context.Context, t fidelityTarget, r *ChannelFidelityCheck, baselines map[string]int) {
	if s.db == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fidelityStateTimeout)
	defer cancel()
	kind := r.ProbeKind
	if kind == "" {
		kind = t.kind()
	}
	skipIfEmpty := func(v string) string {
		if v == "" {
			return fidelityCheckSkip
		}
		return v
	}
	err := s.db.QueryRowContext(writeCtx, `INSERT INTO channel_fidelity_checks(account_id, model, upstream_model, checked_at, status, is_recheck,
 system_check, thinking_check, json_check, answer_check, prompt_check,
 prompt_tokens, baseline_prompt_tokens, completion_tokens, reasoning_tokens, reasoning_chars,
 latency_ms, http_status, message, response_excerpt,
 probe_kind, signature_check, media_check, canary_check, meta_check, signature_prefix)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26) RETURNING id`,
		t.Account.ID, t.Model, t.UpstreamModel, r.CheckedAt, r.Status, r.IsRecheck,
		skipIfEmpty(r.SystemCheck), skipIfEmpty(r.ThinkingCheck), skipIfEmpty(r.JSONCheck), skipIfEmpty(r.AnswerCheck), skipIfEmpty(r.PromptCheck),
		intPtrArg(r.PromptTokens), intPtrArg(r.BaselinePromptTokens), intPtrArg(r.CompletionTokens),
		intPtrArg(r.ReasoningTokens), r.ReasoningChars, intPtrArg(r.LatencyMs), intPtrArg(r.HTTPStatus),
		r.Message, r.ResponseExcerpt,
		kind, skipIfEmpty(r.SignatureCheck), skipIfEmpty(r.MediaCheck), skipIfEmpty(r.CanaryCheck), skipIfEmpty(r.MetaCheck),
		truncateRunes(r.SignaturePrefix, 16)).Scan(&r.ID)
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to record check", "account_id", t.Account.ID, "model", t.UpstreamModel, "error", err)
	}
	key := fidelityKey(t.Account.ID, t.UpstreamModel)
	if kind == fidelityKindGeminiVT && r.Status == fidelityStatusPass && r.PromptTokens != nil && r.CanarySignature != "" {
		// Gemini：每次全部通过都换上新签名做下一轮的金丝雀；prompt 基线只在第一次写入。
		if _, err := s.db.ExecContext(writeCtx, `INSERT INTO channel_fidelity_baselines(account_id, upstream_model, prompt_tokens, canary_signature, updated_at)
 VALUES ($1, $2, $3, $4, NOW()) ON CONFLICT (account_id, upstream_model)
 DO UPDATE SET canary_signature = EXCLUDED.canary_signature, updated_at = NOW()`,
			t.Account.ID, t.UpstreamModel, *r.PromptTokens, r.CanarySignature); err != nil {
			slog.Warn("[ChannelFidelity] failed to save canary", "account_id", t.Account.ID, "error", err)
			return
		}
		if _, has := baselines[key]; !has {
			baselines[key] = *r.PromptTokens
		}
		return
	}
	if _, has := baselines[key]; has || r.Status != fidelityStatusPass || r.PromptTokens == nil {
		return
	}
	if _, err := s.db.ExecContext(writeCtx, `INSERT INTO channel_fidelity_baselines(account_id, upstream_model, prompt_tokens, updated_at)
 VALUES ($1, $2, $3, NOW()) ON CONFLICT (account_id, upstream_model) DO NOTHING`,
		t.Account.ID, t.UpstreamModel, *r.PromptTokens); err != nil {
		slog.Warn("[ChannelFidelity] failed to save baseline", "account_id", t.Account.ID, "error", err)
		return
	}
	baselines[key] = *r.PromptTokens
}

func (s *SecurityAlertService) loadFidelityBaselines(ctx context.Context) map[string]int {
	out := map[string]int{}
	if s.db == nil {
		return out
	}
	rows, err := s.db.QueryContext(ctx, `SELECT account_id, upstream_model, prompt_tokens FROM channel_fidelity_baselines`)
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to load baselines", "error", err)
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var model string
		var tokens int
		if rows.Scan(&id, &model, &tokens) == nil {
			out[fidelityKey(id, model)] = tokens
		}
	}
	return out
}

// attachFidelityCanaries 给 Gemini VT 目标带上上一轮通过时留下的签名。
func (s *SecurityAlertService) attachFidelityCanaries(ctx context.Context, targets []fidelityTarget) {
	if s.db == nil {
		return
	}
	hasGemini := false
	for _, t := range targets {
		if t.kind() == fidelityKindGeminiVT {
			hasGemini = true
			break
		}
	}
	if !hasGemini {
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT account_id, upstream_model, canary_signature FROM channel_fidelity_baselines
 WHERE canary_signature IS NOT NULL AND canary_signature <> ''`)
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to load canaries", "error", err)
		return
	}
	defer func() { _ = rows.Close() }()
	canaries := map[string]string{}
	for rows.Next() {
		var id int64
		var model, sig string
		if rows.Scan(&id, &model, &sig) == nil {
			canaries[fidelityKey(id, model)] = sig
		}
	}
	for i := range targets {
		if targets[i].kind() == fidelityKindGeminiVT {
			targets[i].Canary = canaries[fidelityKey(targets[i].Account.ID, targets[i].UpstreamModel)]
		}
	}
}

// ResetFidelityBaseline 删除基线（Gemini 的金丝雀签名一并删除），下次全部通过时重新记录。
func (s *SecurityAlertService) ResetFidelityBaseline(ctx context.Context, accountID int64, upstreamModel string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM channel_fidelity_baselines WHERE account_id = $1 AND upstream_model = $2`,
		accountID, strings.TrimSpace(upstreamModel))
	return err
}

// ---- 告警 ----

// fidelityAlert 复测确认不通过后抢这轮故障的告警记录并写入 outbox；记录已存在说明已经推送过。
func (s *SecurityAlertService) fidelityAlert(ctx context.Context, t fidelityTarget, first *ChannelFidelityCheck, last ChannelFidelityCheck) {
	if (!t.Account.Schedulable && !t.autoPaused) || !s.fidelityNotifyEnabled(ctx) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fidelityStateTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to begin alert tx", "error", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var subject string
	err = tx.QueryRowContext(ctx, `INSERT INTO security_alert_state(subject, level, value, updated_at)
 VALUES ($1, 1, $2, NOW()) ON CONFLICT (subject) DO NOTHING RETURNING subject`,
		fidelityAlertSubject(t.Account.ID, t.UpstreamModel), float64(last.CheckedAt.Unix())).Scan(&subject)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to claim alert state", "error", err)
		return
	}
	if err := emitSecurityAlert(ctx, tx, fidelityAlertDraft(t, first, last, s.loc)); err != nil {
		slog.Warn("[ChannelFidelity] failed to record alert", "error", err)
		return
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("[ChannelFidelity] failed to commit alert", "error", err)
	}
}

// autoPauseConfirmedGemini Gemini VT 体检复测确认不通过时按配置自动暂停账号调度。
func (s *SecurityAlertService) autoPauseConfirmedGemini(ctx context.Context, cfg ChannelFidelityConfig, t fidelityTarget, last ChannelFidelityCheck) fidelityTarget {
	if t.kind() != fidelityKindGeminiVT || !cfg.GeminiAutoPause || last.Status != fidelityStatusFail || !t.Account.Schedulable {
		return t
	}
	t.autoPaused = s.pauseGeminiVTAccount(ctx, t.Account)
	return t
}

func fidelityModelLabel(t fidelityTarget) string {
	label := sanitizeSecurityText(t.Model, 100)
	if t.UpstreamModel != "" && t.UpstreamModel != t.Model {
		label += "（上游 " + sanitizeSecurityText(t.UpstreamModel, 100) + "）"
	}
	return label
}

func fidelityAlertDraft(t fidelityTarget, first *ChannelFidelityCheck, last ChannelFidelityCheck, loc *time.Location) securityAlertDraft {
	problems := last.failures()
	if len(problems) == 0 && first != nil {
		problems = first.failures()
	}
	if len(problems) == 0 {
		problems = []string{sanitizeSecurityText(last.Message, 200)}
	}
	recheckErrored := first != nil && last.Status == fidelityStatusError
	lines := []string{
		"账号：" + channelMonitorLabel(t.Account.ID, t.Account.Name),
		"模型：" + fidelityModelLabel(t),
		"异常：" + strings.Join(problems, "；"),
	}
	switch {
	case recheckErrored:
		lines = append(lines, "首次体检 "+formatSecurityTime(first.CheckedAt, loc)+" 不通过；"+
			formatSecurityTime(last.CheckedAt, loc)+" 复测请求出错（"+sanitizeSecurityText(last.Message, 80)+"），未能确认恢复")
	case first != nil:
		lines = append(lines, "已复测确认："+formatSecurityTime(first.CheckedAt, loc)+"、"+
			formatSecurityTime(last.CheckedAt, loc)+" 两次都不通过")
	default:
		lines = append(lines, "检测于 "+formatSecurityTime(last.CheckedAt, loc))
	}
	if t.kind() == fidelityKindGeminiVT {
		lines = append(lines, "判断：疑似混用了 AI Studio 等非 Vertex 资源（这类资源读不了 gs:// 文件、会忽略视频直接按文字回答）。")
		if t.autoPaused {
			lines = append(lines, "处理：已自动暂停该账号调度。确认上游修好后在账号管理里重新开启。")
		} else {
			lines = append(lines, "处理：确认后暂停该账号调度并找上游核实；恢复后会再通知。")
		}
	} else {
		lines = append(lines, "处理：确认后可在该账号的模型映射里去掉这个模型，或暂停调度；恢复后会再通知。")
	}
	if last.PromptCheck == fidelityCheckFail {
		lines = append(lines, "如果是上游正常调整了模板，可在「渠道监控 → 保真体检」重置基线。")
	}
	return securityAlertDraft{
		Kind:     securityAlertKindChannelFidelity,
		Severity: SecurityAlertCritical,
		Title:    "渠道保真异常",
		Lines:    lines,
	}
}

// fidelityRecovered 删掉这轮故障的告警记录并推送恢复；没有记录时什么都不做。告警关闭时只清记录不推送。
func (s *SecurityAlertService) fidelityRecovered(ctx context.Context, t fidelityTarget, r ChannelFidelityCheck) {
	notify := s.fidelityNotifyEnabled(ctx)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fidelityStateTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to begin recovery tx", "error", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var alertedUnix float64
	err = tx.QueryRowContext(ctx, `DELETE FROM security_alert_state WHERE subject = $1 RETURNING value`,
		fidelityAlertSubject(t.Account.ID, t.UpstreamModel)).Scan(&alertedUnix)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("[ChannelFidelity] failed to clear alert state", "error", err)
		return
	}
	if notify {
		lines := []string{
			"账号：" + channelMonitorLabel(t.Account.ID, t.Account.Name),
			"模型：" + fidelityModelLabel(t),
			"当前状态：全部检查通过，检测于 " + formatSecurityTime(r.CheckedAt, s.loc),
		}
		if alertedUnix > 0 {
			alertedAt := time.Unix(int64(alertedUnix), 0)
			lines = append(lines, "告警时间："+formatSecurityTime(alertedAt, s.loc)+
				"，距今约 "+formatAlertDuration(r.CheckedAt.Sub(alertedAt)))
		}
		if err := emitSecurityAlert(ctx, tx, securityAlertDraft{
			Kind:     securityAlertKindChannelFidelity,
			Severity: SecurityAlertInfo,
			Title:    "渠道保真已恢复",
			Lines:    lines,
		}); err != nil {
			slog.Warn("[ChannelFidelity] failed to record recovery", "error", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("[ChannelFidelity] failed to commit recovery", "error", err)
	}
}

// ---- 管理端视图 ----

// ChannelFidelityPoint 历史上的一次体检（列表里的小圆点）。
type ChannelFidelityPoint struct {
	CheckedAt time.Time `json:"checked_at"`
	Status    string    `json:"status"`
	IsRecheck bool      `json:"is_recheck"`
	Message   string    `json:"message,omitempty"`
}

// ChannelFidelityRow 一个（账号，模型）的体检状态。
type ChannelFidelityRow struct {
	// Kind deepseek 或 gemini_vt，决定展示哪组检查项。
	Kind                 string                 `json:"kind"`
	AccountID            int64                  `json:"account_id"`
	AccountName          string                 `json:"account_name"`
	Schedulable          bool                   `json:"schedulable"`
	Model                string                 `json:"model"`
	UpstreamModel        string                 `json:"upstream_model"`
	BaselinePromptTokens *int                   `json:"baseline_prompt_tokens"`
	Alerting             bool                   `json:"alerting"`
	Latest               *ChannelFidelityCheck  `json:"latest"`
	History              []ChannelFidelityPoint `json:"history"`
	// Passive 只用于 Gemini VT：最近窗口内真实流量的回复指纹（被动检测）。
	Passive *GeminiPassiveStats `json:"passive,omitempty"`
	// PassiveTriggered 被动检测已判定该账号混用（已暂停或已告警，等管理员处理）。
	PassiveTriggered bool `json:"passive_triggered"`
}

// ChannelFidelityStatus 保真体检页的数据。
type ChannelFidelityStatus struct {
	Available     bool                  `json:"available"`
	Config        ChannelFidelityConfig `json:"config"`
	NotifyEnabled bool                  `json:"notify_enabled"`
	Running       bool                  `json:"running"`
	LastSweepAt   *time.Time            `json:"last_sweep_at"`
	Rows          []ChannelFidelityRow  `json:"rows"`
}

// GetFidelityStatus 汇总所有体检目标的最新结果与近 24 小时历史。
func (s *SecurityAlertService) GetFidelityStatus(ctx context.Context) (ChannelFidelityStatus, error) {
	cfg, err := s.loadFidelityConfig(ctx)
	if err != nil {
		return ChannelFidelityStatus{}, err
	}
	out := ChannelFidelityStatus{
		Available:     s.fidelity != nil,
		Config:        cfg,
		NotifyEnabled: s.fidelityNotifyEnabled(ctx),
		Running:       s.fidelitySweepInProgress.Load(),
		Rows:          []ChannelFidelityRow{},
	}
	var lastSweep sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT updated_at FROM security_alert_state WHERE subject = $1`,
		fidelitySweepSubject).Scan(&lastSweep); err == nil && lastSweep.Valid {
		t := lastSweep.Time
		out.LastSweepAt = &t
	}
	if s.fidelity == nil {
		return out, nil
	}
	targets, err := s.fidelity.allTargets(ctx, cfg)
	if err != nil {
		return out, err
	}
	baselines := s.loadFidelityBaselines(ctx)

	latest := map[string]ChannelFidelityCheck{}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT ON (account_id, upstream_model) `+fidelityCheckColumns+`
 FROM channel_fidelity_checks WHERE checked_at > $1
 ORDER BY account_id, upstream_model, checked_at DESC, id DESC`, s.now().Add(-fidelityRetention))
	if err != nil {
		return out, err
	}
	for rows.Next() {
		c, err := scanFidelityCheck(rows.Scan)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		latest[fidelityKey(c.AccountID, c.UpstreamModel)] = c
	}
	_ = rows.Close()

	history := map[string][]ChannelFidelityPoint{}
	rows, err = s.db.QueryContext(ctx, `SELECT account_id, upstream_model, checked_at, status, is_recheck, message
 FROM channel_fidelity_checks WHERE checked_at > $1 ORDER BY checked_at, id`, s.now().Add(-fidelityHistoryWindow))
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id int64
		var model string
		var p ChannelFidelityPoint
		if err := rows.Scan(&id, &model, &p.CheckedAt, &p.Status, &p.IsRecheck, &p.Message); err != nil {
			_ = rows.Close()
			return out, err
		}
		key := fidelityKey(id, model)
		history[key] = append(history[key], p)
	}
	_ = rows.Close()

	alerting := map[string]struct{}{}
	rows, err = s.db.QueryContext(ctx, `SELECT subject FROM security_alert_state WHERE subject LIKE 'fidelity:%' OR subject LIKE 'vtpassive:%'`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var subject string
		if rows.Scan(&subject) == nil {
			alerting[subject] = struct{}{}
		}
	}
	_ = rows.Close()

	var passive map[string]GeminiPassiveStats
	for _, t := range targets {
		if t.kind() == fidelityKindGeminiVT {
			passive = s.loadGeminiPassiveStatsByTarget(ctx, cfg)
			break
		}
	}
	for _, t := range targets {
		key := fidelityKey(t.Account.ID, t.UpstreamModel)
		row := ChannelFidelityRow{
			Kind:          t.kind(),
			AccountID:     t.Account.ID,
			AccountName:   t.Account.Name,
			Schedulable:   t.Account.Schedulable,
			Model:         t.Model,
			UpstreamModel: t.UpstreamModel,
			History:       history[key],
		}
		if row.History == nil {
			row.History = []ChannelFidelityPoint{}
		}
		if v, ok := baselines[key]; ok {
			b := v
			row.BaselinePromptTokens = &b
		}
		if c, ok := latest[key]; ok {
			row.Latest = &c
		}
		_, row.Alerting = alerting[fidelityAlertSubject(t.Account.ID, t.UpstreamModel)]
		if t.kind() == fidelityKindGeminiVT {
			st := passive[key]
			st.WindowMinutes = cfg.GeminiPassiveWindowMinutes
			row.Passive = &st
			_, row.PassiveTriggered = alerting[geminiPassiveSubjectPrefix+strconv.FormatInt(t.Account.ID, 10)]
		}
		out.Rows = append(out.Rows, row)
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		if out.Rows[i].AccountID != out.Rows[j].AccountID {
			return out.Rows[i].AccountID < out.Rows[j].AccountID
		}
		return out.Rows[i].UpstreamModel < out.Rows[j].UpstreamModel
	})
	return out, nil
}

// RunFidelityCheck 手动体检一个（账号，模型）。通过时清掉告警并推送恢复；
// 不通过时只记录不告警（告警以定时体检的复测结果为准）。
func (s *SecurityAlertService) RunFidelityCheck(ctx context.Context, accountID int64, model string) (ChannelFidelityCheck, error) {
	if s.fidelity == nil {
		return ChannelFidelityCheck{}, errors.New("channel fidelity probe is not available")
	}
	cfg, err := s.loadFidelityConfig(ctx)
	if err != nil {
		return ChannelFidelityCheck{}, err
	}
	model = strings.TrimSpace(model)
	targets, err := s.fidelity.allTargets(ctx, cfg)
	if err != nil {
		return ChannelFidelityCheck{}, err
	}
	for _, t := range targets {
		if t.Account.ID != accountID || (t.Model != model && t.UpstreamModel != model) {
			continue
		}
		one := []fidelityTarget{t}
		s.attachFidelityCanaries(ctx, one)
		t = one[0]
		baselines := s.loadFidelityBaselines(ctx)
		res := s.probeFidelityTargets(ctx, []fidelityTarget{t}, baselines, cfg.PromptTokenTolerance)[0]
		s.recordFidelityCheck(ctx, t, &res, baselines)
		if res.Status == fidelityStatusPass {
			s.fidelityRecovered(ctx, t, res)
		}
		return res, nil
	}
	return ChannelFidelityCheck{}, fmt.Errorf("account %d does not serve model %q (or is not an active DeepSeek / Gemini VT API key account)", accountID, model)
}

// cleanupFidelityChecks 删除 7 天前的体检记录。
func (s *SecurityAlertService) cleanupFidelityChecks(ctx context.Context) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM channel_fidelity_checks WHERE checked_at < $1`,
		s.now().Add(-fidelityRetention)); err != nil {
		slog.Warn("[ChannelFidelity] cleanup failed", "error", err)
	}
}
