//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGLMMiniMaxCreditPricingOverridesRemoteUSD(t *testing.T) {
	ps := newStubPricingServiceFromJSON(t, `{
		"glm-5.3":{"input_cost_per_token":0.0000014,"output_cost_per_token":0.0000044,"cache_read_input_token_cost":0.00000026},
		"minimax-m3":{"input_cost_per_token":0.0000006,"output_cost_per_token":0.0000024,"cache_read_input_token_cost":0.00000012},
		"glm-4.5-x":{"input_cost_per_token":0.0000022,"output_cost_per_token":0.0000089}
	}`)
	for _, catalog := range []*PricingService{nil, ps} {
		bs := NewBillingService(&config.Config{}, catalog)
		for _, tc := range []struct {
			model                string
			input, output, cache float64
		}{
			{"glm-5.3", 8, 28, 2},
			{"zai/GLM-5.3", 8, 28, 2},
			{"glm-5.3-flashx", 2, 7, 0.57},
			{"glm-5.3-flashx-legal", 2, 7, 0.57},
			{"glm-5.3-flash", 0.8, 2.8, 0.23},
			{"glm-5.3-prime", 16, 56, 4},
			{"glm-5.2", 8, 28, 2},
			{"glm-5.2-fast-preview", 8, 28, 2},
			{"glm-5.1", 6, 24, 1.3},
			{"glm-5-turbo", 5, 22, 1.2},
			{"glm-5", 4, 18, 1},
			{"glm-4.7", 3, 14, 0.6},
			{"glm-4.6", 3, 14, 0.6},
			{"glm-4.5", 3, 14, 0.6},
			{"glm-4.5-air", 0.8, 6, 0.16},
			{"glm-4.7-flashx", 0.5, 3, 0.1},
			{"glm-4.7-flash", 0, 0, 0},
			{"glm-4.5-flash", 0, 0, 0},
			{"minimax-m3.1-flash", 1, 4, 0.1},
			{"MiniMax-M3", 2.1, 8.4, 0.42},
			{"minimax/minimax-m3", 2.1, 8.4, 0.42},
			{"minimax-m2.7", 2.1, 8.4, 0.42},
			{"minimax-m2.7-highspeed", 4.2, 16.8, 0.42},
			{"minimax-m2.5", 2.1, 8.4, 0.21},
			{"minimax-m2.5-highspeed", 4.2, 16.8, 0.21},
			{"minimax-m2.1", 2.1, 8.4, 0.21},
			{"minimax-m2", 2.1, 8.4, 0.21},
		} {
			t.Run(tc.model, func(t *testing.T) {
				// 10K input + 10K cache stays below every long-context tier; costs are 1% of the MTok rate.
				cost, err := bs.CalculateCost(tc.model, UsageTokens{
					InputTokens: 10_000, OutputTokens: 10_000, CacheReadTokens: 10_000,
				}, 1.5)
				require.NoError(t, err)
				require.InDelta(t, tc.input/100, cost.InputCost, 1e-9)
				require.InDelta(t, tc.output/100, cost.OutputCost, 1e-9)
				require.InDelta(t, tc.cache/100, cost.CacheReadCost, 1e-9)
				require.InDelta(t, (tc.input+tc.output+tc.cache)/100*1.5, cost.ActualCost, 1e-9)
				if catalog != nil {
					// Direct PricingService consumers (available-channel display) agree with billing.
					p := catalog.GetModelPricing(tc.model)
					require.InDelta(t, tc.input, p.InputCostPerToken*1e6, 1e-9)
					require.InDelta(t, tc.output, p.OutputCostPerToken*1e6, 1e-9)
					require.InDelta(t, tc.cache, p.CacheReadInputTokenCost*1e6, 1e-9)
				}
			})
		}
	}
	// Catalog objects are never rewritten in-place by the site pricing policy.
	require.Equal(t, 1.4e-6, ps.pricingData["glm-5.3"].InputCostPerToken)
}

func TestGLMLegacyModelsWithoutCNYPriceKeepFallbackCards(t *testing.T) {
	bs := newTestBillingService()
	for _, tc := range []struct {
		model         string
		input, output float64
	}{
		{"glm-4.5-x", 2.2, 8.9},
		{"glm-4.5-airx", 1.1, 4.5},
		{"glm-4-32b-0414-128k", 0.1, 0.1},
	} {
		require.Nil(t, glmDefaultPricing(tc.model), tc.model)
		cost, err := bs.CalculateCost(tc.model, UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 1)
		require.NoError(t, err, tc.model)
		require.InDelta(t, tc.input, cost.InputCost, 1e-9, tc.model)
		require.InDelta(t, tc.output, cost.OutputCost, 1e-9, tc.model)
	}
}

func TestGLMInputLengthTiers(t *testing.T) {
	bs := newTestBillingService()
	for _, tc := range []struct {
		model                string
		base, long           [2]float64 // input, output CNY per MTok
		baseCache, longCache float64
	}{
		{"glm-5.1", [2]float64{6, 24}, [2]float64{8, 28}, 1.3, 1.3 * 8 / 6},
		{"glm-5-turbo", [2]float64{5, 22}, [2]float64{7, 26}, 1.2, 1.2 * 7 / 5},
		{"glm-5", [2]float64{4, 18}, [2]float64{6, 22}, 1, 1.5},
		{"glm-4.7", [2]float64{3, 14}, [2]float64{4, 16}, 0.6, 0.8},
		{"glm-4.5-air", [2]float64{0.8, 6}, [2]float64{1.2, 8}, 0.16, 0.24},
	} {
		// Prompt length counts input + cache read; 31,999 stays on the base tier, 32,000 is "≥32K".
		below, err := bs.CalculateCost(tc.model, UsageTokens{InputTokens: 21_999, CacheReadTokens: 10_000, OutputTokens: 1_000_000}, 1)
		require.NoError(t, err, tc.model)
		require.InDelta(t, 21_999*tc.base[0]/1e6, below.InputCost, 1e-9, tc.model)
		require.InDelta(t, tc.base[1], below.OutputCost, 1e-9, tc.model)
		require.InDelta(t, 10_000*tc.baseCache/1e6, below.CacheReadCost, 1e-9, tc.model)

		at, err := bs.CalculateCost(tc.model, UsageTokens{InputTokens: 22_000, CacheReadTokens: 10_000, OutputTokens: 1_000_000}, 1)
		require.NoError(t, err, tc.model)
		require.InDelta(t, 22_000*tc.long[0]/1e6, at.InputCost, 1e-9, tc.model)
		require.InDelta(t, tc.long[1], at.OutputCost, 1e-9, tc.model)
		require.InDelta(t, 10_000*tc.longCache/1e6, at.CacheReadCost, 1e-9, tc.model)
	}
	// Flat-priced cards do not tier.
	flat, err := bs.CalculateCost("glm-5.3", UsageTokens{InputTokens: 200_000, OutputTokens: 1_000_000}, 1)
	require.NoError(t, err)
	require.InDelta(t, 200_000*8/1e6, flat.InputCost, 1e-9)
	require.InDelta(t, 28, flat.OutputCost, 1e-9)
}

func TestMiniMaxM3LongContextAndPriority(t *testing.T) {
	bs := newTestBillingService()
	for _, tc := range []struct {
		tier                 string
		input, output, cache float64 // ≤512K CNY per MTok
	}{
		{"", 2.1, 8.4, 0.42},
		{"priority", 3.15, 12.6, 0.63},
	} {
		short, err := bs.CalculateCostWithServiceTier("minimax-m3", UsageTokens{
			InputTokens: 500_000, CacheReadTokens: 12_000, OutputTokens: 1_000_000,
		}, 1, tc.tier)
		require.NoError(t, err, tc.tier)
		require.InDelta(t, 0.5*tc.input, short.InputCost, 1e-9, tc.tier)
		require.InDelta(t, tc.output, short.OutputCost, 1e-9, tc.tier)
		require.InDelta(t, 0.012*tc.cache, short.CacheReadCost, 1e-9, tc.tier)

		long, err := bs.CalculateCostWithServiceTier("minimax-m3", UsageTokens{
			InputTokens: 500_000, CacheReadTokens: 12_001, OutputTokens: 1_000_000,
		}, 1, tc.tier)
		require.NoError(t, err, tc.tier)
		require.InDelta(t, 0.5*tc.input*2, long.InputCost, 1e-9, tc.tier)
		require.InDelta(t, tc.output*2, long.OutputCost, 1e-9, tc.tier)
		require.InDelta(t, 0.012001*tc.cache*2, long.CacheReadCost, 1e-9, tc.tier)
	}
}

func TestMiniMaxM2CacheWritePrice(t *testing.T) {
	bs := newTestBillingService()
	for _, model := range []string{"minimax-m2.7", "minimax-m2.7-highspeed", "minimax-m2.5", "minimax-m2.5-highspeed", "minimax-m2"} {
		cost, err := bs.CalculateCost(model, UsageTokens{CacheCreationTokens: 1_000_000}, 1)
		require.NoError(t, err, model)
		require.InDelta(t, 2.625, cost.CacheCreationCost, 1e-9, model)
	}
}

func TestGLMMiniMaxCustomPricingRemainsAuthoritative(t *testing.T) {
	bs := newTestBillingService()
	for _, tc := range []struct {
		model    string
		platform string
		cache    float64 // inherited from the CNY default card
	}{
		{"glm-5.3", PlatformZhipu, 2},
		{"minimax-m3", PlatformMiniMax, 0.42},
	} {
		input, output := 1e-6, 2e-6
		card := ChannelModelPricing{Models: []string{tc.model}, BillingMode: BillingModeToken,
			InputPrice: &input, OutputPrice: &output}
		channel, err := bs.GetModelPricingWithChannel(tc.model, &card)
		require.NoError(t, err, tc.model)
		require.Equal(t, input, channel.InputPricePerToken, tc.model)
		require.Equal(t, output, channel.OutputPricePerToken, tc.model)
		group := &Group{ID: 1, Platform: tc.platform, ModelPricing: []ChannelModelPricing{card}}
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: tc.model, Group: group,
			Tokens:         UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000},
			RateMultiplier: 1, Resolver: NewModelPricingResolver(nil, bs),
		})
		require.NoError(t, err, tc.model)
		require.InDelta(t, 1+2+tc.cache, cost.ActualCost, 1e-9, tc.model)
	}
}

func TestGLMLongContextTierFollowsGroupSwitch(t *testing.T) {
	bs := newTestBillingService()
	input, output := 1e-6, 2e-6
	card := ChannelModelPricing{Models: []string{"glm-5.1"}, BillingMode: BillingModeToken,
		InputPrice: &input, OutputPrice: &output}
	for _, tc := range []struct {
		enabled         bool
		inMult, outMult float64
	}{
		{true, 8.0 / 6.0, 28.0 / 24.0},
		{false, 1, 1},
	} {
		group := &Group{ID: 1, Platform: PlatformZhipu, LongContextPricingEnabled: tc.enabled,
			ModelPricing: []ChannelModelPricing{card}}
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "glm-5.1", Group: group,
			Tokens:         UsageTokens{InputTokens: 40_000, OutputTokens: 1_000_000},
			RateMultiplier: 1, Resolver: NewModelPricingResolver(nil, bs),
		})
		require.NoError(t, err)
		// Custom prices stay authoritative; the official ≥32K ladder scales them only when enabled.
		require.InDelta(t, 0.04*tc.inMult, cost.InputCost, 1e-9, tc.enabled)
		require.InDelta(t, 2*tc.outMult, cost.OutputCost, 1e-9, tc.enabled)
	}
}
