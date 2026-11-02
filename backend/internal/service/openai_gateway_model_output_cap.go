package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// hy4MaxOutputTokensCap 是腾讯混元 Hy4 的输出 token 上限。
//
// 实测（2026-11）：Aizzz 的 hy4 路由对 max_output_tokens > 64000 直接返回 500
// "max_output_tokens is set to 65536, but the selected model on this route allows
// at most 64000 output tokens"。网关把它转成 502，并让该账号短暂冷却；分组里只有
// 一个 hy4 账号时，冷却期内所有正常请求都会 503。客户端（Claude Agent SDK 经
// 转换层）常带 max_tokens=65536，因此在出站前把输出上限单向压到 64000。
const hy4MaxOutputTokensCap int64 = 64000

// responsesOutputLimitKeys / chatOutputLimitKeys 是两种出站协议里的输出上限字段。
var (
	responsesOutputLimitKeys = []string{"max_output_tokens"}
	chatOutputLimitKeys      = []string{"max_tokens", "max_completion_tokens"}
)

// isHy4Model 判断模型名是否为 Hy4 系列（hy4-preview / HY4-preview / tencent/hy4-…）。
func isHy4Model(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "hy4")
}

// modelMaxOutputTokensCap 返回模型级输出 token 上限：任一候选模型（映射后的出站
// 模型、计费模型、客户端原始模型）命中即生效；未命中返回 0（不限制）。
func modelMaxOutputTokensCap(models ...string) int64 {
	for _, model := range models {
		if isHy4Model(model) {
			return hy4MaxOutputTokensCap
		}
	}
	return 0
}

// clampModelMaxOutputTokens 把 body 中超过模型上限的输出字段（keys）单向压到上限。
// 未命中模型、字段缺失、非数值或未超限时原样返回；sjson 出错时返回原始 body。
func clampModelMaxOutputTokens(body []byte, keys []string, models ...string) []byte {
	limit := modelMaxOutputTokensCap(models...)
	if limit <= 0 || len(body) == 0 {
		return body
	}
	out := body
	for _, key := range keys {
		value := gjson.GetBytes(out, key)
		if !value.Exists() || value.Type != gjson.Number || value.Int() <= limit {
			continue
		}
		updated, err := sjson.SetBytes(out, key, limit)
		if err != nil {
			return body
		}
		logger.L().Debug("openai gateway: clamped max output tokens to model cap",
			zap.String("field", key),
			zap.Int64("requested", value.Int()),
			zap.Int64("cap", limit),
		)
		out = updated
	}
	return out
}

// modelResponsesMaxOutputTokensClamp 是原生 /v1/responses 路径（patch 机制）的模型级
// clamp：返回需要 patch 到 max_output_tokens 的上限值。非 Codex 客户端的 OpenAI 平台
// 请求里 max_tokens 会被归一化成 max_output_tokens，此时以 max_tokens 为生效值。
func modelResponsesMaxOutputTokensClamp(account *Account, body []byte, normalizesMaxTokens bool, models ...string) (int64, bool) {
	limit := modelMaxOutputTokensCap(models...)
	if limit <= 0 || account == nil {
		return 0, false
	}
	value := gjson.GetBytes(body, "max_output_tokens")
	if !value.Exists() && normalizesMaxTokens && account.Platform == PlatformOpenAI {
		value = gjson.GetBytes(body, "max_tokens")
	}
	if !value.Exists() || value.Type != gjson.Number || value.Int() <= limit {
		return 0, false
	}
	return limit, true
}
