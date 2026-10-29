//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestQwenMiMoHunyuanCreditPricingOverridesRemoteUSD(t *testing.T) {
	ps := newStubPricingServiceFromJSON(t, `{
		"qwen3.8-max":{"input_cost_per_token":0.00000165,"output_cost_per_token":0.00000495},
		"xiaomi/mimo-v2.6-pro":{"input_cost_per_token":0.000000435,"output_cost_per_token":0.00000087},
		"tencent/hy4-preview":{"input_cost_per_token":0.000000834,"output_cost_per_token":0.000002501,"cache_read_input_token_cost":0.000000042}
	}`)
	for _, catalog := range []*PricingService{nil, ps} {
		bs := NewBillingService(&config.Config{}, catalog)
		for _, tc := range []struct {
			model                string
			input, output, cache float64
		}{
			{"qwen3.8-max", 12, 36, 1.5},
			{"qwen3.8-max-0902", 12, 36, 1.5},
			{"dashscope/Qwen3.8-Max", 12, 36, 1.5},
			{"qwen3.8-flash", 0.8, 2.7, 0.1},
			{"qwen3.7-max", 12, 36, 2.4},
			{"qwen3.7-max-2026-06-08", 12, 36, 2.4},
			{"qwen3.7-plus", 2, 8, 0.4},
			{"qwen3.6-plus-2026-04-02", 2, 12, 0.2},
			{"mimo-v2.6-pro", 3, 6, 0.025},
			{"xiaomi/mimo-v2.6-pro", 3, 6, 0.025},
			{"mimo-v2.6-pro-ultraspeed", 30, 60, 0.25},
			{"mimo-v2.6-flash", 1, 2, 0.02},
			{"hy4-preview", 6, 18, 0.3},
			{"Hy4-preview", 6, 18, 0.3},
			{"tencent/hy4-preview", 6, 18, 0.3},
		} {
			t.Run(tc.model, func(t *testing.T) {
				cost, err := bs.CalculateCost(tc.model, UsageTokens{
					InputTokens: 10_000, OutputTokens: 10_000, CacheReadTokens: 10_000,
				}, 1.5)
				require.NoError(t, err)
				require.InDelta(t, tc.input/100, cost.InputCost, 1e-9)
				require.InDelta(t, tc.output/100, cost.OutputCost, 1e-9)
				require.InDelta(t, tc.cache/100, cost.CacheReadCost, 1e-9)
				require.InDelta(t, (tc.input+tc.output+tc.cache)/100*1.5, cost.ActualCost, 1e-9)
				if catalog != nil {
					p := catalog.GetModelPricing(tc.model)
					require.InDelta(t, tc.input, p.InputCostPerToken*1e6, 1e-9)
					require.InDelta(t, tc.output, p.OutputCostPerToken*1e6, 1e-9)
					require.InDelta(t, tc.cache, p.CacheReadInputTokenCost*1e6, 1e-9)
				}
			})
		}
	}
	require.Equal(t, 1.65e-6, ps.pricingData["qwen3.8-max"].InputCostPerToken)
}

func TestQwenUncoveredFamiliesStayUnpriced(t *testing.T) {
	bs := newTestBillingService()
	for _, model := range []string{"qwen3.5-plus", "qwen-plus", "qwen-flash", "qwen3.8-omni-flash", "mimo-v2.5-pro", "hy3"} {
		require.Empty(t, qwenPricingModel(model)+mimoPricingModel(model)+hunyuanPricingModel(model), model)
		_, err := bs.CalculateCost(model, UsageTokens{InputTokens: 1_000}, 1)
		require.Error(t, err, model)
	}
}

func TestQwenPlusLongContextTiers(t *testing.T) {
	bs := newTestBillingService()
	for _, tc := range []struct {
		model                       string
		input, output, cache, write float64 // ≤256K CNY per MTok
		mult                        float64
	}{
		{"qwen3.7-plus", 2, 8, 0.4, 2.5, 3},
		{"qwen3.6-plus", 2, 12, 0.2, 2.5, 4},
	} {
		// Prompt length counts input + cache create + cache read; exactly 256K stays on the base tier.
		at, err := bs.CalculateCost(tc.model, UsageTokens{
			InputTokens: 200_000, CacheCreationTokens: 6_000, CacheReadTokens: 50_000, OutputTokens: 1_000_000,
		}, 1)
		require.NoError(t, err, tc.model)
		require.InDelta(t, 0.2*tc.input, at.InputCost, 1e-9, tc.model)
		require.InDelta(t, tc.output, at.OutputCost, 1e-9, tc.model)
		require.InDelta(t, 0.05*tc.cache, at.CacheReadCost, 1e-9, tc.model)
		require.InDelta(t, 0.006*tc.write, at.CacheCreationCost, 1e-9, tc.model)

		above, err := bs.CalculateCost(tc.model, UsageTokens{
			InputTokens: 200_001, CacheCreationTokens: 6_000, CacheReadTokens: 50_000, OutputTokens: 1_000_000,
		}, 1)
		require.NoError(t, err, tc.model)
		require.InDelta(t, 0.200001*tc.input*tc.mult, above.InputCost, 1e-9, tc.model)
		require.InDelta(t, tc.output*tc.mult, above.OutputCost, 1e-9, tc.model)
		require.InDelta(t, 0.05*tc.cache*tc.mult, above.CacheReadCost, 1e-9, tc.model)
		require.InDelta(t, 0.006*tc.write*tc.mult, above.CacheCreationCost, 1e-9, tc.model)
	}
	// Flat cards do not tier.
	flat, err := bs.CalculateCost("qwen3.8-max", UsageTokens{InputTokens: 900_000, OutputTokens: 1_000_000}, 1)
	require.NoError(t, err)
	require.InDelta(t, 0.9*12, flat.InputCost, 1e-9)
	require.InDelta(t, 36, flat.OutputCost, 1e-9)
}

// Group 72「国产模型 自部署」is an openai-platform group with no custom prices:
// every model its account maps must resolve to a CNY site card.
func TestDomesticModelsOnOpenAIGroupUseSiteCards(t *testing.T) {
	bs := newTestBillingService()
	group := &Group{ID: 72, Platform: PlatformOpenAI, LongContextPricingEnabled: true}
	for _, tc := range []struct {
		model         string
		input, output float64
	}{
		{"qwen3.8-flash", 0.8, 2.7},
		{"qwen3.8-max", 12, 36},
		{"glm-5.3", 8, 28},
		{"glm-5.3-flash", 0.8, 2.8},
		{"hy4-preview", 6, 18},
		{"mimo-v2.6-flash", 1, 2},
		{"mimo-v2.6-pro", 3, 6},
		{"minimax-m3", 2.1, 8.4},
	} {
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: tc.model, Group: group,
			Tokens:         UsageTokens{InputTokens: 10_000, OutputTokens: 10_000},
			RateMultiplier: 0.4, Resolver: NewModelPricingResolver(nil, bs),
		})
		require.NoError(t, err, tc.model)
		require.InDelta(t, (tc.input+tc.output)/100, cost.TotalCost, 1e-9, tc.model)
		require.InDelta(t, (tc.input+tc.output)/100*0.4, cost.ActualCost, 1e-9, tc.model)
	}
}

func TestHunyuanGroupCustomPricingRemainsAuthoritative(t *testing.T) {
	bs := newTestBillingService()
	input, output := 1e-6, 2e-6
	card := ChannelModelPricing{Models: []string{"Hy4-preview"}, BillingMode: BillingModeToken,
		InputPrice: &input, OutputPrice: &output}
	group := &Group{ID: 57, Platform: PlatformOpenAI, ModelPricing: []ChannelModelPricing{card}}
	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "hy4-preview", Group: group,
		Tokens:         UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000},
		RateMultiplier: 1, Resolver: NewModelPricingResolver(nil, bs),
	})
	require.NoError(t, err)
	// Unset cache price inherits ¥0.3 from the default card.
	require.InDelta(t, 1+2+0.3, cost.ActualCost, 1e-9)
}
