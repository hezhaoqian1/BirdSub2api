package service

import "strings"

// hunyuanPricingModel resolves Tencent Hunyuan aliases to a fallback card key.
//
//   - "hy4-preview": any name containing hy4-preview (incl. tencent/hy4-preview).
//   - "hy4": the Hy4 GA release (2026-11-04). Official TokenHub ID is "hy4"
//     (OpenRouter "tencent/hy4"); Aizzz exposes the same model as "hy4std".
//     Matched exactly (optionally behind a "provider/" prefix) so that other
//     Hy4 variants such as Hy4 flash, which has its own cheaper price, are not
//     billed at the standard card.
func hunyuanPricingModel(model string) string {
	modelLower := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(modelLower, "hy4-preview") {
		return "hy4-preview"
	}
	if i := strings.LastIndex(modelLower, "/"); i >= 0 {
		modelLower = modelLower[i+1:]
	}
	switch modelLower {
	case "hy4", "hy4std":
		return "hy4"
	}
	return ""
}

// hunyuanDefaultPricing uses CNY list-price numbers as USD-denominated site credits.
// ¥1 buys $1 credit; these are not converted OpenRouter USD prices.
// Source (腾讯云 TokenHub): Hy4 preview ¥6 / ¥18 / cache hit ¥0.3 per MTok (page updated
// 2026-08-28); Hy4 GA (released 2026-11-04) keeps the preview list price.
func hunyuanDefaultPricing(model string) *ModelPricing {
	switch hunyuanPricingModel(model) {
	case "hy4-preview", "hy4":
		return &ModelPricing{InputPricePerToken: 6e-6, OutputPricePerToken: 18e-6, CacheReadPricePerToken: 0.3e-6}
	}
	return nil
}
