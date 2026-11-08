package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// 渠道监控失败告警的补充说明：把报错翻成一句中文原因，并回查这几次检测落在了哪个渠道。
// 只在推送时计算、只写进推送文本，页面和库表都不变。
//
// 监控打的是本站网关时（监控用的 API Key 能在 api_keys 里找到），网关自己记着每次请求
// 选了哪个上游账号：已结束的在 ops_error_logs（含每次失败转移）和 usage_logs 里，
// 还没结束的在 Redis 并发槽位详情里。监控等 30 秒没响应头就放弃，网关却会继续跑完，
// 所以推送那一刻最后一次失败通常还"仍在处理"。打的是外部地址时只能给出域名。

const (
	channelMonitorTraceTimeout     = 5 * time.Second
	channelMonitorTraceLookback    = 5 * time.Second
	channelMonitorTraceRowLimit    = 100
	channelMonitorTraceMaxAccounts = 5
	channelMonitorTraceDetailRunes = 40
)

type channelMonitorLookup interface {
	Get(ctx context.Context, id int64) (*ChannelMonitor, error)
}

type channelMonitorSlotLister interface {
	ListAllAccountSlotHolders(ctx context.Context) (*ActiveSlotSnapshot, error)
}

// channelMonitorReasonLine 汇总这轮几次失败的原因，同一原因合并计数。
func channelMonitorReasonLine(alert channelMonitorFailureAlert) string {
	samples := alert.Failures
	if len(samples) == 0 {
		samples = []channelMonitorFailureSample{{Status: alert.Status, Message: alert.Message, CheckedAt: alert.CheckedAt}}
	}
	var order []string
	counts := map[string]int{}
	for _, sample := range samples {
		reason := channelMonitorFailureReason(sample.Status, sample.Message)
		if counts[reason] == 0 {
			order = append(order, reason)
		}
		counts[reason]++
	}
	if len(order) == 1 {
		if len(samples) > 1 {
			return fmt.Sprintf("原因（%d 次相同）：%s", len(samples), order[0])
		}
		return "原因：" + order[0]
	}
	parts := make([]string, 0, len(order))
	for _, reason := range order {
		parts = append(parts, fmt.Sprintf("%s ×%d", reason, counts[reason]))
	}
	return "原因：" + strings.Join(parts, "；")
}

var channelMonitorHTTPStatusPattern = regexp.MustCompile(`^upstream http (\d{3})\b`)

// channelMonitorFailureReason 把监控的一条报错翻成中文原因（不含原文，原文另起一行）。
func channelMonitorFailureReason(status, message string) string {
	msg := strings.ToLower(strings.TrimSpace(message))
	switch {
	case msg == "":
		return "没有返回错误详情"
	case strings.HasPrefix(msg, "challenge mismatch"):
		return "回答不对（有返回内容但算术题答错，可能被降级、拦截或换了模型）"
	case strings.Contains(msg, "returned 2xx with empty text"):
		return "返回 200 但内容为空"
	case strings.HasPrefix(msg, "slow response"):
		return "响应太慢"
	case strings.Contains(msg, "timeout awaiting response headers"):
		return fmt.Sprintf("%d 秒内没等到响应头（请求已发出，对方一直没回）", int(monitorResponseHeaderTimeout/time.Second))
	}
	if m := channelMonitorHTTPStatusPattern.FindStringSubmatch(msg); m != nil {
		code, _ := strconv.Atoi(m[1])
		return fmt.Sprintf("HTTP %d，%s", code, channelMonitorHTTPReason(code, msg))
	}
	if reason := channelMonitorNetworkReason(msg); reason != "" {
		return reason
	}
	if status == MonitorStatusFailed {
		return "检测未通过"
	}
	return "其他错误"
}

func channelMonitorHTTPReason(code int, msg string) string {
	switch {
	case strings.Contains(msg, "no available account"):
		return "分组里没有可用账号"
	case strings.Contains(msg, "insufficient") || strings.Contains(msg, "余额") || strings.Contains(msg, "quota"):
		return "余额或额度不足"
	case strings.Contains(msg, "content_policy") || strings.Contains(msg, "prohibited use"):
		return "被内容审核拦截"
	case strings.Contains(msg, "invalid_credentials") || code == 401:
		return "鉴权失败（Key 或凭证无效）"
	case strings.Contains(msg, "stream_required") || strings.Contains(msg, "only accepts streaming"):
		return "只接受流式请求"
	}
	switch {
	case code == 403:
		return "拒绝访问"
	case code == 404:
		return "接口或模型不存在"
	case code == 429:
		return "被限流"
	case code == 524:
		return "Cloudflare 等源站太久后断开"
	case code == 502 || code == 503 || code == 504:
		return "上游服务暂时不可用"
	case code >= 500:
		return "上游服务内部错误"
	case code == 400:
		return "请求被拒（参数或内容不被接受）"
	default:
		return "请求失败"
	}
}

// channelMonitorNetworkReason 识别常见的网络层错误，认不出时返回空串。
func channelMonitorNetworkReason(msg string) string {
	msg = strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "timeout awaiting response headers"):
		return "等响应头超时"
	case strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "client.timeout"):
		return "请求整体超时"
	case strings.Contains(msg, "no such host"):
		return "域名解析失败"
	case strings.Contains(msg, "connection refused"):
		return "对方拒绝连接（服务没起或端口没开）"
	case strings.Contains(msg, "dial tcp") && strings.Contains(msg, "i/o timeout"):
		return "连接超时（连不上对方）"
	case strings.Contains(msg, "i/o timeout"):
		return "读写超时"
	case strings.Contains(msg, "connection reset by peer"):
		return "连接被对方重置"
	case strings.Contains(msg, "tls:") || strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"):
		return "TLS 握手或证书出错"
	case strings.Contains(msg, "goaway") || strings.Contains(msg, "stream error") || strings.Contains(msg, "http2:"):
		return "HTTP/2 连接被中断"
	case strings.Contains(msg, "eof"):
		return "连接被对方提前关闭"
	case strings.HasPrefix(msg, "read body"):
		return "读响应体出错"
	}
	return ""
}

// channelMonitorChannelLines 回查这几次失败检测落在哪个渠道，失败时给出一行说明而不是整段丢掉。
func (n *channelMonitorWeComNotifier) channelMonitorChannelLines(ctx context.Context, alert channelMonitorFailureAlert) []string {
	if n == nil || n.monitors == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, channelMonitorTraceTimeout)
	defer cancel()

	monitor, err := n.monitors.Get(ctx, alert.MonitorID)
	if err != nil || monitor == nil {
		slog.Warn("[SecurityAlert] channel monitor trace: load monitor failed", "monitor_id", alert.MonitorID, "error", err)
		return []string{"出错渠道：读取监控配置失败，没能回查"}
	}
	if defaultCheckMode(monitor.CheckMode) == MonitorCheckModeQuota {
		if monitor.AccountID != nil {
			return []string{fmt.Sprintf("出错渠道：关联账号 #%d（配额模式）", *monitor.AccountID)}
		}
		return []string{"出错渠道：配额模式，未关联账号"}
	}
	host := channelMonitorEndpointHost(monitor.Endpoint)

	key, found, err := n.lookupChannelMonitorAPIKey(ctx, monitor.APIKey)
	if err != nil {
		slog.Warn("[SecurityAlert] channel monitor trace: lookup api key failed", "monitor_id", alert.MonitorID, "error", err)
		return []string{"出错渠道：查询网关记录失败，没能回查"}
	}
	if !found {
		return []string{"出错渠道：外部地址 " + host + "（不经过本站网关，查不到它背后的上游账号）"}
	}

	model := strings.TrimSpace(monitor.PrimaryModel)
	if model == "" {
		model = alert.Model
	}
	since := channelMonitorTraceSince(alert)
	lines := []string{}
	if key.groupID > 0 {
		lines = append(lines, fmt.Sprintf("分组：%s（#%d）", sanitizeSecurityText(key.groupName, 60), key.groupID))
	}
	trace, err := n.collectChannelMonitorTrace(ctx, key.id, model, since)
	if err != nil {
		slog.Warn("[SecurityAlert] channel monitor trace: query gateway records failed", "monitor_id", alert.MonitorID, "error", err)
		return append(lines, "出错渠道：查询网关记录失败，没能回查")
	}
	if trace.empty() {
		return append(lines, "出错渠道：网关里还没有这几次请求的记录（可能没到网关，或刚发出还在路上）")
	}
	return append(lines, trace.render()...)
}

func channelMonitorEndpointHost(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return sanitizeSecurityText(u.Host, 100)
	}
	return sanitizeSecurityText(endpoint, 100)
}

// channelMonitorTraceSince 回查窗口从这轮最早一次失败检测发起前一点开始。
func channelMonitorTraceSince(alert channelMonitorFailureAlert) time.Time {
	earliest := alert.CheckedAt
	for _, sample := range alert.Failures {
		if !sample.CheckedAt.IsZero() && (earliest.IsZero() || sample.CheckedAt.Before(earliest)) {
			earliest = sample.CheckedAt
		}
	}
	if earliest.IsZero() {
		earliest = time.Now()
	}
	return earliest.Add(-channelMonitorTraceLookback)
}

type channelMonitorGatewayKey struct {
	id        int64
	groupID   int64
	groupName string
}

func (n *channelMonitorWeComNotifier) lookupChannelMonitorAPIKey(ctx context.Context, plainKey string) (channelMonitorGatewayKey, bool, error) {
	var key channelMonitorGatewayKey
	plainKey = strings.TrimSpace(plainKey)
	if plainKey == "" {
		return key, false, nil
	}
	err := n.alerts.db.QueryRowContext(ctx, `SELECT k.id, COALESCE(k.group_id, 0), COALESCE(g.name, '')
 FROM api_keys k LEFT JOIN groups g ON g.id = k.group_id
 WHERE k.key = $1 AND k.deleted_at IS NULL LIMIT 1`, plainKey).Scan(&key.id, &key.groupID, &key.groupName)
	if errors.Is(err, sql.ErrNoRows) {
		return key, false, nil
	}
	if err != nil {
		return key, false, err
	}
	return key, true, nil
}

type channelMonitorUpstreamAttempt struct {
	AccountID          int64  `json:"account_id"`
	AccountName        string `json:"account_name"`
	UpstreamStatusCode int    `json:"upstream_status_code"`
	Message            string `json:"message"`
	Detail             string `json:"detail"`
}

func (n *channelMonitorWeComNotifier) collectChannelMonitorTrace(ctx context.Context, apiKeyID int64, model string, since time.Time) (*channelMonitorTrace, error) {
	trace := newChannelMonitorTrace()
	db := n.alerts.db

	rows, err := db.QueryContext(ctx, `SELECT COALESCE(e.account_id, 0), COALESCE(a.name, ''), e.status_code,
 COALESCE(e.upstream_status_code, 0), COALESCE(NULLIF(e.upstream_error_message, ''), e.error_message, ''),
 COALESCE(e.upstream_errors::text, '')
 FROM ops_error_logs e LEFT JOIN accounts a ON a.id = e.account_id
 WHERE e.created_at >= $1 AND e.api_key_id = $2
   AND COALESCE(NULLIF(btrim(COALESCE(e.requested_model, '')), ''), COALESCE(e.model, '')) = $3
 ORDER BY e.created_at DESC LIMIT $4`, since, apiKeyID, model, channelMonitorTraceRowLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var accountID int64
		var accountName, message, attemptsJSON string
		var status, upstreamStatus int
		if err := rows.Scan(&accountID, &accountName, &status, &upstreamStatus, &message, &attemptsJSON); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var attempts []channelMonitorUpstreamAttempt
		if attemptsJSON != "" {
			_ = json.Unmarshal([]byte(attemptsJSON), &attempts)
		}
		if len(attempts) == 0 {
			// 没有逐次记录时用整行结果；status < 400 的行是失败转移后救回来的，成功那次在 usage_logs 里。
			if status >= 400 || upstreamStatus > 0 {
				trace.account(accountID, accountName).addError(channelMonitorAttemptLabel(upstreamStatus, status, message))
			}
			continue
		}
		for _, attempt := range attempts {
			detail := attempt.Message
			if strings.TrimSpace(detail) == "" {
				detail = attempt.Detail
			}
			trace.account(attempt.AccountID, attempt.AccountName).addError(channelMonitorAttemptLabel(attempt.UpstreamStatusCode, 0, detail))
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.QueryContext(ctx, `SELECT COALESCE(u.account_id, 0), COALESCE(a.name, ''), COALESCE(u.duration_ms, 0)
 FROM usage_logs u LEFT JOIN accounts a ON a.id = u.account_id
 WHERE u.api_key_id = $1 AND u.created_at >= $2
   AND COALESCE(NULLIF(btrim(COALESCE(u.requested_model, '')), ''), COALESCE(u.model, '')) = $3
 ORDER BY u.created_at DESC LIMIT $4`, apiKeyID, since, model, channelMonitorTraceRowLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var accountID int64
		var accountName string
		var durationMs int
		if err := rows.Scan(&accountID, &accountName, &durationMs); err != nil {
			_ = rows.Close()
			return nil, err
		}
		trace.account(accountID, accountName).addSuccess(durationMs)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	n.addChannelMonitorInFlight(ctx, trace, apiKeyID, model)
	return trace, n.fillChannelMonitorAccountNames(ctx, trace)
}

// addChannelMonitorInFlight 从并发槽位详情里找这个 Key 还没结束的请求。只是补充信息，读不到就跳过。
func (n *channelMonitorWeComNotifier) addChannelMonitorInFlight(ctx context.Context, trace *channelMonitorTrace, apiKeyID int64, model string) {
	if n.slots == nil {
		return
	}
	snapshot, err := n.slots.ListAllAccountSlotHolders(ctx)
	if err != nil || snapshot == nil {
		if err != nil {
			slog.Warn("[SecurityAlert] channel monitor trace: list slots failed", "error", err)
		}
		return
	}
	for _, slot := range snapshot.Slots {
		if slot.Live || slot.Holder == nil || slot.Holder.APIKeyID != apiKeyID || slot.AccountID <= 0 {
			continue
		}
		if slot.Holder.Model != "" && model != "" && slot.Holder.Model != model {
			continue
		}
		age := int64(0)
		if snapshot.Now > 0 && slot.StartedAt > 0 && snapshot.Now > slot.StartedAt {
			age = snapshot.Now - slot.StartedAt
		}
		trace.account(slot.AccountID, "").addInFlight(age)
	}
}

func (n *channelMonitorWeComNotifier) fillChannelMonitorAccountNames(ctx context.Context, trace *channelMonitorTrace) error {
	var missing []int64
	for _, acc := range trace.accounts {
		if acc.id > 0 && acc.name == "" {
			missing = append(missing, acc.id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	rows, err := n.alerts.db.QueryContext(ctx, `SELECT id, name FROM accounts WHERE id = ANY($1)`, pq.Array(missing))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		if acc := trace.byID[id]; acc != nil {
			acc.name = name
		}
	}
	return rows.Err()
}

// channelMonitorAttemptLabel 网关一次上游尝试的简短说明，如「上游 403：Insufficient account balance」。
func channelMonitorAttemptLabel(upstreamStatus, status int, message string) string {
	if upstreamStatus > 0 {
		label := fmt.Sprintf("上游 %d", upstreamStatus)
		if detail := channelMonitorAttemptDetail(message); detail != "" {
			label += "：" + detail
		}
		return label
	}
	if reason := channelMonitorNetworkReason(message); reason != "" {
		return reason
	}
	if status > 0 {
		label := fmt.Sprintf("网关 %d", status)
		if detail := channelMonitorAttemptDetail(message); detail != "" {
			label += "：" + detail
		}
		return label
	}
	if detail := channelMonitorAttemptDetail(message); detail != "" {
		return detail
	}
	return "出错"
}

var channelMonitorBareErrorCodePattern = regexp.MustCompile(`(?i)^(error code:?\s*\d+|recovered upstream error\s*\d*)$`)

func channelMonitorAttemptDetail(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if message == "" || channelMonitorBareErrorCodePattern.MatchString(message) {
		return ""
	}
	return sanitizeSecurityText(message, channelMonitorTraceDetailRunes)
}

type channelMonitorAccountTrace struct {
	id   int64
	name string

	errorOrder  []string
	errorCounts map[string]int
	errorTotal  int

	// 网关最终成功的请求按监控等响应头的时限分两类：超时的（监控已放弃）和按时的。
	slowCount int
	slowMaxMs int
	okCount   int

	inFlight       int
	inFlightMaxAge int64
}

func (a *channelMonitorAccountTrace) addError(label string) {
	if a.errorCounts[label] == 0 {
		a.errorOrder = append(a.errorOrder, label)
	}
	a.errorCounts[label]++
	a.errorTotal++
}

func (a *channelMonitorAccountTrace) addSuccess(durationMs int) {
	if time.Duration(durationMs)*time.Millisecond < monitorResponseHeaderTimeout {
		a.okCount++
		return
	}
	a.slowCount++
	if durationMs > a.slowMaxMs {
		a.slowMaxMs = durationMs
	}
}

func (a *channelMonitorAccountTrace) addInFlight(ageSeconds int64) {
	a.inFlight++
	if ageSeconds > a.inFlightMaxAge {
		a.inFlightMaxAge = ageSeconds
	}
}

// problems 出错、超时后才成功、仍卡着的次数。
func (a *channelMonitorAccountTrace) problems() int {
	return a.errorTotal + a.slowCount + a.inFlight
}

func (a *channelMonitorAccountTrace) render() string {
	label := "未分到账号"
	if a.id > 0 {
		label = fmt.Sprintf("#%d", a.id)
		if name := strings.TrimSpace(a.name); name != "" {
			label += " " + sanitizeSecurityText(name, 40)
		}
	}
	parts := []string{}
	for _, item := range a.errorOrder {
		parts = append(parts, withChannelMonitorCount(item, a.errorCounts[item]))
	}
	if a.inFlight > 0 {
		if a.inFlight == 1 {
			parts = append(parts, fmt.Sprintf("仍在处理（已 %d 秒）", a.inFlightMaxAge))
		} else {
			parts = append(parts, fmt.Sprintf("仍在处理 ×%d（最久 %d 秒）", a.inFlight, a.inFlightMaxAge))
		}
	}
	timeoutSeconds := int(monitorResponseHeaderTimeout / time.Second)
	if a.slowCount > 0 {
		seconds := (a.slowMaxMs + 500) / 1000
		if a.slowCount == 1 {
			parts = append(parts, fmt.Sprintf("超过 %d 秒才成功（耗时 %d 秒）", timeoutSeconds, seconds))
		} else {
			parts = append(parts, fmt.Sprintf("超过 %d 秒才成功 ×%d（最长 %d 秒）", timeoutSeconds, a.slowCount, seconds))
		}
	}
	if a.okCount > 0 {
		parts = append(parts, withChannelMonitorCount(fmt.Sprintf("%d 秒内正常返回", timeoutSeconds), a.okCount))
	}
	return "· " + label + "：" + strings.Join(parts, "；")
}

func withChannelMonitorCount(item string, count int) string {
	if count > 1 {
		return fmt.Sprintf("%s ×%d", item, count)
	}
	return item
}

type channelMonitorTrace struct {
	accounts []*channelMonitorAccountTrace
	byID     map[int64]*channelMonitorAccountTrace
}

func newChannelMonitorTrace() *channelMonitorTrace {
	return &channelMonitorTrace{byID: map[int64]*channelMonitorAccountTrace{}}
}

func (t *channelMonitorTrace) account(id int64, name string) *channelMonitorAccountTrace {
	if id < 0 {
		id = 0
	}
	acc := t.byID[id]
	if acc == nil {
		acc = &channelMonitorAccountTrace{id: id, errorCounts: map[string]int{}}
		t.byID[id] = acc
		t.accounts = append(t.accounts, acc)
	}
	if acc.name == "" && strings.TrimSpace(name) != "" {
		acc.name = name
	}
	return acc
}

func (t *channelMonitorTrace) empty() bool {
	return len(t.accounts) == 0
}

// render 出问题次数多的账号排前面。
func (t *channelMonitorTrace) render() []string {
	accounts := append([]*channelMonitorAccountTrace(nil), t.accounts...)
	sort.SliceStable(accounts, func(i, j int) bool {
		return accounts[i].problems() > accounts[j].problems()
	})
	lines := []string{"出错渠道（网关记录）："}
	for i, acc := range accounts {
		if i == channelMonitorTraceMaxAccounts {
			lines = append(lines, fmt.Sprintf("· 另有 %d 个账号未列出", len(accounts)-i))
			break
		}
		lines = append(lines, acc.render())
	}
	return lines
}
