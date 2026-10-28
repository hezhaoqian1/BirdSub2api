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
	// K3.1 Pro 与 kimi-k3 同样只认精确 ID（及 / 路径后缀）；高速版须先于标准版判定。
	if kimiModelIDMatches(modelLower, "kimi-k3.1-pro-highspeed", "kimi-k3-1-pro-highspeed") {
		return "kimi-k3.1-pro-highspeed"
	}
	if kimiModelIDMatches(modelLower, "kimi-k3.1-pro", "kimi-k3-1-pro") {
		return "kimi-k3.1-pro"
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

// kimiModelIDMatches reports whether modelLower is one of ids, bare or as a path suffix.
func kimiModelIDMatches(modelLower string, ids ...string) bool {
	for _, id := range ids {
		if modelLower == id || strings.HasSuffix(modelLower, "/"+id) {
			return true
		}
	}
	return false
}

// kimiDefaultPricing uses CNY list-price numbers as USD-denominated site credits.
// ¥1 buys $1 credit; these are not converted international USD prices.
// Source: https://platform.kimi.com/docs/pricing/chat (2026-10-15; K3.1 Pro 2026-10-28).
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
	case "kimi-k3.1-pro":
		return &ModelPricing{
			InputPricePerToken:         13e-6,
			OutputPricePerToken:        68e-6,
			CacheReadPricePerToken:     1.3e-6,
			CacheCreationPricePerToken: 13e-6,
			CacheCreation5mPrice:       13e-6,
			CacheCreation1hPrice:       26e-6,
			SupportsCacheBreakdown:     true,
		}
	// 高速版官方缓存命中价与标准版相同（¥1.30），其余各项为标准版 2 倍。
	case "kimi-k3.1-pro-highspeed":
		return &ModelPricing{
			InputPricePerToken:         26e-6,
			OutputPricePerToken:        136e-6,
			CacheReadPricePerToken:     1.3e-6,
			CacheCreationPricePerToken: 26e-6,
			CacheCreation5mPrice:       26e-6,
			CacheCreation1hPrice:       52e-6,
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
