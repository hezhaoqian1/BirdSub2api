//go:build unit

package service

import (
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHaiku55PricingTiers(t *testing.T) {
	data, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	catalog := &PricingService{}
	catalog.pricingData, err = catalog.parsePricingData(data)
	require.NoError(t, err)
	sources := map[string]*BillingService{
		"billing fallback": newTestBillingService(),
		"pricing fallback": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}),
		"catalog":          NewBillingService(&config.Config{}, catalog),
		// 远端价格表只带基础档价格时，仍需按官方 >100K 档计费。
		"remote without tiers": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
			"claude-haiku-5-5": {
				InputCostPerToken: 0.1e-6, OutputCostPerToken: 0.5e-6,
				CacheCreationInputTokenCost: 0.125e-6, CacheCreationInputTokenCostAbove1hr: 0.2e-6,
				CacheReadInputTokenCost: 0.01e-6, LiteLLMProvider: "anthropic", Mode: "chat",
			},
		}}),
	}
	for source, svc := range sources {
		for _, model := range []string{"claude-haiku-5-5", "anthropic/claude-haiku-5.5", "us.anthropic.claude-haiku-5-5"} {
			t.Run(source+"/"+model, func(t *testing.T) {
				// prompt = input + cache read + cache write; 100,000 stays on the base card.
				for _, prompt := range []int{99_999, 100_000, 100_001} {
					tokens := UsageTokens{
						InputTokens: prompt - 3000, CacheReadTokens: 2000, CacheCreationTokens: 1000,
						CacheCreation5mTokens: 400, CacheCreation1hTokens: 600, OutputTokens: 500,
					}
					cost, err := svc.CalculateCost(model, tokens, 1)
					require.NoError(t, err)
					m := 1.0
					if prompt > 100_000 {
						m = 5
					}
					require.InDelta(t, float64(tokens.InputTokens)*0.1e-6*m, cost.InputCost, 1e-12)
					require.InDelta(t, (400*0.125e-6+600*0.2e-6)*m, cost.CacheCreationCost, 1e-12)
					require.InDelta(t, 2000*0.01e-6*m, cost.CacheReadCost, 1e-12)
					require.InDelta(t, 500*0.5e-6*m, cost.OutputCost, 1e-12)
					require.Equal(t, prompt > 100_000, cost.LongContextBillingApplied)
				}
			})
		}
	}
}

func TestValidateHaiku55Request(t *testing.T) {
	ok := []string{
		`{"model":"claude-haiku-5-5","messages":[]}`,
		`{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`,
		`{"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`,
		`{"thinking":{"type":"disabled"}}`,
		`{"tool_choice":{"type":"any"}}`,
		`{"tool_choice":{"type":"tool","name":"x"}}`,
		`{"temperature":1}`,
		`{"top_p":0.99}`,
	}
	for _, body := range ok {
		require.NoError(t, validateClaude55Request([]byte(body), "claude-haiku-5-5"), body)
	}
	bad := []string{
		`{"thinking":{"type":"enabled","budget_tokens":2048}}`,
		`{"thinking":{"type":"between_tools"}}`,
		`{"thinking":{"type":"disabled"},"output_config":{"effort":"xhigh"}}`,
		`{"thinking":{"type":"disabled"},"output_config":{"effort":"max"}}`,
		`{"thinking":{"type":"adaptive","budget_tokens":1024}}`,
		`{"temperature":0}`,
		`{"top_p":1}`,
		`{"temperature":1,"top_p":0.99}`,
		`{"top_k":5}`,
	}
	for _, body := range bad {
		require.Error(t, validateClaude55Request([]byte(body), "us.anthropic.claude-haiku-5-5"), body)
	}
	// Older Haiku keeps its own surface.
	require.NoError(t, validateClaude55Request([]byte(`{"temperature":0,"thinking":{"type":"enabled","budget_tokens":2048}}`), "claude-haiku-4-5"))
}

func TestHaiku55BedrockThinkingNormalization(t *testing.T) {
	out := sanitizeBedrockThinking([]byte(`{"thinking":{"type":"enabled","budget_tokens":4096}}`), "us.anthropic.claude-haiku-5-5")
	require.Equal(t, "adaptive", gjson.GetBytes(out, "thinking.type").String())
	require.False(t, gjson.GetBytes(out, "thinking.budget_tokens").Exists())

	out = sanitizeBedrockThinking([]byte(`{"thinking":{"type":"disabled"}}`), "us.anthropic.claude-haiku-5-5")
	require.Equal(t, "disabled", gjson.GetBytes(out, "thinking.type").String())
}

func TestHaiku55ToolsetDropsFineGrainedStreamingBeta(t *testing.T) {
	body := []byte(`{"tools":[{"type":"computer_toolset_20260801"}]}`)
	header := "fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14"
	require.Equal(t, "interleaved-thinking-2025-05-14", filterSonnet55ToolsetBeta(header, body, "claude-haiku-5-5"))
	require.Equal(t, header, filterSonnet55ToolsetBeta(header, body, "claude-haiku-4-5"))
}

func TestHaiku55ResponsesConversion(t *testing.T) {
	req := &apicompat.ResponsesRequest{Model: "claude-haiku-5-5", Input: []byte(`"hi"`)}
	out, err := apicompat.ResponsesToAnthropicRequest(req)
	require.NoError(t, err)
	require.Equal(t, "adaptive", out.Thinking.Type)
	require.Equal(t, "medium", out.OutputConfig.Effort)

	req = &apicompat.ResponsesRequest{
		Model: "claude-haiku-5-5", Input: []byte(`"hi"`),
		Reasoning:  &apicompat.ResponsesReasoning{Effort: "none"},
		Tools:      []apicompat.ResponsesTool{{Type: "function", Name: "lookup", Parameters: []byte(`{"type":"object"}`)}},
		ToolChoice: []byte(`"required"`),
	}
	out, err = apicompat.ResponsesToAnthropicRequest(req)
	require.NoError(t, err)
	require.Equal(t, "disabled", out.Thinking.Type)
	require.Equal(t, "low", out.OutputConfig.Effort)
	require.Equal(t, "any", gjson.GetBytes(out.ToolChoice, "type").String())

	temp := 0.2
	_, err = apicompat.ResponsesToAnthropicRequest(&apicompat.ResponsesRequest{Model: "claude-haiku-5-5", Input: []byte(`"hi"`), Temperature: &temp})
	require.Error(t, err)
}
