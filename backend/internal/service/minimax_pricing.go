package service

import "strings"

// minimaxPricingModel resolves MiniMax aliases to a fallback card key, longest family first.
func minimaxPricingModel(model string) string {
	modelLower := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(modelLower, "minimax-m3.1-flash") || strings.Contains(modelLower, "minimax-m3-1-flash"):
		return "minimax-m3.1-flash"
	case strings.Contains(modelLower, "minimax-m3"):
		return "minimax-m3"
	case strings.Contains(modelLower, "minimax-m2.7-highspeed") || strings.Contains(modelLower, "minimax-m2-7-highspeed"):
		return "minimax-m2.7-highspeed"
	case strings.Contains(modelLower, "minimax-m2.7") || strings.Contains(modelLower, "minimax-m2-7"):
		return "minimax-m2.7"
	case strings.Contains(modelLower, "minimax-m2.5-highspeed") || strings.Contains(modelLower, "minimax-m2-5-highspeed"):
		return "minimax-m2.5-highspeed"
	case strings.Contains(modelLower, "minimax-m2.5") || strings.Contains(modelLower, "minimax-m2-5"):
		return "minimax-m2.5"
	case strings.Contains(modelLower, "minimax-m2.1") || strings.Contains(modelLower, "minimax-m2-1"):
		return "minimax-m2.1"
	case strings.Contains(modelLower, "minimax-m2") || strings.Contains(modelLower, "minimax-m-2"):
		return "minimax-m2"
	}
	return ""
}

// minimaxCNYCardModels lists every MiniMax fallback key; all carry CNY site cards.
var minimaxCNYCardModels = []string{
	"minimax-m3.1-flash", "minimax-m3",
	"minimax-m2.7-highspeed", "minimax-m2.7",
	"minimax-m2.5-highspeed", "minimax-m2.5", "minimax-m2.1", "minimax-m2",
}

// minimaxDefaultPricing uses CNY list-price numbers as USD-denominated site credits.
// ¥1 buys $1 credit; these are not converted platform.minimax.io USD prices.
// Source (2026-10-29): https://platform.minimax.cn/docs/guides/pricing-paygo
// M2.x rows are no longer on that page; they follow the official CNY table
// mirrored at https://github.com/hkai-ai/LLM_OFFICIAL_DOCUMENTATION/blob/main/minimax/pricing.md
func minimaxDefaultPricing(model string) *ModelPricing {
	switch minimaxPricingModel(model) {
	case "minimax-m3.1-flash":
		return &ModelPricing{InputPricePerToken: 1e-6, OutputPricePerToken: 4e-6, CacheReadPricePerToken: 0.1e-6}
	// 永久五折后价格；>512K 输入整次按 2 倍计，priority 档为标准档 1.5 倍。
	// 官方未公布 M3 缓存写入价，保持不单独计费。
	case "minimax-m3":
		return &ModelPricing{
			InputPricePerToken:             2.1e-6,
			InputPricePerTokenPriority:     3.15e-6,
			OutputPricePerToken:            8.4e-6,
			OutputPricePerTokenPriority:    12.6e-6,
			CacheReadPricePerToken:         0.42e-6,
			CacheReadPricePerTokenPriority: 0.63e-6,
			LongContextInputThreshold:      512_000,
			LongContextInputMultiplier:     2,
			LongContextOutputMultiplier:    2,
		}
	// highspeed 为标准版 2 倍，缓存读写价与标准版相同。
	case "minimax-m2.7-highspeed":
		return &ModelPricing{InputPricePerToken: 4.2e-6, OutputPricePerToken: 16.8e-6, CacheReadPricePerToken: 0.42e-6, CacheCreationPricePerToken: 2.625e-6}
	case "minimax-m2.7":
		return &ModelPricing{InputPricePerToken: 2.1e-6, OutputPricePerToken: 8.4e-6, CacheReadPricePerToken: 0.42e-6, CacheCreationPricePerToken: 2.625e-6}
	case "minimax-m2.5-highspeed":
		return &ModelPricing{InputPricePerToken: 4.2e-6, OutputPricePerToken: 16.8e-6, CacheReadPricePerToken: 0.21e-6, CacheCreationPricePerToken: 2.625e-6}
	// M2.1 / M2 已不在官方价目页，国际站与 M2.5 同价，按 M2.5 人民币价计。
	case "minimax-m2.5", "minimax-m2.1", "minimax-m2":
		return &ModelPricing{InputPricePerToken: 2.1e-6, OutputPricePerToken: 8.4e-6, CacheReadPricePerToken: 0.21e-6, CacheCreationPricePerToken: 2.625e-6}
	}
	return nil
}
