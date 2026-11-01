package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// 以下响应取自 2026-11-01 实测：zzshu / 月亮 mux.la 的 Vertex 与 coai「As」AI Studio 渠道。
const (
	vertexPlainJSON = `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK","thoughtSignature":"AY89a1/plain"}]},"finishReason":"STOP"}],
 "usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1,"trafficType":"ON_DEMAND"},
 "modelVersion":"gemini-3.8-flash","createTime":"2026-11-01T12:02:20.100000Z","responseId":"USrnauy8BbamyOgPyZGi-QQ"}`
	// 2026-11-01 21:29 之后 zzshu 纯文本请求的样子：有 createTime，没有 responseId 和签名，"Hello" 计 2。
	mixedPlainJSON = `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],
 "usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1,"trafficType":"ON_DEMAND"},
 "modelVersion":"gemini-3.8-flash","createTime":"2026-11-01T13:33:40.100000Z"}`
	vertexCallJSON = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}},
 "thoughtSignature":"AY89a1+Yfresh"}]},"finishReason":"STOP"}],
 "usageMetadata":{"promptTokenCount":36,"candidatesTokenCount":16,"thoughtsTokenCount":57,"trafficType":"ON_DEMAND"},
 "modelVersion":"gemini-3.8-flash","createTime":"2026-11-01T12:02:21.602969Z","responseId":"TSrnatnmJPyfyOgPiKCEuA4"}`
	vertexVideoJSON = `{"candidates":[{"content":{"parts":[{"text":"Tokyo is shown, and the woman's name is Saeka Shimada."}]}}],
 "usageMetadata":{"promptTokenCount":935,"promptTokensDetails":[{"modality":"VIDEO","tokenCount":660},{"modality":"TEXT","tokenCount":25},{"modality":"AUDIO","tokenCount":250}]},
 "createTime":"2026-11-01T13:20:10.100000Z","responseId":"Yyrnap74BqeaotQP38PU-AM"}`
	vertexCanaryJSON = `{"candidates":[{"content":{"parts":[{"text":"It is 17°C and cloudy in Paris."}]}}],
 "createTime":"2026-11-01T13:20:12.100000Z","responseId":"xSrnavKvMa-aotQPzM_m2AU"}`
	// AI Studio 系：签名 E 开头，没有 createTime / responseId。
	aiStudioCallJSON = `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}},
 "thoughtSignature":"EooDCocDAWkUfROG"}]},"finishReason":"STOP","index":0}],
 "usageMetadata":{"promptTokenCount":51,"candidatesTokenCount":16},"modelVersion":"gemini-3.8-flash"}`
)

func geminiReplyOK(t *testing.T, raw string) geminiFidelityReply {
	t.Helper()
	var r geminiFidelityResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &r))
	return geminiFidelityReply{Status: 200, Body: &r}
}

func vertexInput(t *testing.T) geminiFidelityInput {
	return geminiFidelityInput{
		Plain: geminiReplyOK(t, vertexPlainJSON), Call: geminiReplyOK(t, vertexCallJSON), Video: geminiReplyOK(t, vertexVideoJSON), Canary: geminiReplyOK(t, vertexCanaryJSON),
		CanaryFromStore: true, SignaturePrefix: "AY", Tolerance: 10,
	}
}

func TestEvaluateGeminiFidelity_VertexPasses(t *testing.T) {
	in := vertexInput(t)
	in.Baseline = fidelityIntPtr(7)
	res := evaluateGeminiFidelity(in)
	require.Equal(t, fidelityStatusPass, res.Status, res.Message)
	require.Equal(t, fidelityKindGeminiVT, res.ProbeKind)
	for _, v := range []string{res.SignatureCheck, res.MediaCheck, res.CanaryCheck, res.MetaCheck, res.PromptCheck} {
		require.Equal(t, fidelityCheckPass, v)
	}
	require.Equal(t, "AY89a1", res.SignaturePrefix)
	require.Equal(t, 7, *res.PromptTokens) // 纯文本请求的 prompt token
	require.Contains(t, res.ResponseExcerpt, "Saeka Shimada")
	require.Empty(t, res.Message)
	// DeepSeek 专属检查项不适用。
	require.Equal(t, fidelityCheckSkip, res.SystemCheck)
}

func TestEvaluateGeminiFidelity_AIStudioFallback(t *testing.T) {
	in := vertexInput(t)
	in.Plain = geminiReplyOK(t, mixedPlainJSON)
	in.Call = geminiReplyOK(t, aiStudioCallJSON)
	in.Video = geminiFidelityReply{Status: 400, Err: "HTTP 400：Referencing Google Cloud Storage files directly is not supported. Register them using FileService.RegisterFile first."}
	in.Canary = geminiFidelityReply{Status: 400, Err: "HTTP 400：Invalid thought signature."}
	res := evaluateGeminiFidelity(in)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Equal(t, fidelityCheckFail, res.SignatureCheck)
	require.Equal(t, fidelityCheckFail, res.MediaCheck)
	require.Equal(t, fidelityCheckFail, res.CanaryCheck)
	require.Equal(t, fidelityCheckFail, res.MetaCheck)
	require.Contains(t, res.Message, "签名开头是 EooDCo…，不是 Vertex 格式")
	require.Contains(t, res.Message, "gs:// 视频没被读取（HTTP 400：Referencing Google Cloud Storage files directly is not supported")
	require.Contains(t, res.Message, "上一轮的 Vertex 签名被拒（HTTP 400：Invalid thought signature.）")
	require.Contains(t, res.Message, "返回不像 Vertex：纯文本缺 responseId；函数调用缺 createTime、responseId")
}

func TestEvaluateGeminiFidelity_PlainTextRoutedElsewhere(t *testing.T) {
	// 只有纯文本被分流：函数调用、视频、金丝雀都正常，仍然判不通过。
	in := vertexInput(t)
	in.Plain = geminiReplyOK(t, mixedPlainJSON)
	res := evaluateGeminiFidelity(in)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Equal(t, fidelityCheckFail, res.MetaCheck)
	require.Equal(t, fidelityCheckPass, res.SignatureCheck)
	require.Equal(t, fidelityCheckPass, res.MediaCheck)
	require.Contains(t, res.Message, "纯文本缺 responseId")

	// 只有一部分纯文本被分流（zzshu 3.8-flash 约 1/4）：额外采样里抓到也算；出错的采样忽略。
	in = vertexInput(t)
	in.PlainExtra = []geminiFidelityReply{{Status: 524, Err: "HTTP 524"}, geminiReplyOK(t, mixedPlainJSON)}
	res = evaluateGeminiFidelity(in)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Contains(t, res.Message, "纯文本#3缺 responseId")

	in = vertexInput(t)
	in.PlainExtra = []geminiFidelityReply{{Status: 524, Err: "HTTP 524"}, geminiReplyOK(t, vertexPlainJSON)}
	require.Equal(t, fidelityStatusPass, evaluateGeminiFidelity(in).Status)
}

func TestEvaluateGeminiFidelity_VideoSilentlyDropped(t *testing.T) {
	// 客户反馈的混用症状：视频被丢掉，模型只按文字回答。
	in := vertexInput(t)
	in.Video = geminiReplyOK(t, `{"candidates":[{"content":{"parts":[{"text":"NO_VIDEO"}]}}],
 "usageMetadata":{"promptTokenCount":36,"promptTokensDetails":[{"modality":"TEXT","tokenCount":36}]},
 "createTime":"2026-11-01T13:20:10Z","responseId":"abc"}`)
	res := evaluateGeminiFidelity(in)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Equal(t, fidelityCheckFail, res.MediaCheck)
	require.Contains(t, res.Message, "没有 VIDEO token")

	// 没有分模态明细时，看答案：瞎编的答案不算读到视频。
	in.Video = geminiReplyOK(t, `{"candidates":[{"content":{"parts":[{"text":"A city street at night with a man."}]}}],
 "createTime":"2026-11-01T13:20:10Z","responseId":"abc"}`)
	res = evaluateGeminiFidelity(in)
	require.Equal(t, fidelityCheckFail, res.MediaCheck)
	require.Contains(t, res.Message, "答不出视频内容")
}

func TestEvaluateGeminiFidelity_TransientErrorsAreNotFailures(t *testing.T) {
	in := vertexInput(t)
	in.Video = geminiFidelityReply{Status: 429, Err: "HTTP 429：Resource exhausted."}
	in.Canary = geminiFidelityReply{Status: 503, Err: "HTTP 503：No available channel"}
	res := evaluateGeminiFidelity(in)
	require.Equal(t, fidelityStatusError, res.Status)
	require.Equal(t, fidelityCheckSkip, res.MediaCheck)
	require.Equal(t, fidelityCheckSkip, res.CanaryCheck)
	require.Equal(t, fidelityCheckPass, res.SignatureCheck)
	require.Empty(t, res.failures())
	require.Contains(t, res.Message, "视频请求出错：HTTP 429")

	in = vertexInput(t)
	in.Call = geminiFidelityReply{Err: "上游 90 秒内没有返回"}
	res = evaluateGeminiFidelity(in)
	require.Equal(t, fidelityStatusError, res.Status)
	require.Contains(t, res.Message, "函数调用请求出错：上游 90 秒内没有返回")

	in = vertexInput(t)
	in.Plain = geminiFidelityReply{Status: 503, Err: "HTTP 503：No available channel"}
	res = evaluateGeminiFidelity(in)
	require.Equal(t, fidelityStatusError, res.Status)
	require.Contains(t, res.Message, "纯文本请求出错：HTTP 503")
}

func TestEvaluateGeminiFidelity_PromptDriftAndMissingSignature(t *testing.T) {
	// argon 被注入隐藏提示词（2026-11-01：32 → 246）。
	in := vertexInput(t)
	in.Baseline = fidelityIntPtr(7)
	in.Plain = geminiReplyOK(t, strings.Replace(vertexPlainJSON, `"promptTokenCount":7`, `"promptTokenCount":221`, 1))
	res := evaluateGeminiFidelity(in)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Equal(t, fidelityCheckFail, res.PromptCheck)
	require.Contains(t, res.Message, "prompt_tokens 221，基线 7")

	in = vertexInput(t)
	in.Call = geminiReplyOK(t, strings.Replace(vertexCallJSON, `"thoughtSignature":"AY89a1+Yfresh"`, `"thoughtSignature":""`, 1))
	res = evaluateGeminiFidelity(in)
	require.Equal(t, fidelityCheckFail, res.SignatureCheck)
	require.Contains(t, res.Message, "没有返回 thoughtSignature")
}

func TestGeminiFidelityTargetsPickVertexAccounts(t *testing.T) {
	mapping := map[string]any{"gemini-3.8-flash": "gemini-3.8-flash", "gemini-4-argon": "gemini-4-argon"}
	repo := &fidelityAccountRepoStub{accounts: []Account{
		{ID: 145, Name: "月亮 Gemini VT", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "k", "base_url": "https://mux.la/", "model_mapping": mapping}},
		{ID: 174, Name: "zzshu Gemini Vertex", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "k", "base_url": "https://zzshu.cc/", "model_mapping": map[string]any{"gemini-3.8-flash": "gemini-3.8-flash"}}},
		{ID: 146, Name: "coai Gemini Ultra", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "k", "model_mapping": mapping}},
		{ID: 200, Name: "新渠道", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "k", "model_mapping": map[string]any{"gemini-4-argon": "gemini-4-argon"}}},
		{ID: 201, Name: "Gemini VT 停用", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: "disabled", Credentials: map[string]any{"api_key": "k"}},
		{ID: 202, Name: "Gemini VT OAuth", Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{}},
		{ID: 203, Name: "AVTX Gemini", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Credentials: map[string]any{"api_key": "k"}},
	}}
	p := &channelFidelityProber{accounts: repo}
	cfg := DefaultChannelFidelityConfig()
	cfg.GeminiAccountIDs = []int64{200}
	targets, err := p.geminiTargets(context.Background(), cfg)
	require.NoError(t, err)
	var got []string
	for _, tg := range targets {
		require.Equal(t, fidelityKindGeminiVT, tg.Kind)
		require.Equal(t, "AY", tg.SignaturePrefix)
		got = append(got, strconv.FormatInt(tg.Account.ID, 10)+":"+tg.UpstreamModel)
	}
	require.Equal(t, []string{"145:gemini-3.8-flash", "145:gemini-4-argon", "174:gemini-3.8-flash", "200:gemini-4-argon"}, got)

	cfg.GeminiModels = nil
	targets, err = p.geminiTargets(context.Background(), cfg)
	require.NoError(t, err)
	require.Empty(t, targets)
}

// geminiFidelityHTTPStub 按请求内容区分三个体检请求并记录下来。
type geminiFidelityHTTPStub struct {
	HTTPUpstream
	mu      sync.Mutex
	urls    []string
	keys    []string
	bodies  []string
	replies map[string]string // call / video / canary -> JSON
	status  map[string]int
}

func (s *geminiFidelityHTTPStub) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	kind := "plain"
	switch {
	case strings.Contains(body, "fileData"):
		kind = "video"
	case strings.Contains(body, "functionResponse"):
		kind = "canary"
	case strings.Contains(body, "functionDeclarations"):
		kind = "call"
	}
	s.mu.Lock()
	s.urls = append(s.urls, r.URL.String())
	s.keys = append(s.keys, r.Header.Get("x-goog-api-key"))
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()
	status := http.StatusOK
	if v, ok := s.status[kind]; ok {
		status = v
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(s.replies[kind]))}, nil
}

func TestGeminiProbeSendsNativeRequestsWithStoredCanary(t *testing.T) {
	stub := &geminiFidelityHTTPStub{replies: map[string]string{"plain": vertexPlainJSON, "call": vertexCallJSON, "video": vertexVideoJSON, "canary": vertexCanaryJSON}}
	p := &channelFidelityProber{http: stub}
	target := fidelityTarget{
		Account: &Account{ID: 174, Name: "zzshu Gemini VT", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "sk-zz", "base_url": "https://zzshu.cc/"}},
		Model: "gemini-3.8-flash", UpstreamModel: "gemini-3.8-flash",
		Kind: fidelityKindGeminiVT, SignaturePrefix: "AY", Canary: "AY89a1/stored",
	}
	res := p.probe(context.Background(), target, nil, 10)
	require.Equal(t, fidelityStatusPass, res.Status, res.Message)
	require.Len(t, stub.urls, 6) // 纯文本 ×3、函数调用、视频、金丝雀
	for i := range stub.urls {
		require.Equal(t, "https://zzshu.cc/v1beta/models/gemini-3.8-flash:generateContent", stub.urls[i])
		require.Equal(t, "sk-zz", stub.keys[i])
	}
	require.NotContains(t, stub.bodies[0], "tools")
	require.Equal(t, stub.bodies[0], stub.bodies[2])
	require.Contains(t, stub.bodies[3], `"mode":"ANY"`)
	require.Contains(t, stub.bodies[4], fidelityGeminiVideoURI)
	require.Contains(t, stub.bodies[5], `"thoughtSignature":"AY89a1/stored"`)
	require.Equal(t, "AY89a1+Yfresh", res.CanarySignature) // 通过后换成本轮的新签名
	require.Equal(t, int64(174), res.AccountID)
	require.NotNil(t, res.LatencyMs)

	// 混了 AI Studio：签名、视频、金丝雀全部不对，且不留下新的金丝雀。
	stub = &geminiFidelityHTTPStub{
		replies: map[string]string{"plain": mixedPlainJSON, "call": aiStudioCallJSON,
			"video":  `{"error":{"code":400,"message":"Referencing Google Cloud Storage files directly is not supported."}}`,
			"canary": `{"error":{"code":400,"message":"Invalid thought signature."}}`},
		status: map[string]int{"video": 400, "canary": 400},
	}
	p = &channelFidelityProber{http: stub}
	res = p.probe(context.Background(), target, nil, 10)
	require.Equal(t, fidelityStatusFail, res.Status)
	require.Empty(t, res.CanarySignature)
	require.Contains(t, res.Message, "Invalid thought signature.")

	// 第一个请求就失败：不再往下发。
	stub = &geminiFidelityHTTPStub{replies: map[string]string{"plain": `{"error":{"message":"No available channel"}}`}, status: map[string]int{"plain": 503}}
	p = &channelFidelityProber{http: stub}
	res = p.probe(context.Background(), target, nil, 10)
	require.Equal(t, fidelityStatusError, res.Status)
	require.Len(t, stub.urls, 1)
	require.Equal(t, 503, *res.HTTPStatus)
}

func TestGeminiProbeUsesFreshSignatureWithoutStoredCanary(t *testing.T) {
	stub := &geminiFidelityHTTPStub{replies: map[string]string{"plain": vertexPlainJSON, "call": vertexCallJSON, "video": vertexVideoJSON, "canary": vertexCanaryJSON}}
	p := &channelFidelityProber{http: stub}
	target := fidelityTarget{
		Account: &Account{ID: 145, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "k", "base_url": "https://mux.la"}},
		Model: "gemini-4-argon", UpstreamModel: "gemini-4-argon", Kind: fidelityKindGeminiVT,
	}
	res := p.probe(context.Background(), target, nil, 10)
	require.Equal(t, fidelityStatusPass, res.Status, res.Message)
	require.Contains(t, stub.bodies[5], `"thoughtSignature":"AY89a1+Yfresh"`)
}

func TestRecordFidelityCheckSavesGeminiCanaryOnPass(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	baselines := map[string]int{}
	target := fidelityTarget{Account: &Account{ID: 174, Name: "zzshu Gemini VT"}, Model: "gemini-3.8-flash",
		UpstreamModel: "gemini-3.8-flash", Kind: fidelityKindGeminiVT}

	passed := ChannelFidelityCheck{CheckedAt: time.Now(), Status: fidelityStatusPass, ProbeKind: fidelityKindGeminiVT,
		PromptTokens: fidelityIntPtr(36), CanarySignature: "AY89a1+new", SignaturePrefix: "AY89a1"}
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO channel_fidelity_checks")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO channel_fidelity_baselines(account_id, upstream_model, prompt_tokens, canary_signature, updated_at)")).
		WithArgs(int64(174), "gemini-3.8-flash", 36, "AY89a1+new").
		WillReturnResult(sqlmock.NewResult(0, 1))
	svc.recordFidelityCheck(context.Background(), target, &passed, baselines)
	require.Equal(t, 36, baselines[fidelityKey(174, "gemini-3.8-flash")])

	// 不通过时不更新金丝雀。
	failed := passed
	failed.Status = fidelityStatusFail
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO channel_fidelity_checks")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(8))
	svc.recordFidelityCheck(context.Background(), target, &failed, baselines)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelFidelityConfigGeminiDefaultsAndValidation(t *testing.T) {
	// 旧配置（PR #60 时只有 DeepSeek 字段）：Gemini 部分按默认值开启。
	cfg := DefaultChannelFidelityConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"interval_minutes":15,"models":["deepseek-v4-pro"]}`), &cfg))
	cfg.normalize()
	require.Equal(t, []string{"gemini-3.8-flash", "gemini-4-argon"}, cfg.GeminiModels)
	require.Equal(t, "AY", cfg.GeminiSignaturePrefix)
	require.NoError(t, cfg.Validate())

	cfg.GeminiModels = []string{" gemini-3.8-flash ", "", "gemini-3.8-flash"}
	cfg.GeminiAccountIDs = []int64{200, 200, -1}
	cfg.GeminiSignaturePrefix = "  "
	cfg.normalize()
	require.Equal(t, []string{"gemini-3.8-flash"}, cfg.GeminiModels)
	require.Equal(t, []int64{200}, cfg.GeminiAccountIDs)
	require.Equal(t, "AY", cfg.GeminiSignaturePrefix)

	cfg.GeminiModels = nil // 允许只体检 DeepSeek
	require.NoError(t, cfg.Validate())
	cfg.GeminiSignaturePrefix = strings.Repeat("A", 17)
	require.Error(t, cfg.Validate())
}

func TestFidelityAlertDraftGemini(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	in := vertexInput(t)
	in.Video = geminiFidelityReply{Status: 400, Err: "HTTP 400：Referencing Google Cloud Storage files directly is not supported."}
	last := evaluateGeminiFidelity(in)
	last.CheckedAt = time.Date(2026, 11, 1, 13, 21, 0, 0, time.UTC)
	first := last
	first.CheckedAt = last.CheckedAt.Add(-time.Minute)
	target := fidelityTarget{Account: &Account{ID: 174, Name: "zzshu Gemini VT"}, Model: "gemini-3.8-flash",
		UpstreamModel: "gemini-3.8-flash", Kind: fidelityKindGeminiVT}
	body := fidelityAlertDraft(target, &first, last, loc).body()
	require.Contains(t, body, "账号：zzshu Gemini VT（#174）")
	require.Contains(t, body, "gs:// 视频没被读取（HTTP 400：Referencing Google Cloud Storage files directly is not supported.）")
	require.Contains(t, body, "疑似混用了 AI Studio")
	require.Contains(t, body, "已复测确认：2026-11-01 21:20:00、2026-11-01 21:21:00 两次都不通过")
	require.NotContains(t, body, "模型映射里去掉")
}
