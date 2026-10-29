package service

import "strings"

// hunyuanPricingModel resolves Tencent Hunyuan aliases to a fallback card key.
func hunyuanPricingModel(model string) string {
	if strings.Contains(strings.ToLower(strings.TrimSpace(model)), "hy4-preview") {
		return "hy4-preview"
	}
	return ""
}

// hunyuanDefaultPricing uses CNY list-price numbers as USD-denominated site credits.
// ¥1 buys $1 credit; these are not converted OpenRouter USD prices.
// Source (腾讯云 TokenHub, page updated 2026-08-28): Hy4 preview ¥6 / ¥18 / cache hit ¥0.3 per MTok.
func hunyuanDefaultPricing(model string) *ModelPricing {
	if hunyuanPricingModel(model) == "hy4-preview" {
		return &ModelPricing{InputPricePerToken: 6e-6, OutputPricePerToken: 18e-6, CacheReadPricePerToken: 0.3e-6}
	}
	return nil
}
