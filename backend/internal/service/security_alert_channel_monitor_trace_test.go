package service

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

type stubChannelMonitorLookup struct{ monitor *ChannelMonitor }

func (s stubChannelMonitorLookup) Get(context.Context, int64) (*ChannelMonitor, error) {
	return s.monitor, nil
}

type stubChannelMonitorSlots struct{ snapshot *ActiveSlotSnapshot }

func (s stubChannelMonitorSlots) ListAllAccountSlotHolders(context.Context) (*ActiveSlotSnapshot, error) {
	return s.snapshot, nil
}

func TestChannelMonitorFailureReason(t *testing.T) {
	for msg, want := range map[string]string{
		`do request: Post "https://datayes.coincoin.ai/v1/responses": http2: timeout awaiting response headers`:       "30 秒内没等到响应头（请求已发出，对方一直没回）",
		`upstream HTTP 429: {"error":{"code":"insufficient_quota","message":"API quota exhausted"}}`:                  "HTTP 429，余额或额度不足",
		`upstream HTTP 402: {"detail":"insufficient balance"}`:                                                        "HTTP 402，余额或额度不足",
		`upstream HTTP 503: {"error":{"message":"No available accounts"}}`:                                            "HTTP 503，分组里没有可用账号",
		`upstream HTTP 502: {"error":{"message":"Upstream service temporarily unavailable"}}`:                         "HTTP 502，上游服务暂时不可用",
		`upstream HTTP 400: {"error":{"code":"content_policy_violation"}}`:                                            "HTTP 400，被内容审核拦截",
		`challenge mismatch (expected 0, got "Request is blocked")`:                                                   "回答不对（有返回内容但算术题答错，可能被降级、拦截或换了模型）",
		`do request: Post "https://x/v1/responses": read tcp 10.0.0.1:1->1.2.3.4:443: read: connection reset by peer`: "连接被对方重置",
		`do request: Post "https://x/v1/responses": dial tcp: lookup x: no such host`:                                 "域名解析失败",
		`do request: Post "https://x/v1/responses": EOF`:                                                              "连接被对方提前关闭",
		"": "没有返回错误详情",
	} {
		require.Equal(t, want, channelMonitorFailureReason(MonitorStatusError, msg), msg)
	}
	require.Equal(t, "检测未通过", channelMonitorFailureReason(MonitorStatusFailed, "something odd"))
}

func TestChannelMonitorReasonLineMergesSamples(t *testing.T) {
	timeout := `do request: Post "https://a/v1/responses": http2: timeout awaiting response headers`
	alert := channelMonitorFailureAlert{Failures: []channelMonitorFailureSample{
		{Status: MonitorStatusError, Message: timeout},
		{Status: MonitorStatusError, Message: timeout},
		{Status: MonitorStatusError, Message: timeout},
	}}
	require.Equal(t, "原因（3 次相同）：30 秒内没等到响应头（请求已发出，对方一直没回）", channelMonitorReasonLine(alert))

	alert.Failures[2].Message = `upstream HTTP 502: bad gateway`
	require.Equal(t, "原因：30 秒内没等到响应头（请求已发出，对方一直没回） ×2；HTTP 502，上游服务暂时不可用 ×1", channelMonitorReasonLine(alert))

	// 没带逐次记录时用告警本身的报错。
	single := channelMonitorFailureAlert{Status: MonitorStatusError, Message: "upstream HTTP 502: bad gateway"}
	require.Equal(t, "原因：HTTP 502，上游服务暂时不可用", channelMonitorReasonLine(single))
}

func TestChannelMonitorNotifierTracesGatewayChannel(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	notifier := newChannelMonitorWeComNotifier(svc)
	notifier.monitors = stubChannelMonitorLookup{&ChannelMonitor{
		ID: 31, Endpoint: "https://birdapi.up.railway.app", APIKey: "sk-monitor", PrimaryModel: "gpt-5.6",
	}}
	notifier.slots = stubChannelMonitorSlots{&ActiveSlotSnapshot{Now: 1000, Slots: []ActiveSlot{
		{AccountID: 182, StartedAt: 968, Holder: &SlotHolder{APIKeyID: 125, Model: "gpt-5.6"}},
		{AccountID: 182, StartedAt: 900, Holder: &SlotHolder{APIKeyID: 999, Model: "gpt-5.6"}}, // 别的 Key
		{AccountID: 77, StartedAt: 990, Holder: &SlotHolder{APIKeyID: 125, Model: "other"}},    // 附加模型
		{AccountID: 55, StartedAt: 995, Holder: &SlotHolder{APIKeyID: 125, Model: "gpt-5.6"}},  // 名字要回查
	}}}
	checkedAt := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	alert := channelMonitorTestFailure(checkedAt)
	alert.Status = MonitorStatusError
	alert.Message = `do request: Post "https://birdapi.up.railway.app/v1/responses": http2: timeout awaiting response headers`
	alert.Failures = []channelMonitorFailureSample{
		{Status: MonitorStatusError, Message: alert.Message, CheckedAt: checkedAt.Add(-4 * time.Minute)},
		{Status: MonitorStatusError, Message: alert.Message, CheckedAt: checkedAt.Add(-2 * time.Minute)},
		{Status: MonitorStatusError, Message: alert.Message, CheckedAt: checkedAt},
	}
	since := checkedAt.Add(-4*time.Minute - channelMonitorTraceLookback)

	mock.ExpectBegin()
	expectChannelMonitorClaim(mock, checkedAt, true)
	mock.ExpectQuery(regexp.QuoteMeta("FROM api_keys k LEFT JOIN groups g")).WithArgs("sk-monitor").
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "name"}).AddRow(125, 46, "GPT 独享分组"))
	mock.ExpectQuery(regexp.QuoteMeta("FROM ops_error_logs e LEFT JOIN accounts a")).
		WithArgs(since, int64(125), "gpt-5.6", channelMonitorTraceRowLimit).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "name", "status_code", "upstream_status_code", "message", "upstream_errors"}).
			// 失败转移：先在 #27 余额不足，再到 #182 被 Cloudflare 524。
			AddRow(182, "ApiMars", 499, 524, "", `[{"account_id":27,"account_name":"zzone","upstream_status_code":403,"message":"Insufficient account balance"},{"account_id":182,"account_name":"ApiMars","upstream_status_code":524,"detail":"error code: 524"}]`).
			AddRow(182, "ApiMars", 499, 524, "", `[{"account_id":182,"account_name":"ApiMars","upstream_status_code":524,"detail":"error code: 524"}]`).
			// 失败转移后救回来的行没有逐次记录时不计入（成功那次在 usage_logs）。
			AddRow(183, "Saved", 200, 0, "Recovered upstream error", "").
			// 没分到账号的网关错误。
			AddRow(0, "", 503, 0, "No available accounts", ""))
	mock.ExpectQuery(regexp.QuoteMeta("FROM usage_logs u LEFT JOIN accounts a")).
		WithArgs(int64(125), since, "gpt-5.6", channelMonitorTraceRowLimit).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "name", "duration_ms"}).
			AddRow(182, "ApiMars", 105105).AddRow(182, "ApiMars", 1500))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name FROM accounts WHERE id = ANY($1)")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(55, "KDawg"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelMonitor, "warning", "渠道监控连续失败", bodyContains{
			"连续 3 次请求出错，最近一次 2026-10-29 09:00:00\n原因（3 次相同）：30 秒内没等到响应头（请求已发出，对方一直没回）\n错误：do request:",
			"\n分组：GPT 独享分组（#46）\n出错渠道（网关记录）：\n" +
				"· #182 ApiMars：上游 524 ×2；仍在处理（已 32 秒）；超过 30 秒才成功（耗时 105 秒）；30 秒内正常返回\n" +
				"· #27 zzone：上游 403：Insufficient account balance\n",
			"· #55 KDawg：仍在处理（已 5 秒）",
			"· 未分到账号：网关 503：No available accounts",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	handled, err := notifier.NotifyFailure(context.Background(), alert)
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorNotifierExternalEndpoint(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	notifier := newChannelMonitorWeComNotifier(svc)
	notifier.monitors = stubChannelMonitorLookup{&ChannelMonitor{
		ID: 31, Endpoint: "https://datayes.coincoin.ai/", APIKey: "sk-elsewhere", PrimaryModel: "gpt-5.6",
	}}
	checkedAt := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectChannelMonitorClaim(mock, checkedAt, true)
	mock.ExpectQuery(regexp.QuoteMeta("FROM api_keys k LEFT JOIN groups g")).WithArgs("sk-elsewhere").
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "name"}))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelMonitor, "warning", "渠道监控连续失败", bodyContains{
			"错误：HTTP 502 bad gateway\n出错渠道：外部地址 datayes.coincoin.ai（不经过本站网关，查不到它背后的上游账号）",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	handled, err := notifier.NotifyFailure(context.Background(), channelMonitorTestFailure(checkedAt))
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorNotifierTraceErrorStillAlerts(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	notifier := newChannelMonitorWeComNotifier(svc)
	notifier.monitors = stubChannelMonitorLookup{&ChannelMonitor{ID: 31, Endpoint: "https://birdapi.up.railway.app", APIKey: "sk-monitor"}}
	checkedAt := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectChannelMonitorClaim(mock, checkedAt, true)
	mock.ExpectQuery(regexp.QuoteMeta("FROM api_keys k LEFT JOIN groups g")).WillReturnError(context.DeadlineExceeded)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelMonitor, "warning", "渠道监控连续失败", bodyContains{
			"错误：HTTP 502 bad gateway",
			"出错渠道：查询网关记录失败，没能回查",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	handled, err := notifier.NotifyFailure(context.Background(), channelMonitorTestFailure(checkedAt))
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorTraceListsAtMostFiveAccounts(t *testing.T) {
	trace := newChannelMonitorTrace()
	for id := int64(1); id <= 7; id++ {
		trace.account(id, "acct").addError("上游 502")
	}
	trace.account(7, "").addError("上游 502")
	lines := trace.render()
	require.Len(t, lines, 1+channelMonitorTraceMaxAccounts+1)
	require.Equal(t, "· #7 acct：上游 502 ×2", lines[1])
	require.Equal(t, "· 另有 2 个账号未列出", lines[len(lines)-1])
}
