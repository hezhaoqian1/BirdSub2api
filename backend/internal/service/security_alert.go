package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// 安全告警：异常的管理员登录、陌生网络访问后台、敏感管理操作、异常用量。
//
// 检测分两条路径：
//   - 实时：审计记录（AuditLogService.Record）与管理员鉴权成功（admin auth 中间件）
//     会推送到本实例的观察队列，由本实例的 worker 判定后写入 security_alert_events；
//   - 定时：抢到 leader 锁的实例按周期统计登录失败、用量异常、注册突增、每日日报。
//
// 所有实例写入同一张 outbox 表并按 dedupe_key 去重，由 leader 实例统一投递到企业微信
// 群机器人，失败按指数退避重试。

const (
	securityAlertLeaderLockKey    = "security:alert:leader"
	securityAlertLeaderLockTTL    = 2 * time.Minute
	securityAlertTickInterval     = 15 * time.Second
	securityAlertTickTimeout      = 90 * time.Second
	securityAlertLoginCheckEvery  = time.Minute
	securityAlertUsageCheckEvery  = 5 * time.Minute
	securityAlertUsageBaselineTTL = time.Hour
	securityAlertCleanupEvery     = 24 * time.Hour
	securityAlertBaselineTTL      = 5 * time.Minute
	securityAlertConfigTTL        = 30 * time.Second
	securityAlertLocalSeenTTL     = 10 * time.Minute
	securityAlertQueueSize        = 1024
	securityAlertBaselineWindow   = 90 * 24 * time.Hour
	securityAlertRetention        = 90 * 24 * time.Hour
	securityAlertMaxAttempts      = 10
)

// SecurityAlertSeverity 告警级别。
type SecurityAlertSeverity string

const (
	SecurityAlertCritical SecurityAlertSeverity = "critical"
	SecurityAlertWarning  SecurityAlertSeverity = "warning"
	SecurityAlertInfo     SecurityAlertSeverity = "info"
)

// Event kinds that are always delivered, even while alerts are disabled, so that
// turning alerts off or swapping the robot cannot happen silently.
const (
	securityAlertKindConfig = "alert_config"
	securityAlertKindTest   = "test"
	securityAlertKindDigest = "daily_digest"
)

// SecurityAlertConfig 安全告警配置。阈值 <= 0 表示关闭对应规则。
type SecurityAlertConfig struct {
	Enabled bool `json:"enabled"`
	// TrustedCIDRs 额外信任的网段（如自己的手机运营商网段），命中则不视为陌生网络。
	TrustedCIDRs []string `json:"trusted_cidrs"`
	// BalanceAdjustThreshold 后台单次调整余额达到该金额时告警。
	BalanceAdjustThreshold float64 `json:"balance_adjust_threshold"`
	// AdminNewIPNotify 管理员从熟悉网段里的新 IP 登录时也发通知（陌生网段始终告警）。
	AdminNewIPNotify bool `json:"admin_new_ip_notify"`
	// AdminLoginFailThreshold 15 分钟内管理员账号登录失败次数阈值。
	AdminLoginFailThreshold int `json:"admin_login_fail_threshold"`
	// IPLoginFailThreshold 15 分钟内单个 IP 登录失败次数阈值。
	IPLoginFailThreshold int `json:"ip_login_fail_threshold"`
	// SpendSpikeFloor 用户近 1 小时消费低于该值时不判定为异常。
	SpendSpikeFloor float64 `json:"spend_spike_floor"`
	// SpendSpikeMultiplier 用户近 1 小时消费超过其过去 7 天单小时最高值的倍数。
	SpendSpikeMultiplier float64 `json:"spend_spike_multiplier"`
	// KeyIPSpreadThreshold 单个 API Key 近 1 小时出现的不同 IP 数阈值。
	KeyIPSpreadThreshold int `json:"key_ip_spread_threshold"`
	// SignupBurstThreshold 近 1 小时新注册用户数阈值。
	SignupBurstThreshold int `json:"signup_burst_threshold"`
	// DailyDigestHour 每日日报发送的北京时间小时（0-23），-1 关闭。
	DailyDigestHour int `json:"daily_digest_hour"`
}

// DefaultSecurityAlertConfig 返回默认配置。
func DefaultSecurityAlertConfig() SecurityAlertConfig {
	return SecurityAlertConfig{
		Enabled:                 true,
		TrustedCIDRs:            []string{},
		BalanceAdjustThreshold:  500,
		AdminNewIPNotify:        true,
		AdminLoginFailThreshold: 3,
		IPLoginFailThreshold:    20,
		SpendSpikeFloor:         100,
		SpendSpikeMultiplier:    3,
		KeyIPSpreadThreshold:    20,
		SignupBurstThreshold:    10,
		DailyDigestHour:         9,
	}
}

// parseTrustedCIDRs 校验并解析信任网段；单个 IP 视为 /32 或 /128。
func parseTrustedCIDRs(values []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if !strings.Contains(value, "/") {
			ip := net.ParseIP(value)
			if ip == nil {
				return nil, fmt.Errorf("invalid trusted CIDR %q", value)
			}
			if ip.To4() != nil {
				value += "/32"
			} else {
				value += "/128"
			}
		}
		_, ipNet, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted CIDR %q", raw)
		}
		nets = append(nets, ipNet)
	}
	return nets, nil
}

// Validate 校验配置取值范围。
func (c SecurityAlertConfig) Validate() error {
	if _, err := parseTrustedCIDRs(c.TrustedCIDRs); err != nil {
		return err
	}
	if c.DailyDigestHour < -1 || c.DailyDigestHour > 23 {
		return errors.New("daily_digest_hour must be between -1 and 23")
	}
	if c.BalanceAdjustThreshold < 0 || c.SpendSpikeFloor < 0 || c.SpendSpikeMultiplier < 0 {
		return errors.New("thresholds must not be negative")
	}
	return nil
}

// securityNetworkPrefix 把 IP 归并到网段（IPv4 /24、IPv6 /48），代理节点同网段轮换不重复告警。
func securityNetworkPrefix(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])
	}
	return (&net.IPNet{IP: ip.Mask(net.CIDRMask(48, 128)), Mask: net.CIDRMask(48, 128)}).String()
}

// SecurityAdminAccess 一次通过鉴权的管理员请求。
type SecurityAdminAccess struct {
	UserID     int64
	Email      string
	ClientIP   string
	UserAgent  string
	AuthMethod string
	Path       string
	At         time.Time
}

// SecurityAlertEvent 一条已记录的安全告警。
type SecurityAlertEvent struct {
	ID        int64                 `json:"id"`
	Kind      string                `json:"kind"`
	Severity  SecurityAlertSeverity `json:"severity"`
	Title     string                `json:"title"`
	Body      string                `json:"body"`
	CreatedAt time.Time             `json:"created_at"`
	SentAt    *time.Time            `json:"sent_at,omitempty"`
	Attempts  int                   `json:"attempts"`
	LastError string                `json:"last_error,omitempty"`
}

// securityAlertDraft 待写入 outbox 的告警。
type securityAlertDraft struct {
	DedupeKey string
	Kind      string
	Severity  SecurityAlertSeverity
	Title     string
	Lines     []string
}

func (d securityAlertDraft) body() string {
	lines := make([]string, 0, len(d.Lines))
	for _, line := range d.Lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

type securityObservation struct {
	audit  *AuditLog
	access *SecurityAdminAccess
}

type securityAlertSender interface {
	Send(ctx context.Context, webhook, content string) error
}

// securityRuleSnapshot 规则判定所需的只读快照。
type securityRuleSnapshot struct {
	cfg           SecurityAlertConfig
	trusted       []*net.IPNet
	knownPrefixes map[string]struct{}
	knownIPs      map[string]struct{}
	adminEmails   map[string]struct{}
	baselineReady bool
	loc           *time.Location
}

// isKnownNetwork 判断 IP 是否属于信任网段或近 90 天管理员用过的网段。
func (s securityRuleSnapshot) isKnownNetwork(rawIP string) bool {
	ip := net.ParseIP(strings.TrimSpace(rawIP))
	if ip == nil {
		return true // 无法判定时不告警
	}
	for _, n := range s.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	if !s.baselineReady {
		return true // 基线未加载时宁可漏报也不刷屏
	}
	_, ok := s.knownPrefixes[securityNetworkPrefix(rawIP)]
	return ok
}

// isKnownIP 判断该 IP 本身近 90 天是否被管理员用过（比网段更细）。
func (s securityRuleSnapshot) isKnownIP(rawIP string) bool {
	ip := net.ParseIP(strings.TrimSpace(rawIP))
	if ip == nil || !s.baselineReady {
		return true
	}
	for _, n := range s.trusted {
		if n.Contains(ip) {
			return true // 信任网段（如手机流量）IP 经常变，不逐个提醒
		}
	}
	_, ok := s.knownIPs[ip.String()]
	return ok
}

func (s securityRuleSnapshot) isAdminEmail(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	_, ok := s.adminEmails[email]
	return ok
}

// SecurityAlertService 安全告警服务。
type SecurityAlertService struct {
	db         *sql.DB
	settings   *SettingService
	lockCache  LeaderLockCache
	sender     securityAlertSender
	instanceID string
	loc        *time.Location
	now        func() time.Time

	observeCh chan securityObservation
	stopCh    chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup

	mu               sync.Mutex
	cfg              SecurityAlertConfig
	cfgNets          []*net.IPNet
	webhook          string
	cfgLoadedAt      time.Time
	knownPrefixes    map[string]struct{}
	knownIPs         map[string]struct{}
	adminEmails      map[string]struct{}
	baselineReady    bool
	baselineLoadedAt time.Time
	localSeen        map[string]time.Time

	// 以下字段只在定时 goroutine 内读写。
	lastLoginCheck    time.Time
	lastUsageCheck    time.Time
	lastCleanup       time.Time
	userSpendBaseline map[int64]float64
	keyIPBaseline     map[int64]int
	usageBaselineAt   time.Time
}

// NewSecurityAlertService 创建安全告警服务。
func NewSecurityAlertService(db *sql.DB, settings *SettingService, lockCache LeaderLockCache) *SecurityAlertService {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return &SecurityAlertService{
		db:         db,
		settings:   settings,
		lockCache:  lockCache,
		sender:     newWeComRobotSender(),
		instanceID: uuid.NewString(),
		loc:        loc,
		now:        time.Now,
		observeCh:  make(chan securityObservation, securityAlertQueueSize),
		stopCh:     make(chan struct{}),
		cfg:        DefaultSecurityAlertConfig(),
		localSeen:  map[string]time.Time{},
	}
}

// Start 启动观察 worker 与定时任务。
func (s *SecurityAlertService) Start() {
	if s == nil || s.db == nil {
		return
	}
	s.wg.Add(2)
	go s.runObserver()
	go s.runTicker()
}

// Stop 停止服务。
func (s *SecurityAlertService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

// OnAuditRecord 接收一条审计记录（非阻塞，队列满时丢弃）。
func (s *SecurityAlertService) OnAuditRecord(entry *AuditLog) {
	if s == nil || entry == nil {
		return
	}
	copied := *entry
	s.enqueue(securityObservation{audit: &copied})
}

// ObserveAdminAccess 接收一次通过鉴权的管理员请求（非阻塞）。
// 同一网段在本实例 10 分钟内只判定一次，避免每个请求都进入队列。
func (s *SecurityAlertService) ObserveAdminAccess(access SecurityAdminAccess) {
	if s == nil {
		return
	}
	prefix := securityNetworkPrefix(access.ClientIP)
	if prefix == "" {
		return
	}
	now := s.now()
	s.mu.Lock()
	if seen, ok := s.localSeen[prefix]; ok && now.Sub(seen) < securityAlertLocalSeenTTL {
		s.mu.Unlock()
		return
	}
	s.localSeen[prefix] = now
	if len(s.localSeen) > 4096 {
		for k, v := range s.localSeen {
			if now.Sub(v) >= securityAlertLocalSeenTTL {
				delete(s.localSeen, k)
			}
		}
	}
	s.mu.Unlock()
	if access.At.IsZero() {
		access.At = now
	}
	s.enqueue(securityObservation{access: &access})
}

func (s *SecurityAlertService) enqueue(obs securityObservation) {
	select {
	case s.observeCh <- obs:
	default:
		slog.Warn("[SecurityAlert] observation queue full, dropping event")
	}
}

func (s *SecurityAlertService) runObserver() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case obs := <-s.observeCh:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.handleObservation(ctx, obs)
			cancel()
		}
	}
}

func (s *SecurityAlertService) handleObservation(ctx context.Context, obs securityObservation) {
	snap := s.snapshot(ctx)
	if !snap.cfg.Enabled {
		return
	}
	var drafts []securityAlertDraft
	if obs.access != nil {
		if d, ok := evaluateAdminAccess(snap, *obs.access); ok {
			drafts = append(drafts, d)
		}
	}
	if obs.audit != nil {
		drafts = append(drafts, evaluateAuditRecord(snap, obs.audit)...)
	}
	for _, d := range drafts {
		if err := s.emit(ctx, d); err != nil {
			slog.Warn("[SecurityAlert] failed to record alert", "kind", d.Kind, "error", err)
		}
	}
}

// snapshot 读取（带缓存的）配置与管理员网段基线。
func (s *SecurityAlertService) snapshot(ctx context.Context) securityRuleSnapshot {
	s.refreshConfig(ctx, false)
	s.refreshBaseline(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return securityRuleSnapshot{
		cfg:           s.cfg,
		trusted:       s.cfgNets,
		knownPrefixes: s.knownPrefixes,
		knownIPs:      s.knownIPs,
		adminEmails:   s.adminEmails,
		baselineReady: s.baselineReady,
		loc:           s.loc,
	}
}

func (s *SecurityAlertService) refreshConfig(ctx context.Context, force bool) {
	s.mu.Lock()
	fresh := !force && !s.cfgLoadedAt.IsZero() && s.now().Sub(s.cfgLoadedAt) < securityAlertConfigTTL
	s.mu.Unlock()
	if fresh || s.settings == nil || s.settings.settingRepo == nil {
		return
	}
	cfg, webhook, err := s.loadConfig(ctx)
	if err != nil {
		slog.Warn("[SecurityAlert] failed to load config", "error", err)
		return
	}
	nets, err := parseTrustedCIDRs(cfg.TrustedCIDRs)
	if err != nil {
		slog.Warn("[SecurityAlert] invalid trusted CIDRs ignored", "error", err)
		nets = nil
	}
	s.mu.Lock()
	s.cfg = cfg
	s.cfgNets = nets
	s.webhook = webhook
	s.cfgLoadedAt = s.now()
	s.mu.Unlock()
}

func (s *SecurityAlertService) loadConfig(ctx context.Context) (SecurityAlertConfig, string, error) {
	cfg := DefaultSecurityAlertConfig()
	vals, err := s.settings.settingRepo.GetMultiple(ctx, []string{
		SettingKeySecurityAlertConfig,
		SettingKeySecurityAlertWeComWebhook,
	})
	if err != nil {
		return cfg, "", err
	}
	if raw := strings.TrimSpace(vals[SettingKeySecurityAlertConfig]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return DefaultSecurityAlertConfig(), "", fmt.Errorf("decode security alert config: %w", err)
		}
	}
	if cfg.TrustedCIDRs == nil {
		cfg.TrustedCIDRs = []string{}
	}
	return cfg, strings.TrimSpace(vals[SettingKeySecurityAlertWeComWebhook]), nil
}

// refreshBaseline 加载近 90 天管理员使用过的网段与管理员邮箱。
func (s *SecurityAlertService) refreshBaseline(ctx context.Context) {
	s.mu.Lock()
	fresh := s.baselineReady && s.now().Sub(s.baselineLoadedAt) < securityAlertBaselineTTL
	s.mu.Unlock()
	if fresh || s.db == nil {
		return
	}
	prefixes := map[string]struct{}{}
	ips := map[string]struct{}{}
	remember := func(raw string) {
		if p := securityNetworkPrefix(raw); p != "" {
			prefixes[p] = struct{}{}
			ips[net.ParseIP(strings.TrimSpace(raw)).String()] = struct{}{}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT client_ip FROM audit_logs
 WHERE created_at > $1 AND actor_role = 'admin' AND client_ip <> ''`, s.now().Add(-securityAlertBaselineWindow))
	if err != nil {
		slog.Warn("[SecurityAlert] failed to load admin network baseline", "error", err)
		return
	}
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) == nil {
			remember(ip)
		}
	}
	_ = rows.Close()

	emails := map[string]struct{}{}
	rows, err = s.db.QueryContext(ctx, `SELECT LOWER(email) FROM users WHERE role = 'admin' AND deleted_at IS NULL`)
	if err != nil {
		slog.Warn("[SecurityAlert] failed to load admin emails", "error", err)
		return
	}
	for rows.Next() {
		var email string
		if rows.Scan(&email) == nil && email != "" {
			emails[email] = struct{}{}
		}
	}
	_ = rows.Close()

	// 管理员成功登录过的网络也算熟悉网段（例如只在家里登录、换代理后再操作后台）。
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT client_ip, LOWER(COALESCE(substring(request_body from '"email":"([^"]*)"'), ''))
 FROM audit_logs
 WHERE created_at > $1 AND action IN ('auth.login', 'auth.login.2fa')
   AND status_code BETWEEN 200 AND 299 AND client_ip <> ''`, s.now().Add(-securityAlertBaselineWindow))
	if err != nil {
		slog.Warn("[SecurityAlert] failed to load admin login baseline", "error", err)
		return
	}
	for rows.Next() {
		var ip, email string
		if rows.Scan(&ip, &email) != nil {
			continue
		}
		if _, ok := emails[email]; ok {
			remember(ip)
		}
	}
	_ = rows.Close()

	s.mu.Lock()
	s.knownPrefixes = prefixes
	s.knownIPs = ips
	s.adminEmails = emails
	s.baselineReady = true
	s.baselineLoadedAt = s.now()
	s.mu.Unlock()
}

// emit 写入 outbox；dedupe_key 冲突时忽略。
func (s *SecurityAlertService) emit(ctx context.Context, d securityAlertDraft) error {
	if s.db == nil {
		return nil
	}
	if d.DedupeKey == "" {
		d.DedupeKey = d.Kind + ":" + uuid.NewString()
	}
	if len(d.DedupeKey) > 200 {
		d.DedupeKey = d.DedupeKey[:200]
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO security_alert_events(dedupe_key, kind, severity, title, body)
 VALUES ($1, $2, $3, $4, $5) ON CONFLICT (dedupe_key) DO NOTHING`,
		d.DedupeKey, d.Kind, string(d.Severity), truncateRunes(d.Title, 200), d.body())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Warn("[SecurityAlert] alert recorded", "kind", d.Kind, "severity", d.Severity, "title", d.Title)
	}
	return nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// ListEvents 返回最近的告警记录。
func (s *SecurityAlertService) ListEvents(ctx context.Context, limit int) ([]SecurityAlertEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, severity, title, body, created_at, sent_at, attempts, last_error
 FROM security_alert_events ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := []SecurityAlertEvent{}
	for rows.Next() {
		var ev SecurityAlertEvent
		var severity string
		var sentAt sql.NullTime
		if err := rows.Scan(&ev.ID, &ev.Kind, &severity, &ev.Title, &ev.Body, &ev.CreatedAt, &sentAt, &ev.Attempts, &ev.LastError); err != nil {
			return nil, err
		}
		ev.Severity = SecurityAlertSeverity(severity)
		if sentAt.Valid {
			t := sentAt.Time
			ev.SentAt = &t
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// SecurityAlertConfigView 返回给管理端的配置视图（webhook 只给掩码）。
type SecurityAlertConfigView struct {
	Config            SecurityAlertConfig `json:"config"`
	WebhookConfigured bool                `json:"wecom_webhook_configured"`
	WebhookMasked     string              `json:"wecom_webhook_masked"`
}

// GetConfigView 读取当前配置。
func (s *SecurityAlertService) GetConfigView(ctx context.Context) (SecurityAlertConfigView, error) {
	cfg, webhook, err := s.loadConfig(ctx)
	if err != nil {
		return SecurityAlertConfigView{}, err
	}
	return SecurityAlertConfigView{
		Config:            cfg,
		WebhookConfigured: webhook != "",
		WebhookMasked:     MaskWeComRobotWebhook(webhook),
	}, nil
}

// UpdateSecurityAlertConfigInput 更新配置的入参；nil 字段保持不变。
type UpdateSecurityAlertConfigInput struct {
	// ConfigPatch 部分配置 JSON，合并到当前配置上。
	ConfigPatch json.RawMessage
	// WeComWebhook 新的机器人地址；空字符串表示清除。
	WeComWebhook *string
	// Operator 操作人（用于告警文案）。
	Operator string
	ClientIP string
}

// UpdateConfig 更新配置。更换或清除机器人时先通知旧机器人，防止告警通道被悄悄改走。
func (s *SecurityAlertService) UpdateConfig(ctx context.Context, in UpdateSecurityAlertConfigInput) (SecurityAlertConfigView, error) {
	cfg, oldWebhook, err := s.loadConfig(ctx)
	if err != nil {
		return SecurityAlertConfigView{}, err
	}
	oldEnabled := cfg.Enabled
	if len(in.ConfigPatch) > 0 && string(in.ConfigPatch) != "null" {
		if err := json.Unmarshal(in.ConfigPatch, &cfg); err != nil {
			return SecurityAlertConfigView{}, fmt.Errorf("invalid config: %w", err)
		}
	}
	if cfg.TrustedCIDRs == nil {
		cfg.TrustedCIDRs = []string{}
	}
	if err := cfg.Validate(); err != nil {
		return SecurityAlertConfigView{}, err
	}
	newWebhook := oldWebhook
	if in.WeComWebhook != nil {
		newWebhook = strings.TrimSpace(*in.WeComWebhook)
		if newWebhook != "" {
			if err := ValidateWeComRobotWebhook(newWebhook); err != nil {
				return SecurityAlertConfigView{}, err
			}
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return SecurityAlertConfigView{}, err
	}

	webhookChanged := newWebhook != oldWebhook
	changes := []string{}
	if oldEnabled != cfg.Enabled {
		if cfg.Enabled {
			changes = append(changes, "安全告警已开启")
		} else {
			changes = append(changes, "安全告警已关闭")
		}
	}
	if webhookChanged {
		if newWebhook == "" {
			changes = append(changes, "企业微信机器人已被清除")
		} else {
			changes = append(changes, "企业微信机器人已更换为 "+MaskWeComRobotWebhook(newWebhook))
		}
	}
	if len(changes) == 0 {
		changes = append(changes, "告警阈值或信任网段已更新")
	}
	draft := securityAlertDraft{
		DedupeKey: securityAlertKindConfig + ":" + uuid.NewString(),
		Kind:      securityAlertKindConfig,
		Severity:  SecurityAlertWarning,
		Title:     "安全告警配置被修改",
		Lines: append(changes,
			"操作人："+sanitizeSecurityText(in.Operator, 80),
			"来源："+formatSecurityIP(in.ClientIP),
			"时间："+s.now().In(s.loc).Format("2006-01-02 15:04:05"),
			"如果不是你本人操作，请立即检查管理员账号。"),
	}
	if webhookChanged && oldWebhook != "" && s.sender != nil {
		// 旧机器人同步收到一条通知（尽力而为）。
		notice := renderSecurityAlertMessage(s.siteName(ctx), []pendingSecurityAlert{{
			Severity: draft.Severity, Title: draft.Title, Body: draft.body(),
		}})
		if err := s.sender.Send(ctx, oldWebhook, notice); err != nil {
			slog.Warn("[SecurityAlert] failed to notify previous robot", "error", err)
		}
	}

	if err := s.settings.settingRepo.SetMultiple(ctx, map[string]string{
		SettingKeySecurityAlertConfig:       string(raw),
		SettingKeySecurityAlertWeComWebhook: newWebhook,
	}); err != nil {
		return SecurityAlertConfigView{}, err
	}
	s.refreshConfig(ctx, true)
	if err := s.emit(ctx, draft); err != nil {
		slog.Warn("[SecurityAlert] failed to record config change", "error", err)
	}
	return SecurityAlertConfigView{
		Config:            cfg,
		WebhookConfigured: newWebhook != "",
		WebhookMasked:     MaskWeComRobotWebhook(newWebhook),
	}, nil
}

// SendTest 立即发送一条测试消息；webhookOverride 为空时使用已保存的机器人。
func (s *SecurityAlertService) SendTest(ctx context.Context, webhookOverride string) error {
	webhook := strings.TrimSpace(webhookOverride)
	if webhook == "" {
		_, saved, err := s.loadConfig(ctx)
		if err != nil {
			return err
		}
		webhook = saved
	}
	if webhook == "" {
		return errors.New("wecom webhook is not configured")
	}
	if err := ValidateWeComRobotWebhook(webhook); err != nil {
		return err
	}
	msg := renderSecurityAlertMessage(s.siteName(ctx), []pendingSecurityAlert{{
		Severity: SecurityAlertInfo,
		Title:    "安全告警测试",
		Body: securityAlertDraft{Lines: []string{
			"企业微信机器人配置有效，安全告警会推送到这里。",
			"时间：" + s.now().In(s.loc).Format("2006-01-02 15:04:05"),
		}}.body(),
	}})
	return s.sender.Send(ctx, webhook, msg)
}

func (s *SecurityAlertService) siteName(ctx context.Context) string {
	if s.settings != nil {
		if name := strings.TrimSpace(s.settings.GetSiteName(ctx)); name != "" {
			return sanitizeSecurityText(name, 40)
		}
	}
	return "Sub2API"
}
