package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
)

// Gemini VT 保真体检：确认标成 Vertex（VT）的 Gemini 渠道真的走 Vertex，没有拿 AI Studio
// 之类的资源混用或兜底。AI Studio 读不了 gs:// 文件、会忽略视频直接按文字回答、签名格式不同，
// 这些客户都能感觉到但上游不会报错，所以每次用四个请求从返回内容上判断（2026-11-01 对照
// 月亮 mux.la 与 zzshu 两个纯 Vertex 渠道、以及 AI Studio / Antigravity / Ultra 渠道标定）。
// 每个返回都必须带 createTime 与 responseId（Vertex 每次都带，AI Studio 没有 responseId）：
//
//  0. 纯文本：最常见的客户请求。混用的上游会按请求类型分流——2026-11-01 zzshu 被压满后，
//     纯文本走了非 Vertex（没有 responseId、"Hello" 计 2 个 token、argon 被注入约 214 个
//     token），带 gs:// 的请求仍走 Vertex。promptTokenCount 与基线比较（发现注入提示词）。
//  1. 强制函数调用：Vertex 的 thoughtSignature 以 "AY" 开头（AI Studio 系是 "E" 开头）。
//  2. gs:// 公开样例视频：只有 Vertex 能读 Cloud Storage。AI Studio 会报 400，混用的上游
//     还可能悄悄丢掉视频只按文字回答——用"视频里的人叫什么"这种只有看了视频才答得出的问题判断。
//  3. 金丝雀：把上一轮通过时拿到的 Vertex 签名放回第二轮函数调用里。Vertex 认自己签发的签名，
//     AI Studio 会以 "Invalid thought signature" 拒绝，所以请求一旦被转去非 Vertex 来源就会暴露。

const (
	fidelityKindDeepSeek = "deepseek"
	fidelityKindGeminiVT = "gemini_vt"

	fidelityGeminiDefaultSignaturePrefix = "AY"
	fidelityGeminiMaxAccountIDs          = 50
	fidelityGeminiMaxOutputTokens        = 2048
	// 纯文本请求发几次：混用的上游只把一部分纯文本分给非 Vertex（zzshu 3.8-flash 约 1/4），多采几次更容易抓到。
	fidelityGeminiPlainSamples = 3
	// Google 官方公开样例（Pixel 8 广告）：东京夜景，摄影师 Saeka Shimada 自我介绍。
	fidelityGeminiVideoURI = "gs://cloud-samples-data/generative-ai/video/pixel8.mp4"
	fidelityGeminiNoVideo  = "NO_VIDEO"
)

var (
	// 账号名里带 VT 或 Vertex（独立的词）即按 Vertex 体检，例如「月亮 Gemini VT」。
	fidelityVertexNameRegex = regexp.MustCompile(`(?i)(^|[^a-z0-9])(vt|vertex)([^a-z0-9]|$)`)
	// 看过视频才答得出的关键词：城市与人名。
	fidelityGeminiVideoKeywords = []string{"tokyo", "东京", "東京", "saeka", "shimada"}
)

func defaultGeminiFidelityModels() []string {
	return []string{"gemini-3.8-flash", "gemini-4-argon"}
}

// isGeminiVertexAccount 账号是否按 Vertex 体检：名字带 VT/Vertex，或在配置的额外账号列表里。
func isGeminiVertexAccount(a *Account, extraIDs []int64) bool {
	if a == nil {
		return false
	}
	for _, id := range extraIDs {
		if id == a.ID {
			return true
		}
	}
	return fidelityVertexNameRegex.MatchString(a.Name)
}

// geminiTargets 列出所有启用中的 Gemini VT API Key 账号及其支持的体检模型（同一上游模型只测一次）。
func (p *channelFidelityProber) geminiTargets(ctx context.Context, cfg ChannelFidelityConfig) ([]fidelityTarget, error) {
	if len(cfg.GeminiModels) == 0 {
		return nil, nil
	}
	accounts, err := p.accounts.ListByPlatform(ctx, PlatformGemini)
	if err != nil {
		return nil, err
	}
	prefix := cfg.GeminiSignaturePrefix
	if prefix == "" {
		prefix = fidelityGeminiDefaultSignaturePrefix
	}
	var out []fidelityTarget
	for i := range accounts {
		a := &accounts[i]
		if a.Type != AccountTypeAPIKey || !a.IsActive() || strings.TrimSpace(a.GetCredential("api_key")) == "" ||
			!isGeminiVertexAccount(a, cfg.GeminiAccountIDs) {
			continue
		}
		seen := map[string]struct{}{}
		for _, model := range cfg.GeminiModels {
			if !a.IsModelSupported(model) {
				continue
			}
			upstream := strings.TrimSpace(a.GetMappedModel(model))
			if upstream == "" {
				upstream = model
			}
			if _, dup := seen[upstream]; dup {
				continue
			}
			seen[upstream] = struct{}{}
			out = append(out, fidelityTarget{Account: a, Model: model, UpstreamModel: upstream,
				Kind: fidelityKindGeminiVT, SignaturePrefix: prefix})
		}
	}
	return out, nil
}

// ---- 请求 ----

var fidelityGeminiTools = []map[string]any{{
	"functionDeclarations": []map[string]any{{
		"name":        "get_weather",
		"description": "Get the current weather for a city",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"city": map[string]any{"type": "string"}},
			"required":   []string{"city"},
		},
	}},
}}

const fidelityGeminiQuestion = "What is the weather in Paris right now? Use the tool."

// geminiFidelityPlainBody 纯文本请求：不带工具与多模态，内容固定以便比较 promptTokenCount。
func geminiFidelityPlainBody() ([]byte, error) {
	return json.Marshal(map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "Reply with the single word OK."}}}},
		"generationConfig": map[string]any{"maxOutputTokens": fidelityGeminiMaxOutputTokens},
	})
}

func geminiFidelityCallBody() ([]byte, error) {
	return json.Marshal(map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": []map[string]any{{"text": fidelityGeminiQuestion}}}},
		"tools":            fidelityGeminiTools,
		"toolConfig":       map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY"}},
		"generationConfig": map[string]any{"maxOutputTokens": fidelityGeminiMaxOutputTokens},
	})
}

func geminiFidelityVideoBody() ([]byte, error) {
	return json.Marshal(map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": []map[string]any{
			{
				"fileData":      map[string]any{"mimeType": "video/mp4", "fileUri": fidelityGeminiVideoURI},
				"videoMetadata": map[string]any{"startOffset": "0s", "endOffset": "10s"},
			},
			{"text": "Which city is shown, and what is the name of the woman who introduces herself? " +
				"Answer in under 15 words. If no video is attached, reply exactly " + fidelityGeminiNoVideo + "."},
		}}},
		"generationConfig": map[string]any{"maxOutputTokens": fidelityGeminiMaxOutputTokens},
	})
}

// geminiFidelityCanaryBody 第二轮函数调用：模型那一轮带上要检验的签名，再回传工具结果。
func geminiFidelityCanaryBody(signature string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": fidelityGeminiQuestion}}},
			{"role": "model", "parts": []map[string]any{{
				"functionCall":     map[string]any{"name": "get_weather", "args": map[string]any{"city": "Paris"}},
				"thoughtSignature": signature,
			}}},
			{"role": "user", "parts": []map[string]any{{
				"functionResponse": map[string]any{"name": "get_weather", "response": map[string]any{"temp_c": 17, "sky": "cloudy"}},
			}}},
		},
		"tools":            fidelityGeminiTools,
		"generationConfig": map[string]any{"maxOutputTokens": fidelityGeminiMaxOutputTokens},
	})
}

// geminiFidelityResponse Gemini generateContent 非流式响应里体检关心的字段。
type geminiFidelityResponse struct {
	CreateTime   *string `json:"createTime"`
	ResponseID   *string `json:"responseId"`
	ModelVersion string  `json:"modelVersion"`
	Candidates   []struct {
		Content struct {
			Parts []struct {
				Text             string          `json:"text"`
				Thought          bool            `json:"thought"`
				ThoughtSignature string          `json:"thoughtSignature"`
				FunctionCall     json.RawMessage `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     *int `json:"promptTokenCount"`
		CandidatesTokenCount *int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   *int `json:"thoughtsTokenCount"`
		PromptTokensDetails  []struct {
			Modality   string `json:"modality"`
			TokenCount int    `json:"tokenCount"`
		} `json:"promptTokensDetails"`
	} `json:"usageMetadata"`
}

// visibleText 拼接非思考部分的正文。
func (r *geminiFidelityResponse) visibleText() string {
	var b strings.Builder
	for _, c := range r.Candidates {
		for _, p := range c.Content.Parts {
			if !p.Thought {
				b.WriteString(p.Text)
			}
		}
	}
	return b.String()
}

// functionCallSignature 返回第一个函数调用及其签名；found=false 表示没有函数调用。
func (r *geminiFidelityResponse) functionCallSignature() (signature string, found bool) {
	for _, c := range r.Candidates {
		for _, p := range c.Content.Parts {
			if len(p.FunctionCall) > 0 && string(p.FunctionCall) != "null" {
				return p.ThoughtSignature, true
			}
		}
	}
	return "", false
}

// missingMeta 返回缺少的 Vertex 元数据字段名。
func (r *geminiFidelityResponse) missingMeta() []string {
	var out []string
	if r.CreateTime == nil || strings.TrimSpace(*r.CreateTime) == "" {
		out = append(out, "createTime")
	}
	if r.ResponseID == nil || strings.TrimSpace(*r.ResponseID) == "" {
		out = append(out, "responseId")
	}
	return out
}

// modalityTokens 返回某种输入模态的 token 数；hasDetails=false 表示响应里没有分模态明细。
func (r *geminiFidelityResponse) modalityTokens(modality string) (tokens int, hasDetails bool) {
	if r.UsageMetadata == nil || len(r.UsageMetadata.PromptTokensDetails) == 0 {
		return 0, false
	}
	for _, d := range r.UsageMetadata.PromptTokensDetails {
		if strings.EqualFold(d.Modality, modality) {
			tokens += d.TokenCount
		}
	}
	return tokens, true
}

// geminiFidelityReply 一次请求的结果：Body 非空表示 HTTP 2xx 且解析成功；Status 为 0 表示没拿到响应。
type geminiFidelityReply struct {
	Status int
	Err    string
	Body   *geminiFidelityResponse
}

// rejected 上游明确拒绝了这个请求（4xx，排除认证、超时与限流）：对 Vertex 的合法请求不该出现。
func (r geminiFidelityReply) rejected() bool {
	switch r.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return r.Body == nil && r.Status >= 400 && r.Status < 500
}

// ---- 判定 ----

type geminiFidelityInput struct {
	Plain, Call, Video, Canary geminiFidelityReply
	// PlainExtra 额外的纯文本采样，只用于检查 Vertex 元数据（出错的忽略）。
	PlainExtra []geminiFidelityReply
	// CanaryFromStore 金丝雀签名来自上一轮通过的体检（否则用的是本轮第一个请求刚拿到的签名）。
	CanaryFromStore bool
	SignaturePrefix string
	Baseline        *int
	Tolerance       int
}

// evaluateGeminiFidelity 按三个请求的结果判定各检查项。
func evaluateGeminiFidelity(in geminiFidelityInput) ChannelFidelityCheck {
	res := ChannelFidelityCheck{
		ProbeKind: fidelityKindGeminiVT, Status: fidelityStatusPass,
		SystemCheck: fidelityCheckSkip, ThinkingCheck: fidelityCheckSkip, JSONCheck: fidelityCheckSkip,
		AnswerCheck: fidelityCheckSkip, PromptCheck: fidelityCheckSkip,
		SignatureCheck: fidelityCheckSkip, MediaCheck: fidelityCheckSkip, CanaryCheck: fidelityCheckSkip, MetaCheck: fidelityCheckSkip,
	}
	plain := in.Plain.Body
	if plain == nil {
		res.Status = fidelityStatusError
		res.Message = truncateRunes("纯文本请求出错："+in.Plain.Err, fidelityMessageRunes)
		return res
	}
	if u := plain.UsageMetadata; u != nil {
		res.PromptTokens = u.PromptTokenCount
		res.CompletionTokens = u.CandidatesTokenCount
		res.ReasoningTokens = u.ThoughtsTokenCount
	}
	call := in.Call.Body
	if call == nil {
		res.Status = fidelityStatusError
		res.Message = truncateRunes("函数调用请求出错："+in.Call.Err, fidelityMessageRunes)
		return res
	}
	signature, found := call.functionCallSignature()
	if !found {
		res.Status = fidelityStatusError
		res.Message = "上游没有按要求返回函数调用，无法检查签名"
		res.ResponseExcerpt = fidelityExcerpt(call.visibleText(), "")
		return res
	}
	res.SignaturePrefix = truncateRunes(signature, 6)
	prefix := in.SignaturePrefix
	if prefix == "" {
		prefix = fidelityGeminiDefaultSignaturePrefix
	}
	res.SignatureCheck = passFail(signature != "" && strings.HasPrefix(signature, prefix))

	var notes []string
	// 按请求记下缺少的元数据：混用的上游往往只有某一类请求被转走。
	var missing []string
	noteMeta := func(label string, r *geminiFidelityResponse) {
		if m := r.missingMeta(); len(m) > 0 {
			missing = append(missing, label+"缺 "+strings.Join(m, "、"))
		}
	}
	noteMeta("纯文本", plain)
	for i, extra := range in.PlainExtra {
		if extra.Body != nil {
			noteMeta(fmt.Sprintf("纯文本#%d", i+2), extra.Body)
		}
	}
	noteMeta("函数调用", call)

	// 视频：看过才答得出；有分模态明细时还必须计了 VIDEO token。
	switch {
	case in.Video.Body != nil:
		v := in.Video.Body
		noteMeta("视频", v)
		text := v.visibleText()
		res.ResponseExcerpt = fidelityExcerpt("[视频] "+strings.TrimSpace(text), "")
		videoTokens, hasDetails := v.modalityTokens("VIDEO")
		lower := strings.ToLower(text)
		seen := !strings.Contains(text, fidelityGeminiNoVideo)
		if seen {
			seen = false
			for _, kw := range fidelityGeminiVideoKeywords {
				if strings.Contains(lower, kw) {
					seen = true
					break
				}
			}
		}
		switch {
		case hasDetails && videoTokens == 0:
			res.MediaCheck = fidelityCheckFail
			res.mediaDetail = "返回里没有 VIDEO token，视频被丢掉了"
		case !seen:
			res.MediaCheck = fidelityCheckFail
			res.mediaDetail = "答不出视频内容：" + sanitizeSecurityText(text, 60)
		default:
			res.MediaCheck = fidelityCheckPass
		}
	case in.Video.rejected():
		res.MediaCheck = fidelityCheckFail
		res.mediaDetail = sanitizeSecurityText(in.Video.Err, 120)
	default:
		notes = append(notes, "视频请求出错："+sanitizeSecurityText(in.Video.Err, 120))
	}

	// 金丝雀：签名被拒说明请求到了另一个签名体系（非 Vertex）。
	switch {
	case in.Canary.Body != nil:
		noteMeta("金丝雀", in.Canary.Body)
		res.CanaryCheck = fidelityCheckPass
	case in.Canary.rejected():
		res.CanaryCheck = fidelityCheckFail
		res.canaryDetail = sanitizeSecurityText(in.Canary.Err, 120)
		if !in.CanaryFromStore {
			res.canaryDetail += "；本轮刚拿到的签名也不认"
		}
	case in.Canary.Status == 0 && in.Canary.Err == "":
		notes = append(notes, "没有可用的签名做金丝雀")
	default:
		notes = append(notes, "金丝雀请求出错："+sanitizeSecurityText(in.Canary.Err, 120))
	}

	if len(missing) > 0 {
		res.MetaCheck = fidelityCheckFail
		res.metaDetail = strings.Join(missing, "；")
	} else {
		res.MetaCheck = fidelityCheckPass
	}

	if res.PromptTokens != nil && in.Baseline != nil {
		diff := *res.PromptTokens - *in.Baseline
		if diff < 0 {
			diff = -diff
		}
		res.PromptCheck = passFail(diff <= in.Tolerance)
		b := *in.Baseline
		res.BaselinePromptTokens = &b
	}

	failed, incomplete := false, false
	for _, v := range []string{res.SignatureCheck, res.MediaCheck, res.CanaryCheck, res.MetaCheck, res.PromptCheck} {
		if v == fidelityCheckFail {
			failed = true
		}
	}
	for _, v := range []string{res.SignatureCheck, res.MediaCheck, res.CanaryCheck, res.MetaCheck} {
		if v == fidelityCheckSkip {
			incomplete = true
		}
	}
	parts := res.failures()
	parts = append(parts, notes...)
	switch {
	case failed:
		res.Status = fidelityStatusFail
	case incomplete:
		// 有请求出错，没法确认全部通过：记为出错，不算恢复，也不告警。
		res.Status = fidelityStatusError
	}
	res.Message = truncateRunes(strings.Join(parts, "；"), fidelityMessageRunes)
	return res
}

// ---- 发请求 ----

// doGeminiProbe 依次发纯文本（3 次）、函数调用、gs:// 视频、金丝雀。纯文本或函数调用失败时不再往下发。
func (p *channelFidelityProber) doGeminiProbe(ctx context.Context, t fidelityTarget, baseline *int, tolerance int) ChannelFidelityCheck {
	errorCheck := func(msg string) ChannelFidelityCheck {
		return evaluateGeminiFidelity(geminiFidelityInput{Call: geminiFidelityReply{Err: msg}})
	}
	a := t.Account
	apiKey := strings.TrimSpace(a.GetCredential("api_key"))
	if apiKey == "" {
		return errorCheck("账号没有配置 API Key")
	}
	validated, err := cnValidateProbeURL(p.cfg, a.GetGeminiBaseURL(geminicli.AIStudioBaseURL))
	if err != nil {
		return errorCheck("上游地址被出站安全策略拒绝")
	}
	target, err := buildGeminiAIStudioModelActionURL(validated, t.UpstreamModel, "generateContent", false)
	if err != nil {
		return errorCheck("无法构造上游地址：" + err.Error())
	}
	proxyURL := ""
	if a.ProxyID != nil && p.proxies != nil {
		proxy, err := p.proxies.GetByID(ctx, *a.ProxyID)
		if err != nil || proxy == nil {
			return errorCheck("无法读取账号代理")
		}
		proxyURL = proxy.URL()
	}
	send := func(body []byte, buildErr error) geminiFidelityReply {
		if buildErr != nil {
			return geminiFidelityReply{Err: "无法构造体检请求"}
		}
		return p.geminiFidelityCall(ctx, target, apiKey, proxyURL, a.ID, body)
	}

	in := geminiFidelityInput{SignaturePrefix: t.SignaturePrefix, Baseline: baseline, Tolerance: tolerance}
	in.Plain = send(geminiFidelityPlainBody())
	if in.Plain.Body != nil {
		for i := 1; i < fidelityGeminiPlainSamples; i++ {
			in.PlainExtra = append(in.PlainExtra, send(geminiFidelityPlainBody()))
		}
		in.Call = send(geminiFidelityCallBody())
	}
	var fresh string
	if in.Call.Body != nil {
		fresh, _ = in.Call.Body.functionCallSignature()
	}
	if in.Call.Body != nil {
		in.Video = send(geminiFidelityVideoBody())
		canary := strings.TrimSpace(t.Canary)
		in.CanaryFromStore = canary != ""
		if canary == "" {
			canary = fresh
		}
		if canary != "" {
			in.Canary = send(geminiFidelityCanaryBody(canary))
		}
	}
	res := evaluateGeminiFidelity(in)
	for _, r := range []geminiFidelityReply{in.Call, in.Plain} {
		if r.Status != 0 {
			status := r.Status
			res.HTTPStatus = &status
			break
		}
	}
	// 只有全部通过时才把本轮签名留作下一轮的金丝雀。
	if res.Status == fidelityStatusPass {
		res.CanarySignature = fresh
	}
	return res
}

// geminiFidelityCall 发一个 Gemini 原生 generateContent 请求（x-goog-api-key 认证，不跟随重定向）。
func (p *channelFidelityProber) geminiFidelityCall(ctx context.Context, target, apiKey, proxyURL string, accountID int64, body []byte) geminiFidelityReply {
	ctx, cancel := context.WithTimeout(ctx, fidelityProbeTimeout)
	defer cancel()
	ctx = WithHTTPUpstreamRedirectsDisabled(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return geminiFidelityReply{Err: "无法创建体检请求"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BirdSub2Api-Fidelity/1.0")
	req.Header.Set("x-goog-api-key", apiKey)
	resp, err := p.http.Do(req, proxyURL, accountID, 1)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return geminiFidelityReply{Err: fmt.Sprintf("上游 %d 秒内没有返回", int(fidelityProbeTimeout.Seconds()))}
		}
		return geminiFidelityReply{Err: "连接上游失败"}
	}
	defer func() { _ = resp.Body.Close() }()
	status := resp.StatusCode
	raw, err := io.ReadAll(io.LimitReader(resp.Body, fidelityResponseLimit+1))
	if err != nil {
		return geminiFidelityReply{Status: status, Err: "读取上游响应失败"}
	}
	if len(raw) > fidelityResponseLimit {
		return geminiFidelityReply{Status: status, Err: "上游响应过大"}
	}
	if status < 200 || status >= 300 {
		return geminiFidelityReply{Status: status, Err: "HTTP " + strconv.Itoa(status) + "：" + geminiErrorMessage(raw)}
	}
	var parsed geminiFidelityResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return geminiFidelityReply{Status: status, Err: "上游返回的不是 Gemini JSON：" + sanitizeSecurityText(string(raw), 120)}
	}
	return geminiFidelityReply{Status: status, Body: &parsed}
}

// geminiErrorMessage 取 {"error":{"message":...}}，取不到时返回原文节选。
func geminiErrorMessage(raw []byte) string {
	var wrapped struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && strings.TrimSpace(wrapped.Error.Message) != "" {
		return sanitizeSecurityText(wrapped.Error.Message, 200)
	}
	return sanitizeSecurityText(string(raw), 200)
}
