package service

import "strings"

// qwenPricingModel resolves Qwen aliases (dated snapshots, vendor prefixes) to a fallback card key.
func qwenPricingModel(model string) string {
	modelLower := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(modelLower, "qwen3.8-max"):
		return "qwen3.8-max"
	case strings.Contains(modelLower, "qwen3.8-flash"):
		return "qwen3.8-flash"
	case strings.Contains(modelLower, "qwen3.7-max"):
		return "qwen3.7-max"
	case strings.Contains(modelLower, "qwen3.7-plus"):
		return "qwen3.7-plus"
	case strings.Contains(modelLower, "qwen3.6-plus"):
		return "qwen3.6-plus"
	}
	return ""
}

// qwenCNYCardModels lists every Qwen fallback key; all carry CNY site cards.
var qwenCNYCardModels = []string{
	"qwen3.8-max", "qwen3.8-flash", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-plus",
}

// qwenLongContextThreshold is the official "256K<Token≤1M" boundary of the Plus cards.
const qwenLongContextThreshold = 256_000

// qwenDefaultPricing uses CNY list-price numbers as USD-denominated site credits.
// ¥1 buys $1 credit; these are not converted Alibaba Cloud international USD prices.
// Source (2026-10-29, 中国内地·华北2（北京）): https://help.aliyun.com/zh/model-studio/model-pricing
// Cache rules: https://help.aliyun.com/zh/model-studio/context-cache — implicit hits bill 20% of
// input and explicit cache creation 125%; Qwen3.8 hit prices are model specific (both 12.5%).
// Multi-tier families (qwen3.5-*, qwen-plus, qwen-flash, coder/vl) are not covered by these cards.
func qwenDefaultPricing(model string) *ModelPricing {
	switch qwenPricingModel(model) {
	case "qwen3.8-max":
		return &ModelPricing{InputPricePerToken: 12e-6, OutputPricePerToken: 36e-6, CacheReadPricePerToken: 1.5e-6, CacheCreationPricePerToken: 15e-6}
	// 官方未公布 qwen3.8-flash 显式缓存创建价（"非 125%，各地域不同"），保持不单独计费。
	case "qwen3.8-flash":
		return &ModelPricing{InputPricePerToken: 0.8e-6, OutputPricePerToken: 2.7e-6, CacheReadPricePerToken: 0.1e-6}
	case "qwen3.7-max":
		return &ModelPricing{InputPricePerToken: 12e-6, OutputPricePerToken: 36e-6, CacheReadPricePerToken: 2.4e-6, CacheCreationPricePerToken: 15e-6}
	// 以下按输入长度分档：(0,256K] 为基础价，>256K 整次按长上下文价。
	// qwen3.7-plus 取页面原价（限时 8 折不计入）。
	case "qwen3.7-plus":
		return &ModelPricing{
			InputPricePerToken:          2e-6,
			OutputPricePerToken:         8e-6,
			CacheReadPricePerToken:      0.4e-6,
			CacheCreationPricePerToken:  2.5e-6,
			LongContextInputThreshold:   qwenLongContextThreshold,
			LongContextInputMultiplier:  3,
			LongContextOutputMultiplier: 3,
		}
	// 北京地域 qwen3.6-plus 只支持显式缓存，命中按 10% 计。
	case "qwen3.6-plus":
		return &ModelPricing{
			InputPricePerToken:          2e-6,
			OutputPricePerToken:         12e-6,
			CacheReadPricePerToken:      0.2e-6,
			CacheCreationPricePerToken:  2.5e-6,
			LongContextInputThreshold:   qwenLongContextThreshold,
			LongContextInputMultiplier:  4,
			LongContextOutputMultiplier: 4,
		}
	}
	return nil
}
