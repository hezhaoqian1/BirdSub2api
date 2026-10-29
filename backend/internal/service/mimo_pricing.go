package service

import "strings"

// mimoPricingModel resolves Xiaomi MiMo aliases to a fallback card key, longest family first.
func mimoPricingModel(model string) string {
	modelLower := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(modelLower, "mimo-v2.6-pro-ultraspeed") || strings.Contains(modelLower, "mimo-v2-6-pro-ultraspeed"):
		return "mimo-v2.6-pro-ultraspeed"
	case strings.Contains(modelLower, "mimo-v2.6-pro") || strings.Contains(modelLower, "mimo-v2-6-pro"):
		return "mimo-v2.6-pro"
	case strings.Contains(modelLower, "mimo-v2.6-flash") || strings.Contains(modelLower, "mimo-v2-6-flash"):
		return "mimo-v2.6-flash"
	}
	return ""
}

// mimoCNYCardModels lists every MiMo fallback key; all carry CNY site cards.
var mimoCNYCardModels = []string{"mimo-v2.6-pro-ultraspeed", "mimo-v2.6-pro", "mimo-v2.6-flash"}

// mimoDefaultPricing uses CNY list-price numbers as USD-denominated site credits.
// ¥1 buys $1 credit; these are not converted international USD prices.
// Source (2026-10-29, page updated 2026-10-21): https://mimo.mi.com/docs/zh-CN/price/pay-as-you-go
// 实时推理价；缓存写入官方限时免费，不单独计费。MiMo-V2.5 系列已于 2026-10-21 下线。
func mimoDefaultPricing(model string) *ModelPricing {
	switch mimoPricingModel(model) {
	case "mimo-v2.6-pro-ultraspeed":
		return &ModelPricing{InputPricePerToken: 30e-6, OutputPricePerToken: 60e-6, CacheReadPricePerToken: 0.25e-6}
	case "mimo-v2.6-pro":
		return &ModelPricing{InputPricePerToken: 3e-6, OutputPricePerToken: 6e-6, CacheReadPricePerToken: 0.025e-6}
	case "mimo-v2.6-flash":
		return &ModelPricing{InputPricePerToken: 1e-6, OutputPricePerToken: 2e-6, CacheReadPricePerToken: 0.02e-6}
	}
	return nil
}
