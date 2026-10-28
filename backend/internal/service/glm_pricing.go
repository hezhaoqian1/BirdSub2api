package service

import "strings"

// glmPricingModel resolves GLM aliases to a fallback card key, longest family first.
// Dotted versions must win over bare "glm-5", and flashx/flash/prime over "glm-5.3".
func glmPricingModel(model string) string {
	modelLower := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(modelLower, "glm-5.3-flashx"):
		return "glm-5.3-flashx"
	case strings.Contains(modelLower, "glm-5.3-flash") || strings.Contains(modelLower, "glm-5.3flash"):
		return "glm-5.3-flash"
	case strings.Contains(modelLower, "glm-5.3-prime") || strings.Contains(modelLower, "glm-5.3prime"):
		return "glm-5.3-prime"
	case strings.Contains(modelLower, "glm-5.3"):
		return "glm-5.3"
	case strings.Contains(modelLower, "glm-5.2"):
		return "glm-5.2"
	case strings.Contains(modelLower, "glm-5.1"):
		return "glm-5.1"
	case strings.Contains(modelLower, "glm-5-turbo") || strings.Contains(modelLower, "glm-5turbo"):
		return "glm-5-turbo"
	case strings.Contains(modelLower, "glm-5"):
		return "glm-5"
	case strings.Contains(modelLower, "glm-4.7-flashx"):
		return "glm-4.7-flashx"
	case strings.Contains(modelLower, "glm-4.7-flash"):
		return "glm-4.7-flash"
	case strings.Contains(modelLower, "glm-4.7"):
		return "glm-4.7"
	case strings.Contains(modelLower, "glm-4.6"):
		return "glm-4.6"
	case strings.Contains(modelLower, "glm-4.5-flash"):
		return "glm-4.5-flash"
	case strings.Contains(modelLower, "glm-4.5-x") || strings.Contains(modelLower, "glm-4.5x"):
		return "glm-4.5-x"
	case strings.Contains(modelLower, "glm-4.5-airx") || strings.Contains(modelLower, "glm-4.5airx"):
		return "glm-4.5-airx"
	case strings.Contains(modelLower, "glm-4.5-air") || strings.Contains(modelLower, "glm-4.5air"):
		return "glm-4.5-air"
	case strings.Contains(modelLower, "glm-4.5"):
		return "glm-4.5"
	case strings.Contains(modelLower, "glm-4-32b"):
		return "glm-4-32b-0414-128k"
	}
	return ""
}

// glmCNYCardModels lists the GLM keys that carry a CNY site card.
// glm-4.5-x / glm-4.5-airx / glm-4-32b-0414-128k have no official CNY price
// any more and keep their legacy fallback cards in initFallbackPricing.
var glmCNYCardModels = []string{
	"glm-5.3", "glm-5.3-flashx", "glm-5.3-flash", "glm-5.3-prime", "glm-5.2",
	"glm-5.1", "glm-5-turbo", "glm-5", "glm-4.7", "glm-4.7-flashx", "glm-4.7-flash",
	"glm-4.6", "glm-4.5", "glm-4.5-air", "glm-4.5-flash",
}

// glmLongContextThreshold is the official "输入 ≥32K" boundary of the tiered GLM cards.
const glmLongContextThreshold = 32_000

// glmTieredPricing builds a card whose ≥32K-input tier is expressed as multipliers.
// The cost path multiplies cache reads by the input multiplier.
func glmTieredPricing(input, output, cacheRead, longInput, longOutput float64) *ModelPricing {
	return &ModelPricing{
		InputPricePerToken:            input * 1e-6,
		OutputPricePerToken:           output * 1e-6,
		CacheReadPricePerToken:        cacheRead * 1e-6,
		LongContextInputThreshold:     glmLongContextThreshold,
		LongContextThresholdInclusive: true,
		LongContextInputMultiplier:    longInput / input,
		LongContextOutputMultiplier:   longOutput / output,
	}
}

// glmDefaultPricing uses CNY list-price numbers as USD-denominated site credits.
// ¥1 buys $1 credit; these are not converted z.ai international USD prices.
// Source (2026-10-29): https://docs.bigmodel.cn/cn/guide/start/pricing
// glm-5.3-prime: https://help.aliyun.com/zh/model-studio/model-pricing (华北2).
func glmDefaultPricing(model string) *ModelPricing {
	switch glmPricingModel(model) {
	case "glm-5.3", "glm-5.2":
		return &ModelPricing{InputPricePerToken: 8e-6, OutputPricePerToken: 28e-6, CacheReadPricePerToken: 2e-6}
	// FlashX-Legal 与 FlashX 同价，经子串匹配归入同一档。
	case "glm-5.3-flashx":
		return &ModelPricing{InputPricePerToken: 2e-6, OutputPricePerToken: 7e-6, CacheReadPricePerToken: 0.57e-6}
	case "glm-5.3-flash":
		return &ModelPricing{InputPricePerToken: 0.8e-6, OutputPricePerToken: 2.8e-6, CacheReadPricePerToken: 0.23e-6}
	// 百炼只公布 ¥16/¥56，缓存命中按 Prime 全项为标准版 2 倍的口径取 ¥4。
	case "glm-5.3-prime":
		return &ModelPricing{InputPricePerToken: 16e-6, OutputPricePerToken: 56e-6, CacheReadPricePerToken: 4e-6}
	// 以下按输入长度分档：[0,32K) 为基础价，≥32K 为长上下文价。
	// 官方 ≥32K 缓存命中价（5.1 为 ¥2、5-Turbo 为 ¥1.8）略高于按输入倍率推算值，此处按倍率计。
	case "glm-5.1":
		return glmTieredPricing(6, 24, 1.3, 8, 28)
	case "glm-5-turbo":
		return glmTieredPricing(5, 22, 1.2, 7, 26)
	case "glm-5":
		return glmTieredPricing(4, 18, 1, 6, 22)
	// GLM-4.7 / GLM-4.5-Air 的 [0,32K) 档还按输出长度细分，取输出 ≥0.2K 的常见档。
	// GLM-4.6 / GLM-4.5 已不在官方价目页，按官方建议的继任 GLM-4.7 计价（国际站三者同价）。
	case "glm-4.7", "glm-4.6", "glm-4.5":
		return glmTieredPricing(3, 14, 0.6, 4, 16)
	case "glm-4.5-air":
		return glmTieredPricing(0.8, 6, 0.16, 1.2, 8)
	case "glm-4.7-flashx":
		return &ModelPricing{InputPricePerToken: 0.5e-6, OutputPricePerToken: 3e-6, CacheReadPricePerToken: 0.1e-6}
	// 官方免费；保留零价条目，避免未知 alias 误计费。
	case "glm-4.7-flash", "glm-4.5-flash":
		return &ModelPricing{}
	}
	return nil
}
