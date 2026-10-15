//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestKimiCreditPricingOverridesRemoteUSD(t *testing.T) {
	ps := newStubPricingServiceFromJSON(t, `{
		"kimi-k3":{"input_cost_per_token":0.000003,"output_cost_per_token":0.000015,"cache_read_input_token_cost":0.0000003},
		"kimi-k2.6":{"input_cost_per_token":0.00000095,"output_cost_per_token":0.000004},
		"other-model":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}
	}`)
	for _, catalog := range []*PricingService{nil, ps} {
		bs := NewBillingService(&config.Config{}, catalog)
		for _, tc := range []struct {
			model                string
			input, output, cache float64
		}{
			{"kimi-k3", 20, 100, 2},
			{"k3", 20, 100, 2},
			{"kimi-code/k3-256k", 20, 100, 2},
			{"moonshot/kimi-k3", 20, 100, 2},
			{"kimi-k2.7-code", 6.5, 27, 1.3},
			{"kimi-k2.7-code-highspeed", 13, 54, 2.6},
			{"kimi-k2.6", 6.5, 27, 1.1},
			{"KIMI-K2-6", 6.5, 27, 1.1},
			{"kimi-for-coding", 6.5, 27, 1.1},
			{"kimi-k2.5", 4, 21, 0.7},
			{"kimi-k2-thinking", 4, 16, 1},
			{"kimi-k2", 4, 16, 1},
		} {
			t.Run(tc.model, func(t *testing.T) {
				cost, err := bs.CalculateCost(tc.model, UsageTokens{
					InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000,
				}, 1.5)
				require.NoError(t, err)
				require.InDelta(t, tc.input, cost.InputCost, 1e-9)
				require.InDelta(t, tc.output, cost.OutputCost, 1e-9)
				require.InDelta(t, tc.cache, cost.CacheReadCost, 1e-9)
				require.InDelta(t, (tc.input+tc.output+tc.cache)*1.5, cost.ActualCost, 1e-9)
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
	require.Equal(t, 1e-6, ps.GetModelPricing("other-model").InputCostPerToken)
	// Catalog objects are never rewritten in-place by the site pricing policy.
	require.Equal(t, 3e-6, ps.pricingData["kimi-k3"].InputCostPerToken)
}

func TestKimiK3CacheWriteTTL(t *testing.T) {
	bs := newTestBillingService()
	for _, tokens := range []UsageTokens{
		{CacheCreationTokens: 1_000_000},
		{CacheCreationTokens: 1_000_000, CacheCreation5mTokens: 1_000_000},
		{CacheCreationTokens: 1_000_000, CacheCreation1hTokens: 1_000_000},
	} {
		want := 20.0
		if tokens.CacheCreation1hTokens > 0 {
			want = 40
		}
		cost, err := bs.CalculateCost("kimi-k3", tokens, 1)
		require.NoError(t, err)
		require.InDelta(t, want, cost.CacheCreationCost, 1e-9)
		require.InDelta(t, want, cost.ActualCost, 1e-9)
	}
}

func TestKimiCustomPricingRemainsAuthoritative(t *testing.T) {
	bs := newTestBillingService()
	input, output := 1e-6, 2e-6
	card := ChannelModelPricing{Models: []string{"kimi-k3"}, BillingMode: BillingModeToken,
		InputPrice: &input, OutputPrice: &output}
	channel, err := bs.GetModelPricingWithChannel("kimi-k3", &card)
	require.NoError(t, err)
	require.Equal(t, input, channel.InputPricePerToken)
	require.Equal(t, output, channel.OutputPricePerToken)
	group := &Group{ID: 1, Platform: PlatformKimi, ModelPricing: []ChannelModelPricing{card}}
	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "kimi-k3", Group: group,
		Tokens:         UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000},
		RateMultiplier: 1, Resolver: NewModelPricingResolver(nil, bs),
	})
	require.NoError(t, err)
	require.InDelta(t, 5, cost.ActualCost, 1e-9) // Custom 1 + 2, inherited cache read 2.
}

func TestKimiAvailableChannelPricing(t *testing.T) {
	custom := 7e-6
	models := []SupportedModel{
		{Name: "kimi-k3", Platform: PlatformKimi},
		{Name: "kimi-k2.6", Platform: PlatformKimi, Pricing: &ChannelModelPricing{InputPrice: &custom}},
	}
	fillGlobalPricingFallback(&PricingService{}, models)
	require.NotNil(t, models[0].Pricing)
	require.InDelta(t, 20e-6, *models[0].Pricing.InputPrice, 1e-12)
	require.InDelta(t, 100e-6, *models[0].Pricing.OutputPrice, 1e-12)
	require.InDelta(t, 2e-6, *models[0].Pricing.CacheReadPrice, 1e-12)
	require.InDelta(t, 40e-6, *models[0].Pricing.CacheWrite1hPrice, 1e-12)
	require.Equal(t, custom, *models[1].Pricing.InputPrice)
}
