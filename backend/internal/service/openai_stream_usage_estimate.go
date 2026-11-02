package service

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// 流式请求"内容已经发给客户、却没拿到上游 usage"时的估算计费（与 new-api 的
// ResponseText2Usage 一致）：上游中途断流、或终止事件不带 usage 时，此前按 0 token
// 记账——客户拿到了内容，我们却一分没收，而上游通常照样向我们扣费。
//
// 估算口径：
//   - 输出：按已转发给客户的文本（正文、思考内容、工具调用参数）估算 token；
//   - 输入：按发给上游的请求体文本估算 token；其中命中缓存的比例取同一 API Key + 模型
//     最近成功请求的缓存命中率（指数滑动平均），没有历史时整段按缓存读计，宁少勿多，
//     避免一个中断请求比完整请求还贵。

// streamOutputTextMeter 累计流中已转发的文本量，用于估算输出 token。只计数，不保存文本。
type streamOutputTextMeter struct {
	cjkRunes   int
	otherBytes int
}

func (m *streamOutputTextMeter) addText(s string) {
	if m == nil || s == "" {
		return
	}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if isEstimateCJKRune(r) {
			m.cjkRunes++
		} else {
			m.otherBytes += size
		}
		i += size
	}
}

// observeChatChunk 统计 Chat Completions 流式分片中的输出文本。
func (m *streamOutputTextMeter) observeChatChunk(payload string) {
	if m == nil || payload == "" || !strings.Contains(payload, `"delta"`) {
		return
	}
	gjson.Get(payload, "choices").ForEach(func(_, choice gjson.Result) bool {
		delta := choice.Get("delta")
		m.addText(delta.Get("content").String())
		m.addText(delta.Get("reasoning_content").String())
		m.addText(delta.Get("reasoning").String())
		delta.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
			m.addText(call.Get("function.name").String())
			m.addText(call.Get("function.arguments").String())
			return true
		})
		return true
	})
}

// observeResponsesEvent 统计 Responses API 流式事件中的输出文本。
func (m *streamOutputTextMeter) observeResponsesEvent(payload []byte, eventType string) {
	if m == nil || len(payload) == 0 {
		return
	}
	switch strings.TrimSpace(eventType) {
	case "response.output_text.delta",
		"response.reasoning_text.delta",
		"response.reasoning_summary_text.delta",
		"response.function_call_arguments.delta",
		"response.custom_tool_call_input.delta",
		"response.refusal.delta":
		m.addText(gjson.GetBytes(payload, "delta").String())
	}
}

func (m *streamOutputTextMeter) tokens() int {
	if m == nil {
		return 0
	}
	return estimateTokensFromCounts(m.cjkRunes, m.otherBytes)
}

// estimateTokensFromCounts 中日韩字符约 0.7 token/字，其余约 4 字节/token，向上取整。
// 偏保守：宁可少估，避免客户为没收到的内容付费。
func estimateTokensFromCounts(cjkRunes, otherBytes int) int {
	if cjkRunes <= 0 && otherBytes <= 0 {
		return 0
	}
	return int(math.Ceil(float64(cjkRunes)*0.7 + float64(otherBytes)/4.0))
}

func isEstimateCJKRune(r rune) bool {
	return (r >= 0x2E80 && r <= 0x9FFF) || (r >= 0xAC00 && r <= 0xD7AF) || (r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFF00 && r <= 0xFFEF)
}

// estimateRequestInputTokens 估算请求体中的文本 token：遍历所有字符串值（JSON 键不计），
// 跳过图片/文件等内联数据。适用于 Chat Completions 与 Responses 两种请求格式。
func estimateRequestInputTokens(body []byte) int {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0
	}
	var meter streamOutputTextMeter
	var walk func(key string, v gjson.Result)
	walk = func(key string, v gjson.Result) {
		switch {
		case v.IsObject() || v.IsArray():
			v.ForEach(func(k, child gjson.Result) bool {
				walk(k.String(), child)
				return true
			})
		case v.Type == gjson.String:
			if isEstimateSkippedInputField(key, v.Str) {
				return
			}
			meter.addText(v.Str)
		}
	}
	walk("", gjson.ParseBytes(body))
	return meter.tokens()
}

func isEstimateSkippedInputField(key, value string) bool {
	switch key {
	case "model", "role", "type", "id", "call_id", "tool_call_id", "image_url", "url", "file_data", "data", "file_id", "encrypted_content", "signature":
		return true
	}
	if strings.HasPrefix(value, "data:") {
		return true
	}
	// 很长且没有空白的字符串基本是 base64 之类的内联数据。
	return len(value) > 2048 && !strings.ContainsAny(value[:2048], " \n\t")
}

// openAIPromptCacheRatios 记录 API Key + 模型最近的缓存命中率（指数滑动平均），只在本进程内存里。
var openAIPromptCacheRatios = struct {
	sync.Mutex
	m map[string]float64
}{m: make(map[string]float64)}

const openAIPromptCacheRatioMaxKeys = 20000

func openAIPromptCacheRatioKey(apiKeyID int64, model string) string {
	return strconv.FormatInt(apiKeyID, 10) + "|" + strings.ToLower(strings.TrimSpace(model))
}

func rememberOpenAIPromptCacheRatio(apiKeyID int64, model string, usage OpenAIUsage) {
	if apiKeyID <= 0 || usage.InputTokens <= 0 {
		return
	}
	ratio := float64(usage.CacheReadInputTokens) / float64(usage.InputTokens)
	ratio = math.Max(0, math.Min(1, ratio))
	key := openAIPromptCacheRatioKey(apiKeyID, model)
	openAIPromptCacheRatios.Lock()
	defer openAIPromptCacheRatios.Unlock()
	if prev, ok := openAIPromptCacheRatios.m[key]; ok {
		openAIPromptCacheRatios.m[key] = prev*0.7 + ratio*0.3
		return
	}
	if len(openAIPromptCacheRatios.m) >= openAIPromptCacheRatioMaxKeys {
		openAIPromptCacheRatios.m = make(map[string]float64)
	}
	openAIPromptCacheRatios.m[key] = ratio
}

// lookupOpenAIPromptCacheRatio 没有历史时按 1（全部按缓存读计）——宁少勿多。
func lookupOpenAIPromptCacheRatio(apiKeyID int64, model string) float64 {
	openAIPromptCacheRatios.Lock()
	defer openAIPromptCacheRatios.Unlock()
	if ratio, ok := openAIPromptCacheRatios.m[openAIPromptCacheRatioKey(apiKeyID, model)]; ok {
		return ratio
	}
	return 1
}

func openAIUsageIsEmpty(u OpenAIUsage) bool {
	return u.InputTokens <= 0 && u.OutputTokens <= 0 && u.CacheReadInputTokens <= 0 && u.CacheCreationInputTokens <= 0
}

// settleOpenAIStreamUsage 在流式请求收尾时调用：上游给了 usage 就记下缓存命中率；
// 没给但已经向客户输出了内容，就按输出文本和请求体估算 usage。返回是否做了估算。
func settleOpenAIStreamUsage(c *gin.Context, usage *OpenAIUsage, meter *streamOutputTextMeter, upstreamBody []byte, model string, accountID int64, reason string) bool {
	if usage == nil {
		return false
	}
	apiKeyID := getAPIKeyIDFromContext(c)
	if !openAIUsageIsEmpty(*usage) {
		rememberOpenAIPromptCacheRatio(apiKeyID, model, *usage)
		return false
	}
	outputTokens := meter.tokens()
	if outputTokens <= 0 {
		return false
	}
	inputTokens := estimateRequestInputTokens(upstreamBody)
	ratio := lookupOpenAIPromptCacheRatio(apiKeyID, model)
	usage.InputTokens = inputTokens
	usage.CacheReadInputTokens = int(math.Round(float64(inputTokens) * ratio))
	usage.OutputTokens = outputTokens
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	logger.FromContext(ctx).Warn("openai_usage.estimated_missing_stream_usage",
		zap.String("component", "service.openai_gateway"),
		zap.String("reason", reason),
		zap.Int64("account_id", accountID),
		zap.Int64("api_key_id", apiKeyID),
		zap.String("model", model),
		zap.Int("estimated_input_tokens", inputTokens),
		zap.Int("estimated_cache_read_tokens", usage.CacheReadInputTokens),
		zap.Int("estimated_output_tokens", outputTokens),
		zap.Float64("cache_ratio", ratio),
	)
	return true
}
