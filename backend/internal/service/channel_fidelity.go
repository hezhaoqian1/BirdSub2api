package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// 渠道保真体检：定时用每个上游账号自己的 key 直接发一个很小的请求，检查上游有没有
// 偷偷改掉我们的参数——丢 system 提示词、关不掉思考、不认 JSON 模式、注入隐藏提示词。
// 这类问题不会报错，现有的可用性监控看不出来，只能看返回内容。
//
// 每次请求：system 里放一个随机暗号并要求按 JSON 输出，同时带关闭思考的两种写法
// （thinking.type=disabled 与 reasoning_effort=none）和 response_format=json_object，
// user 里放一道随机加法题。判定见 evaluateChannelFidelity。

const (
	fidelityCheckPass = "pass"
	fidelityCheckFail = "fail"
	fidelityCheckSkip = "skip"

	fidelityStatusPass  = "pass"
	fidelityStatusFail  = "fail"
	fidelityStatusError = "error"

	fidelityProbeTimeout      = 90 * time.Second
	fidelityProbeMaxTokens    = 300
	fidelityResponseLimit     = 256 * 1024
	fidelityExcerptRunes      = 600
	fidelityReasoningExcerpt  = 300
	fidelityMessageRunes      = 300
	fidelityMaxModels         = 10
	fidelityDefaultInterval   = 15
	fidelityDefaultRecheck    = 60
	fidelityDefaultTolerance  = 10
	fidelitySupportedPlatform = PlatformDeepseek
)

// ChannelFidelityConfig 保真体检配置（存于 settings.channel_fidelity_config）。
type ChannelFidelityConfig struct {
	// Enabled 是否定时体检。关闭后仍可在管理端手动体检。
	Enabled bool `json:"enabled"`
	// IntervalMinutes 两轮体检的间隔（分钟）。
	IntervalMinutes int `json:"interval_minutes"`
	// RecheckSeconds 首次不通过后隔多少秒复测；复测仍不通过才告警。0 表示不复测，首次不通过即告警。
	RecheckSeconds int `json:"recheck_seconds"`
	// Models 要体检的模型（客户请求的模型名）；账号不支持的模型自动跳过，按账号映射换算成上游模型。
	Models []string `json:"models"`
	// PromptTokenTolerance prompt_tokens 与基线的允许偏差。
	PromptTokenTolerance int `json:"prompt_token_tolerance"`
}

// DefaultChannelFidelityConfig 默认配置：默认关闭，DeepSeek flash 与 pro 各测一次。
func DefaultChannelFidelityConfig() ChannelFidelityConfig {
	return ChannelFidelityConfig{
		Enabled:              false,
		IntervalMinutes:      fidelityDefaultInterval,
		RecheckSeconds:       fidelityDefaultRecheck,
		Models:               []string{"deepseek-v4.1-flash", "deepseek-v4-pro"},
		PromptTokenTolerance: fidelityDefaultTolerance,
	}
}

func (c *ChannelFidelityConfig) normalize() {
	models := make([]string, 0, len(c.Models))
	seen := map[string]struct{}{}
	for _, m := range c.Models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		models = append(models, m)
	}
	c.Models = models
}

// Validate 校验取值范围。
func (c ChannelFidelityConfig) Validate() error {
	if c.IntervalMinutes < 5 || c.IntervalMinutes > 1440 {
		return errors.New("interval_minutes must be between 5 and 1440")
	}
	if c.RecheckSeconds != 0 && (c.RecheckSeconds < 15 || c.RecheckSeconds > 600) {
		return errors.New("recheck_seconds must be 0 (no recheck) or between 15 and 600")
	}
	if c.PromptTokenTolerance < 0 || c.PromptTokenTolerance > 1000 {
		return errors.New("prompt_token_tolerance must be between 0 and 1000")
	}
	if len(c.Models) == 0 {
		return errors.New("models must not be empty")
	}
	if len(c.Models) > fidelityMaxModels {
		return fmt.Errorf("at most %d models", fidelityMaxModels)
	}
	for _, m := range c.Models {
		if len(m) > 100 {
			return fmt.Errorf("model name %q is too long", m)
		}
	}
	return nil
}

// fidelityPrompt 一次体检的随机题目。暗号与加数都用固定位数的数字，
// 保证同一账号同一模型每次的 prompt_tokens 不变，才能和基线逐个比较。
type fidelityPrompt struct {
	Code   string
	A, B   int
	Answer int
}

func newFidelityPrompt() fidelityPrompt {
	a := 100 + rand.IntN(900)          //nolint:gosec // 体检题目，无安全含义
	b := 100 + rand.IntN(900)          //nolint:gosec
	code := 100000 + rand.IntN(900000) //nolint:gosec
	return fidelityPrompt{Code: strconv.Itoa(code), A: a, B: b, Answer: a + b}
}

func (p fidelityPrompt) systemText() string {
	return "你是接口自检程序。本次暗号是 " + p.Code +
		"。请只输出一个 JSON 对象，格式为 {\"code\": \"暗号\", \"answer\": 计算结果}，不要输出任何其它内容。"
}

func (p fidelityPrompt) userText() string {
	return fmt.Sprintf("计算 %d + %d 等于多少？", p.A, p.B)
}

func (p fidelityPrompt) requestBody(upstreamModel string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model": upstreamModel,
		"messages": []map[string]string{
			{"role": "system", "content": p.systemText()},
			{"role": "user", "content": p.userText()},
		},
		"thinking":         map[string]string{"type": "disabled"},
		"reasoning_effort": "none",
		"response_format":  map[string]string{"type": "json_object"},
		"max_tokens":       fidelityProbeMaxTokens,
		"temperature":      0,
		"stream":           false,
	})
}

// fidelityResponse 上游 Chat Completions 非流式响应里体检关心的字段。
type fidelityResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			Reasoning        *string `json:"reasoning"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens            *int `json:"prompt_tokens"`
		CompletionTokens        *int `json:"completion_tokens"`
		CompletionTokensDetails *struct {
			ReasoningTokens *int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// ChannelFidelityCheck 一次体检结果。
type ChannelFidelityCheck struct {
	ID                   int64     `json:"id"`
	AccountID            int64     `json:"account_id"`
	Model                string    `json:"model"`
	UpstreamModel        string    `json:"upstream_model"`
	CheckedAt            time.Time `json:"checked_at"`
	Status               string    `json:"status"`
	IsRecheck            bool      `json:"is_recheck"`
	SystemCheck          string    `json:"system_check"`
	ThinkingCheck        string    `json:"thinking_check"`
	JSONCheck            string    `json:"json_check"`
	AnswerCheck          string    `json:"answer_check"`
	PromptCheck          string    `json:"prompt_check"`
	PromptTokens         *int      `json:"prompt_tokens"`
	BaselinePromptTokens *int      `json:"baseline_prompt_tokens"`
	CompletionTokens     *int      `json:"completion_tokens"`
	ReasoningTokens      *int      `json:"reasoning_tokens"`
	ReasoningChars       int       `json:"reasoning_chars"`
	LatencyMs            *int      `json:"latency_ms"`
	HTTPStatus           *int      `json:"http_status"`
	Message              string    `json:"message"`
	ResponseExcerpt      string    `json:"response_excerpt"`
}

// failures 返回不通过项的中文描述（用于告警与列表提示）。
func (c ChannelFidelityCheck) failures() []string {
	var out []string
	if c.ThinkingCheck == fidelityCheckFail {
		out = append(out, fmt.Sprintf("关闭思考无效（仍返回 %d 字思考）", c.ReasoningChars))
	}
	if c.SystemCheck == fidelityCheckFail {
		out = append(out, "system 提示词未生效（暗号没出现）")
	}
	if c.JSONCheck == fidelityCheckFail {
		out = append(out, "JSON 模式无效（返回的不是 JSON）")
	}
	if c.AnswerCheck == fidelityCheckFail {
		out = append(out, "计算题答错")
	}
	if c.PromptCheck == fidelityCheckFail && c.PromptTokens != nil && c.BaselinePromptTokens != nil {
		out = append(out, fmt.Sprintf("prompt_tokens %d，基线 %d（可能被注入或丢弃了提示词）",
			*c.PromptTokens, *c.BaselinePromptTokens))
	}
	return out
}

var fidelityNumberRegex = regexp.MustCompile(`-?\d+`)

// evaluateChannelFidelity 按上游返回判定各检查项。baseline 为 nil 表示还没有基线（prompt 项记为 skip）。
func evaluateChannelFidelity(p fidelityPrompt, resp fidelityResponse, baseline *int, tolerance int) ChannelFidelityCheck {
	res := ChannelFidelityCheck{
		Status:        fidelityStatusPass,
		SystemCheck:   fidelityCheckSkip,
		ThinkingCheck: fidelityCheckSkip,
		JSONCheck:     fidelityCheckSkip,
		AnswerCheck:   fidelityCheckSkip,
		PromptCheck:   fidelityCheckSkip,
	}
	if resp.Usage != nil {
		res.PromptTokens = resp.Usage.PromptTokens
		res.CompletionTokens = resp.Usage.CompletionTokens
		if d := resp.Usage.CompletionTokensDetails; d != nil {
			res.ReasoningTokens = d.ReasoningTokens
		}
	}
	if len(resp.Choices) == 0 {
		res.Status = fidelityStatusError
		res.Message = "上游返回里没有 choices"
		return res
	}
	choice := resp.Choices[0]
	content := ""
	if choice.Message.Content != nil {
		content = *choice.Message.Content
	}
	reasoning := ""
	if choice.Message.ReasoningContent != nil {
		reasoning = *choice.Message.ReasoningContent
	} else if choice.Message.Reasoning != nil {
		reasoning = *choice.Message.Reasoning
	}
	res.ReasoningChars = len([]rune(reasoning))
	res.ResponseExcerpt = fidelityExcerpt(content, reasoning)

	// 1. 关闭思考：不能有思考内容，也不能计思考 token。
	thinkingFailed := res.ReasoningChars > 0 || (res.ReasoningTokens != nil && *res.ReasoningTokens > 0)
	res.ThinkingCheck = passFail(!thinkingFailed)

	trimmed := strings.TrimSpace(content)
	if trimmed == "" && thinkingFailed {
		// 思考把 max_tokens 用完了，正文为空：其余几项无从判断，不算失败。
		res.Message = "思考占满了输出上限，正文为空"
	} else {
		var obj map[string]any
		jsonOK := json.Unmarshal([]byte(trimmed), &obj) == nil && obj != nil
		res.JSONCheck = passFail(jsonOK)

		// 2. system：暗号原样出现（优先看 JSON 的 code 字段）。
		systemOK := strings.Contains(content, p.Code)
		if jsonOK {
			systemOK = systemOK || fmt.Sprint(obj["code"]) == p.Code
		}
		res.SystemCheck = passFail(systemOK)

		// 3. 计算题：answer 字段或正文里出现正确结果。
		expected := strconv.Itoa(p.Answer)
		answerOK := false
		if jsonOK {
			answerOK = jsonNumberEquals(obj["answer"], p.Answer)
		}
		if !answerOK {
			for _, m := range fidelityNumberRegex.FindAllString(content, -1) {
				if m == expected {
					answerOK = true
					break
				}
			}
		}
		res.AnswerCheck = passFail(answerOK)
	}

	// 4. prompt_tokens 与基线比较。
	if res.PromptTokens != nil && baseline != nil {
		diff := *res.PromptTokens - *baseline
		if diff < 0 {
			diff = -diff
		}
		res.PromptCheck = passFail(diff <= tolerance)
		b := *baseline
		res.BaselinePromptTokens = &b
	}

	for _, v := range []string{res.SystemCheck, res.ThinkingCheck, res.JSONCheck, res.AnswerCheck, res.PromptCheck} {
		if v == fidelityCheckFail {
			res.Status = fidelityStatusFail
		}
	}
	if res.Status == fidelityStatusFail && res.Message == "" {
		res.Message = strings.Join(res.failures(), "；")
	} else if res.Status == fidelityStatusFail {
		res.Message = strings.Join(res.failures(), "；") + "；" + res.Message
	}
	res.Message = truncateRunes(res.Message, fidelityMessageRunes)
	return res
}

func passFail(ok bool) string {
	if ok {
		return fidelityCheckPass
	}
	return fidelityCheckFail
}

func jsonNumberEquals(v any, want int) bool {
	switch n := v.(type) {
	case float64:
		return n == float64(want)
	case string:
		return strings.TrimSpace(n) == strconv.Itoa(want)
	default:
		return false
	}
}

func fidelityExcerpt(content, reasoning string) string {
	out := truncateRunes(strings.TrimSpace(content), fidelityExcerptRunes)
	if strings.TrimSpace(reasoning) != "" {
		out += "\n[思考] " + truncateRunes(strings.TrimSpace(reasoning), fidelityReasoningExcerpt)
	}
	return out
}

// channelFidelityProber 发起体检请求。复用上游余额查询的出站 HTTP 客户端
// （与网关转发的连接池隔离）与 URL 安全策略。
type channelFidelityProber struct {
	accounts AccountRepository
	proxies  ProxyRepository
	http     HTTPUpstream
	cfg      *config.Config
}

func newChannelFidelityProber(balances *UpstreamBalanceService) *channelFidelityProber {
	if balances == nil || balances.accounts == nil || balances.http == nil {
		return nil
	}
	return &channelFidelityProber{
		accounts: balances.accounts,
		proxies:  balances.proxies,
		http:     balances.http,
		cfg:      balances.cfg,
	}
}

// fidelityTarget 一个待体检的（账号，模型）。
type fidelityTarget struct {
	Account       *Account
	Model         string
	UpstreamModel string
}

// fidelityTargets 列出所有启用中的 DeepSeek API Key 账号及其支持的体检模型（同一上游模型只测一次）。
func (p *channelFidelityProber) targets(ctx context.Context, models []string) ([]fidelityTarget, error) {
	accounts, err := p.accounts.ListByPlatform(ctx, fidelitySupportedPlatform)
	if err != nil {
		return nil, err
	}
	var out []fidelityTarget
	for i := range accounts {
		a := &accounts[i]
		if a.Type != AccountTypeAPIKey || !a.IsActive() || strings.TrimSpace(a.GetOpenAIProtocolAPIKey()) == "" {
			continue
		}
		seen := map[string]struct{}{}
		for _, model := range models {
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
			out = append(out, fidelityTarget{Account: a, Model: model, UpstreamModel: upstream})
		}
	}
	return out, nil
}

// probe 对一个目标发起一次体检请求并判定结果（不写库）。
func (p *channelFidelityProber) probe(ctx context.Context, t fidelityTarget, baseline *int, tolerance int) ChannelFidelityCheck {
	prompt := newFidelityPrompt()
	started := time.Now()
	res := p.doProbe(ctx, t, prompt, baseline, tolerance)
	latency := int(time.Since(started) / time.Millisecond)
	res.LatencyMs = &latency
	res.AccountID = t.Account.ID
	res.Model = t.Model
	res.UpstreamModel = t.UpstreamModel
	res.CheckedAt = time.Now()
	return res
}

func (p *channelFidelityProber) doProbe(ctx context.Context, t fidelityTarget, prompt fidelityPrompt, baseline *int, tolerance int) ChannelFidelityCheck {
	fail := func(msg string, status *int) ChannelFidelityCheck {
		return ChannelFidelityCheck{
			Status: fidelityStatusError, SystemCheck: fidelityCheckSkip, ThinkingCheck: fidelityCheckSkip,
			JSONCheck: fidelityCheckSkip, AnswerCheck: fidelityCheckSkip, PromptCheck: fidelityCheckSkip,
			Message: truncateRunes(msg, fidelityMessageRunes), HTTPStatus: status,
		}
	}
	a := t.Account
	base := strings.TrimSpace(a.GetOpenAIBaseURL())
	if base == "" {
		return fail("账号没有配置上游地址", nil)
	}
	validated, err := cnValidateProbeURL(p.cfg, base)
	if err != nil {
		return fail("上游地址被出站安全策略拒绝", nil)
	}
	target := buildOpenAIChatCompletionsURL(validated)
	body, err := prompt.requestBody(t.UpstreamModel)
	if err != nil {
		return fail("无法构造体检请求", nil)
	}

	ctx, cancel := context.WithTimeout(ctx, fidelityProbeTimeout)
	defer cancel()
	ctx = WithHTTPUpstreamRedirectsDisabled(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fail("无法创建体检请求", nil)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BirdSub2Api-Fidelity/1.0")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.GetOpenAIProtocolAPIKey()))

	proxyURL := ""
	if a.ProxyID != nil && p.proxies != nil {
		proxy, err := p.proxies.GetByID(ctx, *a.ProxyID)
		if err != nil || proxy == nil {
			return fail("无法读取账号代理", nil)
		}
		proxyURL = proxy.URL()
	}
	resp, err := p.http.Do(req, proxyURL, a.ID, 1)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail(fmt.Sprintf("上游 %d 秒内没有返回", int(fidelityProbeTimeout/time.Second)), nil)
		}
		return fail("连接上游失败", nil)
	}
	defer func() { _ = resp.Body.Close() }()
	status := resp.StatusCode
	raw, err := io.ReadAll(io.LimitReader(resp.Body, fidelityResponseLimit+1))
	if err != nil {
		return fail("读取上游响应失败", &status)
	}
	if len(raw) > fidelityResponseLimit {
		return fail("上游响应过大", &status)
	}
	if status < 200 || status >= 300 {
		return fail(fmt.Sprintf("上游返回 HTTP %d：%s", status, sanitizeSecurityText(string(raw), 200)), &status)
	}
	var parsed fidelityResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fail("上游返回的不是 Chat Completions JSON："+sanitizeSecurityText(string(raw), 160), &status)
	}
	res := evaluateChannelFidelity(prompt, parsed, baseline, tolerance)
	res.HTTPStatus = &status
	return res
}
