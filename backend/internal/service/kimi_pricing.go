package service

import "strings"

// kimiPricingModel shares Kimi alias resolution between fallback and default-card policy.
func kimiPricingModel(model string) string {
	modelLower := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(modelLower, "kimi-for-coding") {
		return "kimi-for-coding"
	}
	if modelLower == "kimi-k3" || strings.HasSuffix(modelLower, "/kimi-k3") ||
		modelLower == "k3" || modelLower == "k3-256k" ||
		strings.HasSuffix(modelLower, "/k3") || strings.HasSuffix(modelLower, "/k3-256k") {
		return "kimi-k3"
	}
	if strings.Contains(modelLower, "kimi-k2.7-code-highspeed") {
		return "kimi-k2.7-code-highspeed"
	}
	if strings.Contains(modelLower, "kimi-k2.7-code") {
		return "kimi-k2.7-code"
	}
	if strings.Contains(modelLower, "kimi-k2.6") || strings.Contains(modelLower, "kimi-k2-6") {
		return "kimi-k2.6"
	}
	if strings.Contains(modelLower, "kimi-k2.5") || strings.Contains(modelLower, "kimi-k2-5") {
		return "kimi-k2.5"
	}
	if strings.Contains(modelLower, "kimi-k2-thinking") || strings.Contains(modelLower, "kimi-k2-thinking-") {
		return "kimi-k2-thinking"
	}
	if strings.Contains(modelLower, "kimi-k2") || strings.Contains(modelLower, "kimi/k2") {
		return "kimi-k2"
	}

	return ""
}

// kimiDefaultPricing uses CNY list-price numbers as USD-denominated site credits.
// ¥1 buys $1 credit; these are not converted international USD prices.
// Source: https://platform.kimi.com/docs/pricing/chat (2026-10-15).
func kimiDefaultPricing(model string) *ModelPricing {
	switch kimiPricingModel(model) {
	case "kimi-k3":
		return &ModelPricing{
			InputPricePerToken:         20e-6,
			OutputPricePerToken:        100e-6,
			CacheReadPricePerToken:     2e-6,
			CacheCreationPricePerToken: 20e-6,
			CacheCreation5mPrice:       20e-6,
			CacheCreation1hPrice:       40e-6,
			SupportsCacheBreakdown:     true,
		}
	case "kimi-k2.7-code":
		return &ModelPricing{
			InputPricePerToken:     6.5e-6,
			OutputPricePerToken:    27e-6,
			CacheReadPricePerToken: 1.3e-6,
		}
	case "kimi-k2.7-code-highspeed":
		return &ModelPricing{
			InputPricePerToken:     13e-6,
			OutputPricePerToken:    54e-6,
			CacheReadPricePerToken: 2.6e-6,
		}
	case "kimi-k2.6":
		return &ModelPricing{
			InputPricePerToken:     6.5e-6,
			OutputPricePerToken:    27e-6,
			CacheReadPricePerToken: 1.1e-6,
			SupportsCacheBreakdown: false,
		}
	// kimi-for-coding 走 Kimi Coding endpoint，按当前 K2.6 coding 档位兜底计费。
	case "kimi-for-coding":
		return &ModelPricing{
			InputPricePerToken:     6.5e-6,
			OutputPricePerToken:    27e-6,
			CacheReadPricePerToken: 1.1e-6,
			SupportsCacheBreakdown: false,
		}
	case "kimi-k2.5":
		return &ModelPricing{
			InputPricePerToken:     4e-6,
			OutputPricePerToken:    21e-6,
			CacheReadPricePerToken: 0.7e-6,
			SupportsCacheBreakdown: false,
		}
	case "kimi-k2-thinking":
		return &ModelPricing{
			InputPricePerToken:     4e-6,
			OutputPricePerToken:    16e-6,
			CacheReadPricePerToken: 1e-6,
			SupportsCacheBreakdown: false,
		}
	case "kimi-k2":
		return &ModelPricing{
			InputPricePerToken:     4e-6,
			OutputPricePerToken:    16e-6,
			CacheReadPricePerToken: 1e-6,
			SupportsCacheBreakdown: false,
		}

	}
	return nil
}
