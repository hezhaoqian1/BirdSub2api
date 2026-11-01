package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func fidelityContent(s string) *string { return &s }

func fidelityIntPtr(v int) *int { return &v }

// fidelityOKResponse 构造一个完全遵守要求的上游返回。
func fidelityOKResponse(p fidelityPrompt, promptTokens int) fidelityResponse {
	var resp fidelityResponse
	raw := `{"model":"deepseek-v4.1-flash","choices":[{"message":{"content":` +
		strconv.Quote(`{"code": "`+p.Code+`", "answer": `+strconv.Itoa(p.Answer)+`}`) +
		`},"finish_reason":"stop"}],"usage":{"prompt_tokens":` + strconv.Itoa(promptTokens) +
		`,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":0}}}`
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		panic(err)
	}
	return resp
}

func testFidelityPrompt() fidelityPrompt {
	return fidelityPrompt{Code: "482913", A: 123, B: 456, Answer: 579}
}

func TestEvaluateChannelFidelity_AllPass(t *testing.T) {
	p := testFidelityPrompt()
	res := evaluateChannelFidelity(p, fidelityOKResponse(p, 81), fidelityIntPtr(81), 10)
	require.Equal(t, fidelityStatusPass, res.Status)
	require.Equal(t, fidelityCheckPass, res.SystemCheck)
	require.Equal(t, fidelityCheckPass, res.ThinkingCheck)
	require.Equal(t, fidelityCheckPass, res.JSONCheck)
	require.Equal(t, fidelityCheckPass, res.AnswerCheck)
	require.Equal(t, fidelityCheckPass, res.PromptCheck)
	require.Empty(t, res.Message)
	require.Empty(t, res.failures())
}

func TestEvaluateChannelFidelity_NoBaselineSkipsPromptCheck(t *testing.T) {
	p := testFidelityPrompt()
	res := evaluateChannelFidelity(p, fidelityOKResponse(p, 81), nil, 10)
	require.Equal(t, fidelityStatusPass, res.Status)
	require.Equal(t, fidelityCheckSkip, res.PromptCheck)
	require.Nil(t, res.BaselinePromptTokens)
}

func TestEvaluateChannelFidelity_ForcedThinking(t *testing.T) {
	// Aizzz 2026-10-31：关闭思考被忽略，仍返回思考内容，正文照常。
	p := testFidelityPrompt()
	resp := fidelityOKResponse(p, 59)
	resp.Choices[0].Message.ReasoningContent = fidelityContent("用户要求只输出 JSON，先算 123+456……")
	res := evaluateChannelFidelity(p, resp, fidelityIntPtr(59), 10)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Equal(t, fidelityCheckFail, res.ThinkingCheck)
	require.Equal(t, fidelityCheckPass, res.SystemCheck)
	require.Contains(t, res.Message, "关闭思考无效")
	require.Contains(t, res.ResponseExcerpt, "[思考]")
}

func TestEvaluateChannelFidelity_ReasoningTokensOnly(t *testing.T) {
	p := testFidelityPrompt()
	resp := fidelityOKResponse(p, 59)
	resp.Usage.CompletionTokensDetails.ReasoningTokens = fidelityIntPtr(30)
	res := evaluateChannelFidelity(p, resp, nil, 10)
	require.Equal(t, fidelityCheckFail, res.ThinkingCheck)
	require.Equal(t, fidelityStatusFail, res.Status)
	// Aizzz 收返佣号 2026-11-01：没回传思考内容，只在 usage 里计了思考 token。
	require.Contains(t, res.Message, "没返回思考内容，但计了 30 个思考 token")
	require.NotContains(t, res.Message, "0 字思考")
}

func TestEvaluateChannelFidelity_ThinkingExhaustsOutput(t *testing.T) {
	// 思考把 max_tokens 用完、正文为空：其余几项不算失败，只报思考。
	p := testFidelityPrompt()
	resp := fidelityOKResponse(p, 59)
	resp.Choices[0].Message.Content = nil
	resp.Choices[0].Message.ReasoningContent = fidelityContent(strings.Repeat("想", 500))
	resp.Choices[0].FinishReason = "length"
	res := evaluateChannelFidelity(p, resp, fidelityIntPtr(59), 10)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Equal(t, fidelityCheckFail, res.ThinkingCheck)
	require.Equal(t, fidelityCheckSkip, res.SystemCheck)
	require.Equal(t, fidelityCheckSkip, res.JSONCheck)
	require.Equal(t, fidelityCheckSkip, res.AnswerCheck)
	require.Equal(t, 500, res.ReasoningChars)
	require.Contains(t, res.Message, "思考占满了输出上限")
}

func TestEvaluateChannelFidelity_SystemDropped(t *testing.T) {
	// 鲜藕鸡 2026-10-30：system 被换掉，模型不知道暗号。
	p := testFidelityPrompt()
	resp := fidelityOKResponse(p, 81)
	resp.Choices[0].Message.Content = fidelityContent(`{"code": "暗号", "answer": 579}`)
	res := evaluateChannelFidelity(p, resp, nil, 10)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Equal(t, fidelityCheckFail, res.SystemCheck)
	require.Equal(t, fidelityCheckPass, res.JSONCheck)
	require.Equal(t, fidelityCheckPass, res.AnswerCheck)
	require.Contains(t, res.Message, "system 提示词未生效")
}

func TestEvaluateChannelFidelity_JSONModeIgnored(t *testing.T) {
	p := testFidelityPrompt()
	resp := fidelityOKResponse(p, 81)
	resp.Choices[0].Message.Content = fidelityContent("```json\n{\"code\": \"482913\", \"answer\": 579}\n```")
	res := evaluateChannelFidelity(p, resp, nil, 10)
	require.Equal(t, fidelityCheckFail, res.JSONCheck)
	require.Equal(t, fidelityCheckPass, res.SystemCheck)
	require.Equal(t, fidelityCheckPass, res.AnswerCheck)
	require.Equal(t, fidelityStatusFail, res.Status)
}

func TestEvaluateChannelFidelity_AnswerAsStringAndWrongAnswer(t *testing.T) {
	p := testFidelityPrompt()
	resp := fidelityOKResponse(p, 81)
	resp.Choices[0].Message.Content = fidelityContent(`{"code": "482913", "answer": "579"}`)
	require.Equal(t, fidelityCheckPass, evaluateChannelFidelity(p, resp, nil, 10).AnswerCheck)

	resp.Choices[0].Message.Content = fidelityContent(`{"code": "482913", "answer": 578}`)
	res := evaluateChannelFidelity(p, resp, nil, 10)
	require.Equal(t, fidelityCheckFail, res.AnswerCheck)
	require.Contains(t, res.Message, "计算题答错")
}

func TestEvaluateChannelFidelity_PromptTokenDrift(t *testing.T) {
	p := testFidelityPrompt()
	// 偏差在容差内：通过。
	res := evaluateChannelFidelity(p, fidelityOKResponse(p, 88), fidelityIntPtr(81), 10)
	require.Equal(t, fidelityCheckPass, res.PromptCheck)
	// Aizzz 那次 105 → 134：超出容差，判定为注入。
	res = evaluateChannelFidelity(p, fidelityOKResponse(p, 134), fidelityIntPtr(105), 10)
	require.Equal(t, fidelityCheckFail, res.PromptCheck)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Contains(t, res.Message, "prompt_tokens 134，基线 105")
	require.Equal(t, 105, *res.BaselinePromptTokens)
}

func TestEvaluateChannelFidelity_NoChoicesIsError(t *testing.T) {
	res := evaluateChannelFidelity(testFidelityPrompt(), fidelityResponse{}, nil, 10)
	require.Equal(t, fidelityStatusError, res.Status)
	require.NotEmpty(t, res.Message)
}

func TestNewFidelityPromptHasFixedLengthParts(t *testing.T) {
	for i := 0; i < 50; i++ {
		p := newFidelityPrompt()
		require.Len(t, p.Code, 6)
		require.GreaterOrEqual(t, p.A, 100)
		require.LessOrEqual(t, p.A, 999)
		require.GreaterOrEqual(t, p.B, 100)
		require.LessOrEqual(t, p.B, 999)
		require.Equal(t, p.A+p.B, p.Answer)
	}
}

func TestFidelityRequestBodyDisablesThinkingAndAsksForJSON(t *testing.T) {
	p := testFidelityPrompt()
	raw, err := p.requestBody("deepseek-flash")
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	require.Equal(t, "deepseek-flash", body["model"])
	require.Equal(t, map[string]any{"type": "disabled"}, body["thinking"])
	require.Equal(t, "none", body["reasoning_effort"])
	require.Equal(t, map[string]any{"type": "json_object"}, body["response_format"])
	require.Equal(t, false, body["stream"])
	msgs, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, msgs, 2)
	system, ok := msgs[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "system", system["role"])
	require.Contains(t, system["content"], "482913")
	require.Contains(t, system["content"], "JSON")
	user, ok := msgs[1].(map[string]any)
	require.True(t, ok)
	require.Contains(t, user["content"], "123 + 456")
}

func TestChannelFidelityConfigValidate(t *testing.T) {
	cfg := DefaultChannelFidelityConfig()
	require.NoError(t, cfg.Validate())
	require.False(t, cfg.Enabled)

	cfg.Models = []string{" deepseek-v4-pro ", "", "deepseek-v4-pro"}
	cfg.normalize()
	require.Equal(t, []string{"deepseek-v4-pro"}, cfg.Models)

	bad := DefaultChannelFidelityConfig()
	bad.IntervalMinutes = 1
	require.Error(t, bad.Validate())
	bad = DefaultChannelFidelityConfig()
	bad.RecheckSeconds = 5
	require.Error(t, bad.Validate())
	bad.RecheckSeconds = 0
	require.NoError(t, bad.Validate())
	bad.Models = nil
	require.Error(t, bad.Validate())
}

func TestDefaultSecurityAlertConfigEnablesFidelityNotify(t *testing.T) {
	require.True(t, DefaultSecurityAlertConfig().ChannelFidelityNotify)
	// 旧配置 JSON 没有这个字段时保持默认开启。
	cfg := DefaultSecurityAlertConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"channel_monitor_notify":false}`), &cfg))
	require.True(t, cfg.ChannelFidelityNotify)
}

func TestFidelityAlertSubjectIsBounded(t *testing.T) {
	require.Equal(t, "fidelity:135:deepseek-v4.1-flash", fidelityAlertSubject(135, "deepseek-v4.1-flash"))
	require.LessOrEqual(t, len(fidelityAlertSubject(1, strings.Repeat("m", 300))), 100)
}

func TestFidelityAlertDraft(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	target := fidelityTarget{
		Account:       &Account{ID: 135, Name: "Aizzz DeepSeek"},
		Model:         "deepseek-v4.1-flash",
		UpstreamModel: "deepseek-v4.1-flash",
	}
	first := ChannelFidelityCheck{CheckedAt: time.Date(2026, 10, 31, 13, 20, 0, 0, time.UTC)}
	last := ChannelFidelityCheck{
		CheckedAt:            time.Date(2026, 10, 31, 13, 21, 0, 0, time.UTC),
		ThinkingCheck:        fidelityCheckFail,
		PromptCheck:          fidelityCheckFail,
		ReasoningChars:       8214,
		PromptTokens:         fidelityIntPtr(134),
		BaselinePromptTokens: fidelityIntPtr(105),
	}
	d := fidelityAlertDraft(target, &first, last, loc)
	require.Equal(t, securityAlertKindChannelFidelity, d.Kind)
	require.Equal(t, SecurityAlertCritical, d.Severity)
	body := d.body()
	require.Contains(t, body, "Aizzz DeepSeek（#135）")
	require.Contains(t, body, "关闭思考无效（仍返回 8214 字思考）")
	require.Contains(t, body, "prompt_tokens 134，基线 105")
	require.Contains(t, body, "两次都不通过")
	require.Contains(t, body, "重置基线")
	require.NotContains(t, body, "（上游") // 上游模型与请求模型相同时不重复写

	target.UpstreamModel = "deepseek-flash"
	d = fidelityAlertDraft(target, nil, last, loc)
	require.Contains(t, d.body(), "deepseek-v4.1-flash（上游 deepseek-flash）")
	require.Contains(t, d.body(), "检测于")
}

type fidelityAccountRepoStub struct {
	AccountRepository
	accounts []Account
}

func (r *fidelityAccountRepoStub) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	var out []Account
	for _, a := range r.accounts {
		if a.Platform == platform {
			out = append(out, a)
		}
	}
	return out, nil
}

func TestFidelityTargetsFollowModelMapping(t *testing.T) {
	repo := &fidelityAccountRepoStub{accounts: []Account{
		{ // lcodex 式映射：v4.1-flash 与 -1017 都映射到 deepseek-flash，只测一次；不支持 v4-pro。
			ID: 157, Name: "lcodex", Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "k", "base_url": "https://ai.lcodex.cc/", "model_mapping": map[string]any{
				"deepseek-v4.1-flash": "deepseek-flash", "deepseek-v4.1-flash-1017": "deepseek-flash",
			}},
		},
		{ // 停止调度的账号照样体检。
			ID: 123, Name: "xoj", Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false,
			Credentials: map[string]any{"api_key": "k", "base_url": "http://192.0.2.1:8080/", "model_mapping": map[string]any{
				"deepseek-v4-pro": "deepseek-v4-pro", "deepseek-v4.1-flash": "deepseek-v4.1-flash",
			}},
		},
		{ID: 1, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: "disabled", Credentials: map[string]any{"api_key": "k"}},
		{ID: 2, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive, Credentials: map[string]any{}},
	}}
	p := &channelFidelityProber{accounts: repo}
	targets, err := p.targets(context.Background(), []string{"deepseek-v4.1-flash", "deepseek-v4.1-flash-1017", "deepseek-v4-pro"})
	require.NoError(t, err)
	var got []string
	for _, tg := range targets {
		got = append(got, strconv.FormatInt(tg.Account.ID, 10)+":"+tg.Model+"->"+tg.UpstreamModel)
	}
	require.Equal(t, []string{
		"157:deepseek-v4.1-flash->deepseek-flash",
		"123:deepseek-v4.1-flash->deepseek-v4.1-flash",
		"123:deepseek-v4-pro->deepseek-v4-pro",
	}, got)
}

type fidelityHTTPStub struct {
	HTTPUpstream
	req    *http.Request
	body   []byte
	status int
	reply  func(prompt map[string]any) string
}

func (f *fidelityHTTPStub) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	f.req = r
	f.body, _ = io.ReadAll(r.Body)
	var parsed map[string]any
	_ = json.Unmarshal(f.body, &parsed)
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(f.reply(parsed)))}, nil
}

// fidelityEchoReply 读出请求里的暗号和加数，回一个完全遵守要求的答案。
func fidelityEchoReply(reasoning string) func(map[string]any) string {
	return func(req map[string]any) string {
		msgs, _ := req["messages"].([]any)
		if len(msgs) < 2 {
			return `{"error":{"message":"bad request"}}`
		}
		systemMsg, _ := msgs[0].(map[string]any)
		userMsg, _ := msgs[1].(map[string]any)
		system, _ := systemMsg["content"].(string)
		user, _ := userMsg["content"].(string)
		code := fidelityNumberRegex.FindString(system)
		nums := fidelityNumberRegex.FindAllString(user, -1)
		a, _ := strconv.Atoi(nums[0])
		b, _ := strconv.Atoi(nums[1])
		content := `{"code": "` + code + `", "answer": ` + strconv.Itoa(a+b) + `}`
		msg := map[string]any{"role": "assistant", "content": content}
		if reasoning != "" {
			msg["reasoning_content"] = reasoning
		}
		out, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 81, "completion_tokens": 20},
		})
		return string(out)
	}
}

func TestFidelityProbeSendsAccountCredentialsToChatCompletions(t *testing.T) {
	stub := &fidelityHTTPStub{reply: fidelityEchoReply("")}
	p := &channelFidelityProber{http: stub}
	target := fidelityTarget{
		Account: &Account{ID: 135, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://zl.aizzz.xyz/"}},
		Model: "deepseek-v4.1-flash", UpstreamModel: "deepseek-v4.1-flash",
	}
	res := p.probe(context.Background(), target, fidelityIntPtr(81), 10)
	require.Equal(t, fidelityStatusPass, res.Status, res.Message)
	require.Equal(t, "https://zl.aizzz.xyz/v1/chat/completions", stub.req.URL.String())
	require.Equal(t, "Bearer sk-test", stub.req.Header.Get("Authorization"))
	require.Contains(t, string(stub.body), `"thinking":{"type":"disabled"}`)
	require.Equal(t, int64(135), res.AccountID)
	require.Equal(t, 200, *res.HTTPStatus)
	require.NotNil(t, res.LatencyMs)

	stub.reply = fidelityEchoReply("我先想一想")
	res = p.probe(context.Background(), target, fidelityIntPtr(81), 10)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Equal(t, fidelityCheckFail, res.ThinkingCheck)
}

func TestFidelityProbeHTTPErrorIsNotAFidelityFailure(t *testing.T) {
	stub := &fidelityHTTPStub{status: http.StatusBadGateway, reply: func(map[string]any) string {
		return `{"error":{"message":"Upstream service temporarily unavailable"}}`
	}}
	p := &channelFidelityProber{http: stub}
	target := fidelityTarget{
		Account: &Account{ID: 161, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://example.com"}},
		Model: "deepseek-v4-pro", UpstreamModel: "deepseek-v4-pro",
	}
	res := p.probe(context.Background(), target, nil, 10)
	require.Equal(t, fidelityStatusError, res.Status)
	require.Equal(t, 502, *res.HTTPStatus)
	require.Contains(t, res.Message, "HTTP 502")
	require.Empty(t, res.failures())
}

func fidelityTestTarget(schedulable bool) fidelityTarget {
	return fidelityTarget{
		Account:       &Account{ID: 135, Name: "Aizzz DeepSeek", Schedulable: schedulable},
		Model:         "deepseek-v4.1-flash",
		UpstreamModel: "deepseek-v4.1-flash",
	}
}

func fidelityFailedCheck(at time.Time) ChannelFidelityCheck {
	return ChannelFidelityCheck{
		CheckedAt: at, Status: fidelityStatusFail, ThinkingCheck: fidelityCheckFail, ReasoningChars: 812,
		SystemCheck: fidelityCheckPass, JSONCheck: fidelityCheckPass, AnswerCheck: fidelityCheckPass, PromptCheck: fidelityCheckPass,
	}
}

func expectFidelityClaim(mock sqlmock.Sqlmock, at time.Time, won bool) {
	rows := sqlmock.NewRows([]string{"subject"})
	if won {
		rows.AddRow("fidelity:135:deepseek-v4.1-flash")
	}
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO security_alert_state(subject, level, value, updated_at)")).
		WithArgs("fidelity:135:deepseek-v4.1-flash", float64(at.Unix())).
		WillReturnRows(rows)
}

func TestFidelityAlertOncePerEpisode(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, `{"enabled":true}`, "")
	first := time.Date(2026, 10, 31, 13, 20, 0, 0, time.UTC)
	last := first.Add(time.Minute)
	firstCheck := fidelityFailedCheck(first)

	mock.ExpectBegin()
	expectFidelityClaim(mock, last, true)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelFidelity, "critical", "渠道保真异常", bodyContains{
			"账号：Aizzz DeepSeek（#135）",
			"模型：deepseek-v4.1-flash",
			"关闭思考无效（仍返回 812 字思考）",
			"已复测确认：2026-10-31 21:20:00、2026-10-31 21:21:00 两次都不通过",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	svc.fidelityAlert(context.Background(), fidelityTestTarget(true), &firstCheck, fidelityFailedCheck(last))

	// 已推送过这轮故障（记录已存在）：不再写告警。
	mock.ExpectBegin()
	expectFidelityClaim(mock, last, false)
	mock.ExpectRollback()
	svc.fidelityAlert(context.Background(), fidelityTestTarget(true), &firstCheck, fidelityFailedCheck(last))

	// 停止调度的账号只记录不告警：不碰数据库。
	svc.fidelityAlert(context.Background(), fidelityTestTarget(false), &firstCheck, fidelityFailedCheck(last))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFidelityAlertRespectsNotifySwitch(t *testing.T) {
	for _, cfg := range []string{`{"enabled":true,"channel_fidelity_notify":false}`, `{"enabled":false}`} {
		svc, mock, _ := newUserWatchTestService(t, cfg, "")
		at := time.Date(2026, 10, 31, 13, 20, 0, 0, time.UTC)
		svc.fidelityAlert(context.Background(), fidelityTestTarget(true), nil, fidelityFailedCheck(at))
		require.NoError(t, mock.ExpectationsWereMet(), cfg)
	}
}

func TestFidelityRecoveredNotifiesOnce(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, `{"enabled":true}`, "")
	alertedAt := time.Date(2026, 10, 31, 13, 21, 0, 0, time.UTC)
	ok := ChannelFidelityCheck{CheckedAt: alertedAt.Add(2 * time.Hour), Status: fidelityStatusPass}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("DELETE FROM security_alert_state WHERE subject = $1 RETURNING value")).
		WithArgs("fidelity:135:deepseek-v4.1-flash").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(float64(alertedAt.Unix())))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindChannelFidelity, "info", "渠道保真已恢复", bodyContains{
			"当前状态：全部检查通过，检测于 2026-10-31 23:21:00",
			"距今约 2 小时 0 分钟",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	svc.fidelityRecovered(context.Background(), fidelityTestTarget(true), ok)

	// 没有告警记录：什么都不发。
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("DELETE FROM security_alert_state WHERE subject = $1 RETURNING value")).
		WithArgs("fidelity:135:deepseek-v4.1-flash").
		WillReturnRows(sqlmock.NewRows([]string{"value"}))
	mock.ExpectRollback()
	svc.fidelityRecovered(context.Background(), fidelityTestTarget(true), ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRecordFidelityCheckLearnsBaselineOnlyOnFullPass(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	baselines := map[string]int{}
	target := fidelityTestTarget(true)

	failed := fidelityFailedCheck(time.Now())
	failed.PromptTokens = fidelityIntPtr(134)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO channel_fidelity_checks")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	svc.recordFidelityCheck(context.Background(), target, &failed, baselines)
	require.Equal(t, int64(1), failed.ID)
	require.Empty(t, baselines)

	passed := ChannelFidelityCheck{CheckedAt: time.Now(), Status: fidelityStatusPass, PromptTokens: fidelityIntPtr(105)}
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO channel_fidelity_checks")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO channel_fidelity_baselines")).
		WithArgs(int64(135), "deepseek-v4.1-flash", 105).
		WillReturnResult(sqlmock.NewResult(0, 1))
	svc.recordFidelityCheck(context.Background(), target, &passed, baselines)
	require.Equal(t, 105, baselines[fidelityKey(135, "deepseek-v4.1-flash")])

	// 已有基线：后续通过不再改写。
	again := ChannelFidelityCheck{CheckedAt: time.Now(), Status: fidelityStatusPass, PromptTokens: fidelityIntPtr(106)}
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO channel_fidelity_checks")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(3))
	svc.recordFidelityCheck(context.Background(), target, &again, baselines)
	require.Equal(t, 105, baselines[fidelityKey(135, "deepseek-v4.1-flash")])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateFidelityConfigMergesAndValidates(t *testing.T) {
	svc, _, repo := newUserWatchTestService(t, "", "")
	cfg, err := svc.UpdateFidelityConfig(context.Background(), json.RawMessage(`{"enabled":true,"interval_minutes":30}`))
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, 30, cfg.IntervalMinutes)
	require.Equal(t, DefaultChannelFidelityConfig().Models, cfg.Models)
	require.Contains(t, repo.values[SettingKeyChannelFidelityConfig], `"interval_minutes":30`)

	_, err = svc.UpdateFidelityConfig(context.Background(), json.RawMessage(`{"interval_minutes":1}`))
	require.Error(t, err)
	got, err := svc.GetFidelityConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, 30, got.IntervalMinutes) // 校验失败不落库
}
