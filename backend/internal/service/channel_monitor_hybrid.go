package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

var ErrHybridConflict = errors.New("监控配置已更新，请刷新后重试")

type HybridMonitorService struct {
	db              *sql.DB
	monitors        *ChannelMonitorService
	encryptor       SecretEncryptor
	settings        *SettingService
	origin          string
	cancel          context.CancelFunc
	done            chan struct{}
	startOnce       sync.Once
	workers         sync.WaitGroup
	probeWorkers    sync.WaitGroup
	client          *http.Client
	lastCleanup     time.Time
	probeSlots      chan struct{}
	instanceID      string
	probeKeyVersion int
}

func NewHybridMonitorService(db *sql.DB, monitors *ChannelMonitorService, encryptor SecretEncryptor, settings *SettingService, origin string) *HybridMonitorService {
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return &HybridMonitorService{db: db, monitors: monitors, encryptor: encryptor, settings: settings, origin: origin, done: make(chan struct{}), client: client, probeSlots: make(chan struct{}, 6), instanceID: uuid.NewString()}
}

func (s *HybridMonitorService) Config(ctx context.Context) (HybridMonitorConfig, error) {
	var cfg HybridMonitorConfig
	var raw []byte
	var webhook, secret string
	err := s.db.QueryRowContext(ctx, "SELECT version,config,webhook_encrypted,secret_encrypted FROM channel_monitor_hybrid_config WHERE id=1").Scan(&cfg.Version, &raw, &webhook, &secret)
	if err != nil {
		return cfg, err
	}
	version := cfg.Version
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return cfg, err
	}
	cfg.Version = version
	cfg.Webhook = ""
	cfg.Secret = ""
	cfg.WebhookConfigured = webhook != ""
	cfg.SecretConfigured = secret != ""
	if cfg.Rules == nil {
		cfg.Rules = []HybridMonitorRule{}
	}
	return cfg, nil
}

func (s *HybridMonitorService) Save(ctx context.Context, cfg HybridMonitorConfig) (HybridMonitorConfig, error) {
	if err := validateHybridConfig(&cfg); err != nil {
		return cfg, err
	}
	for _, rule := range cfg.Rules {
		if !cfg.Enabled || !rule.Enabled {
			continue
		}
		if _, _, err := s.target(ctx, rule); err != nil {
			return cfg, fmt.Errorf("%s: %w", rule.Name, err)
		}
	}
	var webhook, secret string
	if cfg.Webhook != "" {
		if _, err := signedHybridWebhook(cfg.Webhook, "", time.Now()); err != nil {
			return cfg, err
		}
		encrypted, err := s.encryptor.Encrypt(strings.TrimSpace(cfg.Webhook))
		if err != nil {
			return cfg, err
		}
		webhook = encrypted
	}
	if cfg.Secret != "" {
		encrypted, err := s.encryptor.Encrypt(strings.TrimSpace(cfg.Secret))
		if err != nil {
			return cfg, err
		}
		secret = encrypted
	}
	expected := cfg.Version
	clear := cfg.ClearCredentials
	cfg.Webhook = ""
	cfg.Secret = ""
	cfg.ClearCredentials = false
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cfg, err
	}
	defer tx.Rollback()
	var previousRaw []byte
	var storedVersion int
	if err = tx.QueryRowContext(ctx, "SELECT version,config FROM channel_monitor_hybrid_config WHERE id=1 FOR UPDATE").Scan(&storedVersion, &previousRaw); err != nil {
		return cfg, err
	}
	if storedVersion != expected {
		return cfg, ErrHybridConflict
	}
	var previous HybridMonitorConfig
	if err = json.Unmarshal(previousRaw, &previous); err != nil {
		return cfg, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE channel_monitor_hybrid_config SET version=version+1,config=$1,
 webhook_encrypted=CASE WHEN $5 THEN '' WHEN $3<>'' THEN $3 ELSE webhook_encrypted END,
 secret_encrypted=CASE WHEN $5 THEN '' WHEN $4<>'' THEN $4 ELSE secret_encrypted END WHERE id=1 AND version=$2`, string(raw), expected, webhook, secret, clear)
	if err != nil {
		return cfg, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return cfg, ErrHybridConflict
	}
	for _, oldRule := range previous.Rules {
		var next *HybridMonitorRule
		for index := range cfg.Rules {
			if cfg.Rules[index].ID == oldRule.ID {
				next = &cfg.Rules[index]
				break
			}
		}
		reset := next == nil || previous.Enabled != cfg.Enabled || (next != nil && (oldRule.GroupID != next.GroupID || oldRule.MonitorID != next.MonitorID || oldRule.Model != next.Model || oldRule.Enabled != next.Enabled || oldRule.RedSuccess != next.RedSuccess || oldRule.GreenSuccess != next.GreenSuccess || oldRule.WarningTTFT != next.WarningTTFT || oldRule.CriticalTTFT != next.CriticalTTFT || oldRule.MinimumTTFTSamples != next.MinimumTTFTSamples))
		if reset {
			if _, err = tx.ExecContext(ctx, "DELETE FROM channel_monitor_hybrid_states WHERE rule_id=$1", oldRule.ID); err != nil {
				return cfg, err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM channel_monitor_hybrid_cursors WHERE rule_id=$1", oldRule.ID); err != nil {
				return cfg, err
			}
		}
		if reset || !cfg.NotificationsEnabled || (next != nil && next.Muted) {
			if _, err = tx.ExecContext(ctx, "DELETE FROM channel_monitor_hybrid_notifications WHERE rule_id=$1 AND sent_at IS NULL", oldRule.ID); err != nil {
				return cfg, err
			}
		}
		if next == nil || (next != nil && (oldRule.GroupID != next.GroupID || oldRule.Model != next.Model || oldRule.MonitorID != next.MonitorID)) {
			if _, err = tx.ExecContext(ctx, "DELETE FROM channel_monitor_hybrid_minutes WHERE rule_id=$1", oldRule.ID); err != nil {
				return cfg, err
			}
		}
	}
	if cfg.Enabled && cfg.NotificationsEnabled {
		for _, rule := range cfg.Rules {
			if !rule.Enabled || rule.Muted {
				continue
			}
			wasDeliverable := previous.Enabled && previous.NotificationsEnabled
			for _, oldRule := range previous.Rules {
				if oldRule.ID == rule.ID {
					wasDeliverable = wasDeliverable && oldRule.Enabled && !oldRule.Muted
					break
				}
			}
			if wasDeliverable {
				continue
			}
			var stateRaw, pointRaw []byte
			if tx.QueryRowContext(ctx, "SELECT state FROM channel_monitor_hybrid_states WHERE rule_id=$1", rule.ID).Scan(&stateRaw) != nil {
				continue
			}
			var state HybridMonitorState
			if json.Unmarshal(stateRaw, &state) != nil || (!state.Incident && !state.BlindSpot) {
				continue
			}
			if tx.QueryRowContext(ctx, "SELECT result FROM channel_monitor_hybrid_minutes WHERE rule_id=$1 AND minute=$2", rule.ID, state.Minute).Scan(&pointRaw) != nil {
				continue
			}
			var point HybridMonitorMinute
			if json.Unmarshal(pointRaw, &point) != nil {
				continue
			}
			kind := "incident"
			if state.BlindSpot {
				kind = "blind_spot"
			}
			if err = s.enqueueNotification(tx, rule, point, state, kind); err != nil {
				return cfg, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return cfg, err
	}
	return s.Config(ctx)
}

func (s *HybridMonitorService) target(ctx context.Context, rule HybridMonitorRule) (*ChannelMonitor, int64, error) {
	monitor, err := s.monitors.Get(ctx, rule.MonitorID)
	if err != nil {
		return nil, 0, err
	}
	if monitor.APIKeyDecryptFailed || monitor.CheckMode == MonitorCheckModeQuota {
		return nil, 0, errors.New("请选择具有有效凭证的探测配置")
	}
	if monitor.PrimaryModel != rule.Model {
		found := false
		for _, model := range monitor.ExtraModels {
			found = found || model == rule.Model
		}
		if !found {
			return nil, 0, errors.New("模型不在所选探测配置中")
		}
	}
	var keyID int64
	if err = s.db.QueryRowContext(ctx, `SELECT id FROM api_keys WHERE key=$1 AND group_id=$2 AND status='active' AND deleted_at IS NULL`, monitor.APIKey, rule.GroupID).Scan(&keyID); err != nil {
		return nil, 0, errors.New("探测 Key 必须是本站该分组的有效专用 API Key")
	}
	RegisterHybridMonitorProbeKey(keyID, rule.GroupID)
	origin, err := url.Parse(monitor.Endpoint)
	if err != nil || origin.Scheme != "https" || origin.Host == "" {
		return nil, 0, errors.New("探测地址必须是本站 HTTPS 地址")
	}
	site, err := url.Parse(s.settings.GetFrontendURL(ctx))
	if err != nil || site.Host == "" || !strings.EqualFold(site.Host, origin.Host) {
		return nil, 0, errors.New("探测地址必须与系统设置的前端地址同源")
	}
	for key := range monitor.ExtraHeaders {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "x-api-key") || strings.EqualFold(key, "x-goog-api-key") {
			return nil, 0, errors.New("融合探测不允许模板覆盖认证请求头")
		}
	}
	return monitor, keyID, nil
}

func (s *HybridMonitorService) Owns(ctx context.Context, id int64) bool {
	cfg, err := s.Config(ctx)
	if err != nil || !cfg.Enabled {
		return false
	}
	for _, rule := range cfg.Rules {
		if rule.MonitorID == id {
			return true
		}
	}
	return false
}

func (s *HybridMonitorService) Start() {
	s.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.workers.Add(2)
		go func() {
			defer s.workers.Done()
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			s.tick(ctx)
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.tick(ctx)
				}
			}
		}()
		go func() {
			defer s.workers.Done()
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			s.deliveryTick(ctx)
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.deliveryTick(ctx)
				}
			}
		}()
		go func() {
			s.workers.Wait()
			s.probeWorkers.Wait()
			close(s.done)
		}()
	})
}
func (s *HybridMonitorService) Stop() {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
}

func (s *HybridMonitorService) tick(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	if s.settings == nil || !s.settings.GetChannelMonitorRuntime(ctx).Enabled {
		return
	}
	s.publishActivity(ctx)
	cfg, err := s.Config(ctx)
	if err != nil || !cfg.Enabled {
		return
	}
	if s.probeKeyVersion != cfg.Version {
		probeKeys := make(map[int64]int64)
		for _, rule := range cfg.Rules {
			if rule.Enabled {
				if _, keyID, targetErr := s.target(ctx, rule); targetErr == nil {
					probeKeys[keyID] = rule.GroupID
				}
			}
		}
		ReplaceHybridMonitorProbeKeys(probeKeys)
		s.probeKeyVersion = cfg.Version
	}
	release, ok := tryAcquireSingletonLeaderLock(ctx, nil, s.db, "channel-monitor-hybrid", uuid.NewString(), time.Minute)
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}
	now := time.Now().UTC()
	minute := now.Add(-30 * time.Second).Truncate(time.Minute).Add(-time.Minute)
	rules := make(map[string]HybridMonitorRule, len(cfg.Rules))
	ids := make([]string, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if rule.Enabled {
			rules[rule.ID] = rule
			ids = append(ids, rule.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO channel_monitor_hybrid_cursors(rule_id,next_minute)
 SELECT rule_id,$2 FROM unnest($1::text[]) AS enabled(rule_id) ON CONFLICT(rule_id) DO NOTHING`, pq.Array(ids), minute); err != nil {
		s.recordRuntime(ctx, err)
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rule_id,next_minute FROM channel_monitor_hybrid_cursors
 WHERE rule_id=ANY($1) AND next_minute<=$2 ORDER BY next_minute,rule_id LIMIT 24`, pq.Array(ids), minute)
	if err != nil {
		s.recordRuntime(ctx, err)
		return
	}
	type task struct {
		rule   HybridMonitorRule
		minute time.Time
	}
	tasks := make([]task, 0, 24)
	for rows.Next() {
		var id string
		var targetMinute time.Time
		if rows.Scan(&id, &targetMinute) == nil {
			if rule, exists := rules[id]; exists {
				tasks = append(tasks, task{rule: rule, minute: targetMinute})
			}
		}
	}
	rows.Close()
	var wait sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, pending := range tasks {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wait.Wait()
			return
		}
		wait.Add(1)
		go func(pending task) {
			defer wait.Done()
			defer func() { <-slots }()
			_ = s.evaluate(ctx, cfg, pending.rule, pending.minute)
		}(pending)
	}
	wait.Wait()
	s.scheduleProbes(parent, cfg, rules, ids, now)
	s.recordRuntime(ctx, ctx.Err())
	if time.Since(s.lastCleanup) >= time.Hour {
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 2*time.Second)
		defer cleanupCancel()
		if _, err := s.db.ExecContext(cleanupCtx, "DELETE FROM channel_monitor_hybrid_minutes WHERE ctid IN (SELECT ctid FROM channel_monitor_hybrid_minutes WHERE minute < NOW()-INTERVAL '30 days' ORDER BY minute LIMIT 10000)"); err == nil {
			s.lastCleanup = time.Now()
		}
	}
}

func (s *HybridMonitorService) recordRuntime(ctx context.Context, runErr error) {
	detail := ""
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		detail = runErr.Error()
	}
	_, _ = s.db.ExecContext(ctx, "UPDATE channel_monitor_hybrid_runtime SET heartbeat_at=NOW(),last_error=$1 WHERE id=1", detail)
}

func (s *HybridMonitorService) publishActivity(ctx context.Context) {
	if s.instanceID == "" {
		s.instanceID = uuid.NewString()
	}
	HybridBusinessActivitySnapshot(func(groupID int64, inFlight int64, lastActivity time.Time) {
		var activity any
		if !lastActivity.IsZero() {
			activity = lastActivity
		}
		_, _ = s.db.ExecContext(ctx, `INSERT INTO channel_monitor_hybrid_activity(instance_id,group_id,in_flight,last_activity_at,heartbeat_at)
 VALUES($1,$2,$3,$4,NOW()) ON CONFLICT(instance_id,group_id) DO UPDATE SET in_flight=EXCLUDED.in_flight,last_activity_at=EXCLUDED.last_activity_at,heartbeat_at=NOW()`, s.instanceID, groupID, inFlight, activity)
	})
}

func (s *HybridMonitorService) evaluate(ctx context.Context, cfg HybridMonitorConfig, rule HybridMonitorRule, minute time.Time) bool {
	var probeRunning bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM channel_monitor_hybrid_cursors
 WHERE rule_id=$1 AND last_probe_at>=$2 AND last_probe_at<$3 AND probe_lease_until>NOW())`, rule.ID, minute, minute.Add(time.Minute)).Scan(&probeRunning); err != nil || probeRunning {
		return false
	}
	point := HybridMonitorMinute{Minute: minute, ObservedAt: time.Now().UTC(), Source: "unknown", Status: "unknown", Reasons: []string{"monitor_unavailable"}, Finalized: true}
	_, keyID, err := s.target(ctx, rule)
	if err != nil {
		point.Detail = err.Error()
	} else {
		observed, readErr := s.passive(ctx, rule, keyID, minute)
		if readErr != nil {
			point.Detail = "业务日志读取失败"
		} else if observed.Successes+observed.Failures-observed.Excluded > 0 {
			point = evaluateHybridMinute(rule, observed)
		} else {
			var raw []byte
			probeErr := s.db.QueryRowContext(ctx, "SELECT result FROM channel_monitor_hybrid_minutes WHERE rule_id=$1 AND minute=$2 AND result->>'source'='active'", rule.ID, minute).Scan(&raw)
			if probeErr == nil && json.Unmarshal(raw, &point) == nil {
				point.Requests = observed.Requests
				point.Successes = observed.Successes
				point.Failures = observed.Failures
				point.Excluded = observed.Excluded
				point.Finalized = true
			} else {
				inFlight, _ := HybridBusinessActivity(rule.GroupID)
				point = observed
				point.Status = "unknown"
				point.Reasons = []string{"no_eligible_requests"}
				if inFlight > 0 {
					point.Reasons = []string{"waiting_request_completion"}
					point.Detail = "有业务请求仍在处理中，本分钟不发起主动探测"
				}
			}
		}
	}
	point.Finalized = true
	if err := s.persist(ctx, cfg, rule, point); err != nil {
		if !errors.Is(err, ErrHybridConflict) {
			slog.Warn("hybrid monitor persistence failed", "rule_id", rule.ID, "error", err)
		}
		return false
	}
	return true
}

func (s *HybridMonitorService) passive(ctx context.Context, rule HybridMonitorRule, keyID int64, minute time.Time) (HybridMonitorMinute, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	point := HybridMonitorMinute{Minute: minute, ObservedAt: time.Now().UTC(), Source: "passive"}
	rows, err := s.db.QueryContext(ctx, `WITH usage_observations AS (
 SELECT id,api_key_id,COALESCE(NULLIF(regexp_replace(request_id,'^(local|client):',''),''),'usage:'||id::text) request_id,first_token_ms::bigint ttft,created_at
 FROM usage_logs WHERE group_id=$1 AND COALESCE(NULLIF(BTRIM(requested_model),''),model)=$2 AND api_key_id<>$3 AND created_at >=$4 AND created_at<$5
 AND (actual_cost>0 OR input_tokens>0 OR output_tokens>0 OR cache_read_tokens>0 OR cache_creation_tokens>0) AND COALESCE(request_type,0) NOT IN (4,6)
 ), error_observations AS (
	 SELECT id,COALESCE(api_key_id,0) api_key_id,COALESCE(NULLIF(regexp_replace(request_id,'^(local|client):',''),''),'error:'||id::text) request_id,
 NULLIF(regexp_replace(client_request_id,'^(local|client):',''),'') client_request_id,COALESCE(error_owner,'') owner,
 COALESCE(status_code,0) status,COALESCE(upstream_status_code,0) upstream_status,COALESCE(error_type,'') error_type,
 COALESCE(error_message,'') message,created_at
	 FROM ops_error_logs WHERE group_id=$1 AND COALESCE(NULLIF(BTRIM(requested_model),''),model)=$2 AND COALESCE(api_key_id,0)<>$3
 AND created_at >=$4 AND created_at<$5 AND NOT is_count_tokens AND COALESCE(request_type,0)<>6 AND (status_code>=400 OR error_type='cyber_policy')
 ), usage_final AS (
 SELECT e.id IS NULL success,u.ttft,COALESCE(e.owner,'') owner,COALESCE(e.status,0) status,
 COALESCE(e.upstream_status,0) upstream_status,COALESCE(e.error_type,'') error_type,COALESCE(e.message,'') message
 FROM usage_observations u LEFT JOIN LATERAL (
	   SELECT e.* FROM error_observations e WHERE e.api_key_id=u.api_key_id AND (e.request_id=u.request_id OR e.client_request_id=u.request_id) ORDER BY e.created_at DESC LIMIT 1
 ) e ON true
 ), unmatched_errors AS (
	 SELECT DISTINCT ON (e.api_key_id,COALESCE(e.client_request_id,e.request_id)) false success,NULL::bigint ttft,e.owner,e.status,e.upstream_status,e.error_type,e.message
 FROM error_observations e WHERE NOT EXISTS (
	   SELECT 1 FROM usage_observations u WHERE e.api_key_id=u.api_key_id AND (e.request_id=u.request_id OR e.client_request_id=u.request_id)
	 ) ORDER BY e.api_key_id,COALESCE(e.client_request_id,e.request_id),e.created_at DESC
 ) SELECT * FROM usage_final UNION ALL SELECT * FROM unmatched_errors`, rule.GroupID, rule.Model, keyID, minute, minute.Add(time.Minute))
	if err != nil {
		return point, err
	}
	defer rows.Close()
	timings := []int64{}
	for rows.Next() {
		var success bool
		var ttft sql.NullInt64
		var input ChannelMonitorV2ErrorInput
		if err = rows.Scan(&success, &ttft, &input.ErrorOwner, &input.StatusCode, &input.UpstreamStatusCode, &input.ErrorType, &input.Message); err != nil {
			return point, err
		}
		point.Requests++
		if success {
			point.Successes++
			if ttft.Valid {
				timings = append(timings, ttft.Int64)
			}
		} else {
			point.Failures++
			category := ClassifyChannelMonitorV2Error(input)
			if input.ErrorOwner != "provider" && input.UpstreamStatusCode == 0 && (category == "authentication" || category == "invalid_request" || category == "context_limit" || category == "content_policy" || category == "client_cancelled" || category == "group_access") {
				point.Excluded++
			}
		}
	}
	if err = rows.Err(); err != nil {
		return point, err
	}
	sort.Slice(timings, func(left, right int) bool { return timings[left] < timings[right] })
	point.TTFTSamples = len(timings)
	if len(timings) > 0 {
		median := timings[(len(timings)-1)/2]
		point.TTFTMs = &median
	}
	return point, nil
}

func (s *HybridMonitorService) persist(ctx context.Context, cfg HybridMonitorConfig, rule HybridMonitorRule, point HybridMonitorMinute) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "SELECT version FROM channel_monitor_hybrid_config WHERE id=1 FOR SHARE").Scan(&version); err != nil {
		return err
	}
	if version != cfg.Version {
		return ErrHybridConflict
	}
	var existingRaw []byte
	existingErr := tx.QueryRowContext(ctx, "SELECT result FROM channel_monitor_hybrid_minutes WHERE rule_id=$1 AND minute=$2 FOR UPDATE", rule.ID, point.Minute).Scan(&existingRaw)
	if existingErr == nil {
		var existing HybridMonitorMinute
		if json.Unmarshal(existingRaw, &existing) == nil && existing.Finalized {
			_, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_hybrid_cursors(rule_id,next_minute) VALUES($1,$2)
 ON CONFLICT(rule_id) DO UPDATE SET next_minute=GREATEST(channel_monitor_hybrid_cursors.next_minute,EXCLUDED.next_minute)`, rule.ID, point.Minute.Add(time.Minute))
			if err != nil {
				return err
			}
			return tx.Commit()
		}
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return existingErr
	}
	point.Finalized = true
	raw, _ := json.Marshal(point)
	_, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_hybrid_minutes(rule_id,minute,result) VALUES($1,$2,$3)
 ON CONFLICT(rule_id,minute) DO UPDATE SET result=EXCLUDED.result`, rule.ID, point.Minute, string(raw))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_hybrid_cursors(rule_id,next_minute) VALUES($1,$2)
 ON CONFLICT(rule_id) DO UPDATE SET next_minute=GREATEST(channel_monitor_hybrid_cursors.next_minute,EXCLUDED.next_minute)`, rule.ID, point.Minute.Add(time.Minute)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_hybrid_states(rule_id,state) VALUES($1,'{}') ON CONFLICT DO NOTHING`, rule.ID); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT state FROM channel_monitor_hybrid_states WHERE rule_id=$1 FOR UPDATE", rule.ID).Scan(&raw); err != nil {
		return err
	}
	var state HybridMonitorState
	if err = json.Unmarshal(raw, &state); err != nil {
		return err
	}
	state, kind := advanceHybridState(state, point)
	raw, _ = json.Marshal(state)
	if _, err = tx.ExecContext(ctx, "UPDATE channel_monitor_hybrid_states SET state=$2 WHERE rule_id=$1", rule.ID, string(raw)); err != nil {
		return err
	}
	if kind != "" && cfg.NotificationsEnabled && !rule.Muted {
		if err = s.enqueueNotification(tx, rule, point, state, kind); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *HybridMonitorService) persistProbe(ctx context.Context, cfg HybridMonitorConfig, rule HybridMonitorRule, point HybridMonitorMinute) error {
	point.Finalized = false
	raw, _ := json.Marshal(point)
	_, err := s.db.ExecContext(ctx, `INSERT INTO channel_monitor_hybrid_minutes(rule_id,minute,result)
 SELECT $1,$2,$3 WHERE (SELECT version FROM channel_monitor_hybrid_config WHERE id=1)=$4
 ON CONFLICT(rule_id,minute) DO NOTHING`, rule.ID, point.Minute, string(raw), cfg.Version)
	return err
}

func (s *HybridMonitorService) scheduleProbes(parent context.Context, cfg HybridMonitorConfig, rules map[string]HybridMonitorRule, ids []string, now time.Time) {
	if now.Second() < 45 {
		return
	}
	inFlight := int64(0)
	lastActivity := time.Time{}
	var sharedInFlight int64
	var sharedLast sql.NullTime
	currentMinute := now.Truncate(time.Minute)
	available := cap(s.probeSlots) - len(s.probeSlots)
	if available <= 0 {
		return
	}
	rows, err := s.db.QueryContext(parent, `SELECT c.rule_id FROM channel_monitor_hybrid_cursors c
 WHERE c.rule_id=ANY($1)
 AND (c.probe_lease_until IS NULL OR c.probe_lease_until<NOW())
 AND NOT EXISTS(SELECT 1 FROM channel_monitor_hybrid_minutes current WHERE current.rule_id=c.rule_id AND current.minute=$2)
 ORDER BY c.last_probe_at NULLS FIRST,c.rule_id LIMIT $3`, pq.Array(ids), currentMinute, available)
	if err != nil {
		return
	}
	candidates := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			candidates = append(candidates, id)
		}
	}
	rows.Close()
	for _, id := range candidates {
		rule, exists := rules[id]
		if !exists {
			continue
		}
		inFlight, lastActivity = HybridBusinessActivity(rule.GroupID)
		sharedInFlight = 0
		sharedLast = sql.NullTime{}
		if err := s.db.QueryRowContext(parent, `SELECT COALESCE(SUM(in_flight),0),MAX(last_activity_at)
 FROM channel_monitor_hybrid_activity WHERE group_id=$1 AND heartbeat_at>NOW()-INTERVAL '15 seconds'`, rule.GroupID).Scan(&sharedInFlight, &sharedLast); err == nil {
			if sharedInFlight > inFlight {
				inFlight = sharedInFlight
			}
			if sharedLast.Valid && sharedLast.Time.After(lastActivity) {
				lastActivity = sharedLast.Time
			}
		}
		if !shouldScheduleHybridProbe(inFlight, lastActivity, currentMinute) {
			continue
		}
		result, claimErr := s.db.ExecContext(parent, `UPDATE channel_monitor_hybrid_cursors SET last_probe_at=NOW(),probe_lease_until=NOW()+INTERVAL '55 seconds'
 WHERE rule_id=$1 AND (probe_lease_until IS NULL OR probe_lease_until<NOW())`, id)
		if claimErr != nil {
			continue
		}
		claimed, _ := result.RowsAffected()
		if claimed == 0 {
			continue
		}
		s.probeSlots <- struct{}{}
		s.probeWorkers.Add(1)
		go s.executeProbe(parent, cfg, rule)
	}
}

func shouldScheduleHybridProbe(inFlight int64, lastActivity, currentMinute time.Time) bool {
	return inFlight == 0 && (lastActivity.IsZero() || lastActivity.Before(currentMinute))
}

func (s *HybridMonitorService) executeProbe(parent context.Context, cfg HybridMonitorConfig, rule HybridMonitorRule) {
	defer s.probeWorkers.Done()
	defer func() { <-s.probeSlots }()
	defer func() {
		_, _ = s.db.ExecContext(context.Background(), "UPDATE channel_monitor_hybrid_cursors SET probe_lease_until=NULL WHERE rule_id=$1", rule.ID)
	}()
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	monitor, _, err := s.target(ctx, rule)
	if err != nil {
		return
	}
	target := *monitor
	target.Endpoint = s.origin
	point := runHybridProbe(ctx, &target, rule.Model, s.client)
	point.ObservedAt = time.Now().UTC()
	if point.Status == "operational" {
		point = evaluateHybridMinute(rule, point)
	}
	if err = s.persistProbe(ctx, cfg, rule, point); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("hybrid monitor probe persistence failed", "rule_id", rule.ID, "error", err)
	}
}

func (s *HybridMonitorService) Snapshot(ctx context.Context, admin bool, allowed []int64) (HybridMonitorConfig, []HybridMonitorItem, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cfg, err := s.Config(ctx)
	items := []HybridMonitorItem{}
	if err != nil {
		return cfg, items, err
	}
	if !admin && !cfg.Enabled {
		return HybridMonitorConfig{Enabled: false}, items, nil
	}
	visible := make([]HybridMonitorRule, 0, len(cfg.Rules))
	ids := []string{}
	for _, rule := range cfg.Rules {
		if !admin {
			permitted := false
			for _, groupID := range allowed {
				permitted = permitted || groupID == rule.GroupID
			}
			if !rule.Public || !permitted {
				continue
			}
		}
		visible = append(visible, rule)
		ids = append(ids, rule.ID)
	}
	minutes := make(map[string][]HybridMonitorMinute, len(ids))
	if len(ids) > 0 {
		rows, queryErr := s.db.QueryContext(ctx, `SELECT targets.rule_id, recent.result FROM unnest($1::text[]) AS targets(rule_id)
 CROSS JOIN LATERAL (SELECT result FROM channel_monitor_hybrid_minutes WHERE rule_id=targets.rule_id AND minute>=NOW()-INTERVAL '65 minutes' ORDER BY minute DESC LIMIT 60) AS recent`, pq.Array(ids))
		if queryErr != nil {
			return cfg, items, queryErr
		}
		for rows.Next() {
			var ruleID string
			var raw []byte
			var point HybridMonitorMinute
			if err = rows.Scan(&ruleID, &raw); err != nil {
				rows.Close()
				return cfg, items, err
			}
			if err = json.Unmarshal(raw, &point); err != nil {
				rows.Close()
				return cfg, items, err
			}
			if !admin {
				point.Requests = 0
				point.Successes = 0
				point.Failures = 0
				point.Excluded = 0
				point.TTFTSamples = 0
				point.AlertSuccessRate = nil
				point.Detail = ""
			}
			minutes[ruleID] = append(minutes[ruleID], point)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return cfg, items, err
		}
	}
	for _, rule := range visible {
		points := minutes[rule.ID]
		if points == nil {
			points = []HybridMonitorMinute{}
		}
		sort.Slice(points, func(left, right int) bool { return points[left].Minute.After(points[right].Minute) })
		item := HybridMonitorItem{Rule: rule, Minutes: points}
		if admin {
			var raw []byte
			stateErr := s.db.QueryRowContext(ctx, "SELECT state FROM channel_monitor_hybrid_states WHERE rule_id=$1", rule.ID).Scan(&raw)
			if stateErr == nil {
				var state HybridMonitorState
				if json.Unmarshal(raw, &state) == nil {
					item.State = &state
				}
			}
		} else {
			item.Rule = HybridMonitorRule{ID: rule.ID, Name: rule.Name, Model: rule.Model, Enabled: rule.Enabled}
		}
		items = append(items, item)
	}
	if !admin {
		cfg = HybridMonitorConfig{Enabled: cfg.Enabled}
	}
	return cfg, items, nil
}
