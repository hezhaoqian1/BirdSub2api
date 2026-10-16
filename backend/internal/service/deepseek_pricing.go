package service

import "strings"

// deepseekDefaultPricing returns off-peak CNY numeric rates in USD credits.
// Explicit snapshots use the Bailian price card, including its 20% cache rate:
// https://help.aliyun.com/zh/model-studio/model-pricing
// https://help.aliyun.com/zh/model-studio/context-cache (checked 2026-10-16).
// Unversioned aliases retain the DeepSeek direct API card: those names also
// route to official accounts and cannot identify Bailian's legacy Pro model.
// Existing peak windows and custom group/channel pricing are handled separately.
func deepseekDefaultPricing(model string) *ModelPricing {
	if !isDeepSeekModel(model) {
		return nil
	}
	model = strings.ToLower(strings.TrimSpace(model))
	card := &ModelPricing{
		InputPricePerToken:     deepseekFlashOffPeakInputPrice,
		OutputPricePerToken:    deepseekFlashOffPeakOutputPrice,
		CacheReadPricePerToken: deepseekFlashOffPeakCacheRead,
	}
	switch {
	case model == "deepseek-v4-flash-0731" || strings.HasPrefix(model, "deepseek-v4-flash-0731-"):
		card.InputPricePerToken = 1.5e-6
		card.OutputPricePerToken = 4.5e-6
		card.CacheReadPricePerToken = 0.3e-6
	case model == "deepseek-v4-pro-0813" || strings.HasPrefix(model, "deepseek-v4-pro-0813-"):
		card.InputPricePerToken = 4.5e-6
		card.OutputPricePerToken = 13.5e-6
		card.CacheReadPricePerToken = 0.9e-6
	case isDeepSeekProModel(model):
		card.InputPricePerToken = deepseekProOffPeakInputPrice
		card.OutputPricePerToken = deepseekProOffPeakOutputPrice
		card.CacheReadPricePerToken = deepseekProOffPeakCacheRead
	}
	return card
}
