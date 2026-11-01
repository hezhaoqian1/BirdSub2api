package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Gemini VT 被动检测的判定（安全告警 leader 每分钟一次）：汇总 gemini_upstream_fingerprints 里
// 最近 GeminiPassiveWindowMinutes 分钟的真实流量指纹，可调度的 VT 账号非 Vertex 回复
// 达到阈值时自动暂停调度（GeminiAutoPause）并推送企业微信。
//
// security_alert_state 的 vtpassive:<账号> 记录触发时间：账号被暂停期间不再判断；
// 管理员重新开启调度后只看触发时间之后的流量，再次混用会再次暂停并告警；
// 干净地跑满一个窗口后删除记录。

const (
	geminiPassiveSubjectPrefix  = "vtpassive:"
	geminiPassiveMinNonVertex   = 3
	geminiPassiveRealertNoPause = time.Hour
	securityAlertKindGeminiVT   = "gemini_vt_mixing"
)

// GeminiPassiveStats 一个账号（或账号 + 模型）在窗口内的真实流量指纹。
type GeminiPassiveStats struct {
	Responses      int64 `json:"responses"`
	NonVertex      int64 `json:"non_vertex"`
	NoResponseID   int64 `json:"no_response_id"`
	OtherSignature int64 `json:"other_signature"`
	MediaRequests  int64 `json:"media_requests"`
	MediaDropped   int64 `json:"media_dropped"`
	WindowMinutes  int   `json:"window_minutes"`
}

func (g *GeminiPassiveStats) add(c geminiFingerprintCounts) {
	g.Responses += c.Responses
	g.NonVertex += c.NonVertex
	g.NoResponseID += c.NoResponseID
	g.OtherSignature += c.OtherSignature
	g.MediaRequests += c.MediaRequests
	g.MediaDropped += c.MediaDropped
}

// geminiPassiveMixing 是否达到混用阈值。
func geminiPassiveMixing(st GeminiPassiveStats, cfg ChannelFidelityConfig) bool {
	return st.Responses >= int64(cfg.GeminiPassiveMinResponses) &&
		st.NonVertex >= geminiPassiveMinNonVertex &&
		st.NonVertex*100 >= int64(cfg.GeminiPassiveThresholdPercent)*st.Responses
}

type geminiFingerprintRow struct {
	accountID int64
	model     string
	bucket    time.Time
	counts    geminiFingerprintCounts
}

// loadGeminiFingerprints 读 since 之后开始的桶。
func (s *SecurityAlertService) loadGeminiFingerprints(ctx context.Context, since time.Time) ([]geminiFingerprintRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account_id, model, bucket_start, responses, non_vertex, no_response_id,
 no_create_time, vertex_signature, other_signature, media_requests, media_dropped
 FROM gemini_upstream_fingerprints WHERE bucket_start >= $1`, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []geminiFingerprintRow
	for rows.Next() {
		var r geminiFingerprintRow
		c := &r.counts
		if err := rows.Scan(&r.accountID, &r.model, &r.bucket, &c.Responses, &c.NonVertex, &c.NoResponseID,
			&c.NoCreateTime, &c.VertexSignature, &c.OtherSignature, &c.MediaRequests, &c.MediaDropped); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// checkGeminiPassiveFingerprints leader 每个 tick 调用一次。
func (s *SecurityAlertService) checkGeminiPassiveFingerprints(ctx context.Context) {
	if s.fidelity == nil || s.fidelity.accounts == nil || s.db == nil {
		return
	}
	cfg, err := s.loadFidelityConfig(ctx)
	if err != nil || !cfg.GeminiPassiveEnabled {
		return
	}
	now := s.now()
	window := time.Duration(cfg.GeminiPassiveWindowMinutes) * time.Minute
	since := now.Add(-window).Truncate(geminiFingerprintBucket)
	rows, err := s.loadGeminiFingerprints(ctx, since)
	if err != nil {
		slog.Warn("[GeminiVT] failed to load fingerprints", "error", err)
		return
	}
	states, err := s.loadGeminiPassiveStates(ctx)
	if err != nil {
		slog.Warn("[GeminiVT] failed to load passive states", "error", err)
		return
	}
	if len(rows) == 0 && len(states) == 0 {
		return
	}
	accounts, err := s.fidelity.accounts.ListByPlatform(ctx, PlatformGemini)
	if err != nil {
		slog.Warn("[GeminiVT] failed to list accounts", "error", err)
		return
	}
	for i := range accounts {
		a := &accounts[i]
		if a.Type != AccountTypeAPIKey || !isGeminiVertexAccount(a, cfg.GeminiAccountIDs) {
			continue
		}
		triggeredAt, hasState := states[a.ID]
		if hasState && !a.Schedulable {
			continue // 已暂停，等管理员处理
		}
		start := since
		if hasState && triggeredAt.After(start) {
			start = triggeredAt // 重新开启后只看之后的流量
		}
		total, perModel := aggregateGeminiPassive(rows, a.ID, start)
		total.WindowMinutes = cfg.GeminiPassiveWindowMinutes
		if !geminiPassiveMixing(total, cfg) {
			if hasState && now.Sub(triggeredAt) > window {
				s.clearGeminiPassiveState(ctx, a.ID)
			}
			continue
		}
		if !a.Schedulable {
			continue // 停止调度的账号只记录，不处理
		}
		if hasState && !cfg.GeminiAutoPause && now.Sub(triggeredAt) < geminiPassiveRealertNoPause {
			continue // 不自动暂停时最多每小时提醒一次
		}
		paused := false
		if cfg.GeminiAutoPause {
			paused = s.pauseGeminiVTAccount(ctx, a)
		}
		s.raiseGeminiPassiveAlert(ctx, a, total, perModel, paused, now)
	}
}

// aggregateGeminiPassive 汇总某账号 start 之后（含）开始的桶，另按模型分开。
func aggregateGeminiPassive(rows []geminiFingerprintRow, accountID int64, start time.Time) (GeminiPassiveStats, map[string]GeminiPassiveStats) {
	var total GeminiPassiveStats
	perModel := map[string]GeminiPassiveStats{}
	for _, r := range rows {
		if r.accountID != accountID || r.bucket.Before(start) {
			continue
		}
		total.add(r.counts)
		m := perModel[r.model]
		m.add(r.counts)
		perModel[r.model] = m
	}
	return total, perModel
}

func (s *SecurityAlertService) loadGeminiPassiveStates(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject, value FROM security_alert_state WHERE subject LIKE 'vtpassive:%'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]time.Time{}
	for rows.Next() {
		var subject string
		var value float64
		if rows.Scan(&subject, &value) != nil {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(subject, geminiPassiveSubjectPrefix), 10, 64)
		if err == nil {
			out[id] = time.Unix(int64(value), 0)
		}
	}
	return out, rows.Err()
}

func (s *SecurityAlertService) clearGeminiPassiveState(ctx context.Context, accountID int64) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM security_alert_state WHERE subject = $1`,
		geminiPassiveSubjectPrefix+strconv.FormatInt(accountID, 10)); err != nil {
		slog.Warn("[GeminiVT] failed to clear passive state", "account_id", accountID, "error", err)
	}
}

// pauseGeminiVTAccount 暂停账号调度（走账号仓库：同时通知调度器刷新快照）。
func (s *SecurityAlertService) pauseGeminiVTAccount(ctx context.Context, a *Account) bool {
	if s.fidelity == nil || s.fidelity.accounts == nil || a == nil || !a.Schedulable {
		return false
	}
	pauseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fidelityStateTimeout)
	defer cancel()
	if err := s.fidelity.accounts.SetSchedulable(pauseCtx, a.ID, false); err != nil {
		slog.Error("[GeminiVT] failed to pause account", "account_id", a.ID, "error", err)
		return false
	}
	a.Schedulable = false
	slog.Warn("[GeminiVT] account paused: upstream is not pure Vertex", "account_id", a.ID, "name", a.Name)
	return true
}

func (s *SecurityAlertService) raiseGeminiPassiveAlert(ctx context.Context, a *Account, total GeminiPassiveStats, perModel map[string]GeminiPassiveStats, paused bool, now time.Time) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fidelityStateTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("[GeminiVT] failed to begin alert tx", "error", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO security_alert_state(subject, level, value, updated_at) VALUES ($1, 1, $2, NOW())
 ON CONFLICT (subject) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		geminiPassiveSubjectPrefix+strconv.FormatInt(a.ID, 10), float64(now.Unix())); err != nil {
		slog.Warn("[GeminiVT] failed to record passive state", "error", err)
		return
	}
	if s.fidelityNotifyEnabled(ctx) {
		if err := emitSecurityAlert(ctx, tx, geminiPassiveAlertDraft(a, total, perModel, paused)); err != nil {
			slog.Warn("[GeminiVT] failed to record alert", "error", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("[GeminiVT] failed to commit alert", "error", err)
	}
}

func geminiPassiveAlertDraft(a *Account, total GeminiPassiveStats, perModel map[string]GeminiPassiveStats, paused bool) securityAlertDraft {
	pct := 0
	if total.Responses > 0 {
		pct = int(total.NonVertex * 100 / total.Responses)
	}
	lines := []string{
		"账号：" + channelMonitorLabel(a.ID, a.Name),
		fmt.Sprintf("真实流量（最近 %d 分钟）：%d 条回复里 %d 条不是 Vertex（%d%%）",
			total.WindowMinutes, total.Responses, total.NonVertex, pct),
	}
	var reasons []string
	if total.NoResponseID > 0 {
		reasons = append(reasons, fmt.Sprintf("缺 responseId %d 条", total.NoResponseID))
	}
	if total.OtherSignature > 0 {
		reasons = append(reasons, fmt.Sprintf("签名不是 AY 开头 %d 条", total.OtherSignature))
	}
	if total.MediaDropped > 0 {
		reasons = append(reasons, fmt.Sprintf("图片/视频被丢掉 %d 条（共 %d 条带媒体）", total.MediaDropped, total.MediaRequests))
	}
	if len(reasons) > 0 {
		lines = append(lines, "其中："+strings.Join(reasons, "，"))
	}
	models := make([]string, 0, len(perModel))
	for m := range perModel {
		models = append(models, m)
	}
	sort.Strings(models)
	var parts []string
	for _, m := range models {
		st := perModel[m]
		parts = append(parts, fmt.Sprintf("%s %d/%d", sanitizeSecurityText(m, 60), st.NonVertex, st.Responses))
	}
	if len(parts) > 0 {
		lines = append(lines, "按模型（非 Vertex/总数）："+strings.Join(parts, "，"))
	}
	lines = append(lines, "判断：疑似混用或兜底到 AI Studio 等非 Vertex 资源。")
	if paused {
		lines = append(lines, "处理：已自动暂停该账号调度。确认上游修好后在账号管理里重新开启；开启后再混用会再次暂停。")
	} else {
		lines = append(lines, "处理：自动暂停已关闭或失败，请手动暂停该账号调度并找上游核实。")
	}
	return securityAlertDraft{
		Kind:     securityAlertKindGeminiVT,
		Severity: SecurityAlertCritical,
		Title:    "Gemini VT 混用（真实流量）",
		Lines:    lines,
	}
}

// loadGeminiPassiveStatsByTarget 管理端视图：窗口内每个 (账号, 模型) 的指纹。
func (s *SecurityAlertService) loadGeminiPassiveStatsByTarget(ctx context.Context, cfg ChannelFidelityConfig) map[string]GeminiPassiveStats {
	out := map[string]GeminiPassiveStats{}
	if s.db == nil {
		return out
	}
	since := s.now().Add(-time.Duration(cfg.GeminiPassiveWindowMinutes) * time.Minute).Truncate(geminiFingerprintBucket)
	rows, err := s.loadGeminiFingerprints(ctx, since)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("[GeminiVT] failed to load fingerprints for status", "error", err)
		}
		return out
	}
	for _, r := range rows {
		key := fidelityKey(r.accountID, r.model)
		st := out[key]
		st.add(r.counts)
		st.WindowMinutes = cfg.GeminiPassiveWindowMinutes
		out[key] = st
	}
	return out
}

// cleanupGeminiFingerprints 删除 7 天前的指纹。
func (s *SecurityAlertService) cleanupGeminiFingerprints(ctx context.Context) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gemini_upstream_fingerprints WHERE bucket_start < $1`,
		s.now().Add(-geminiFingerprintRetention)); err != nil {
		slog.Warn("[GeminiVT] fingerprint cleanup failed", "error", err)
	}
}

// SetGeminiFingerprintRecorder 注入被动检测记录器（网关各实例都会写入，只有 leader 判定）。
func (s *SecurityAlertService) SetGeminiFingerprintRecorder(r *geminiFingerprintRecorder) {
	if s == nil || r == nil {
		return
	}
	s.geminiFP = r
	geminiFingerprints.Store(r)
}
