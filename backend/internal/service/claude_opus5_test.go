package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Claude Opus 5 官方定价（USD per token）：$5 输入 / $25 输出 per MTok。
const (
	opus5InputPricePerToken         = 5e-6
	opus5OutputPricePerToken        = 25e-6
	opus5CacheCreationPricePerToken = 6.25e-6
	opus5CacheReadPricePerToken     = 0.5e-6
)

const (
	opus55InputPricePerToken         = 4e-6
	opus55OutputPricePerToken        = 20e-6
	opus55CacheCreationPricePerToken = 5e-6
	opus55CacheReadPricePerToken     = 0.2e-6
)

// TestClaudeOpus5_FamilyFallbackDoesNotUseOpus4Rates 覆盖定价数据里还没有
// claude-opus-5 条目的场景（远端价格表滞后于模型发布）。
// 修复前 matchByModelFamily 会把 claude-opus-5 归到 "opus-4" 系列，
// 按 $15/$75 计费 —— 输入与输出双双 3 倍超收。
func TestClaudeOpus5_FamilyFallbackDoesNotUseOpus4Rates(t *testing.T) {
	svc := NewBillingService(&config.Config{}, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			// 有 4.8（同价），故意不放 claude-opus-5
			"claude-opus-4-8": {
				InputCostPerToken:           opus5InputPricePerToken,
				OutputCostPerToken:          opus5OutputPricePerToken,
				CacheCreationInputTokenCost: opus5CacheCreationPricePerToken,
				CacheReadInputTokenCost:     opus5CacheReadPricePerToken,
			},
			// 修复前会被误命中的旧 Opus 条目（$15/$75）
			"claude-opus-4-1":        {InputCostPerToken: 15e-6, OutputCostPerToken: 75e-6},
			"claude-opus-4-20250514": {InputCostPerToken: 15e-6, OutputCostPerToken: 75e-6},
			"claude-3-opus-20240229": {InputCostPerToken: 15e-6, OutputCostPerToken: 75e-6},
		},
	})

	for _, model := range []string{"claude-opus-5", "us.anthropic.claude-opus-5-v1"} {
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err)
			require.NotNil(t, pricing)
			assert.InDelta(t, opus5InputPricePerToken, pricing.InputPricePerToken, 1e-12)
			assert.InDelta(t, opus5OutputPricePerToken, pricing.OutputPricePerToken, 1e-12)
		})
	}
}

// TestClaudeOpus5_HardcodedFallbackPricing 覆盖动态价格服务完全不可用时的
// 硬编码兜底表。同时锁定不能被 "opus-5" 子串误伤的相邻型号。
func TestClaudeOpus5_HardcodedFallbackPricing(t *testing.T) {
	// pricingService 为 nil，强制走硬编码兜底表
	svc := NewBillingService(&config.Config{}, nil)

	tests := []struct {
		model  string
		input  float64
		output float64
	}{
		{"claude-opus-5", opus5InputPricePerToken, opus5OutputPricePerToken},
		{"us.anthropic.claude-opus-5-v1", opus5InputPricePerToken, opus5OutputPricePerToken},
		// 4.8 与 5 同价；修复前兜底表缺失，会掉到 claude-3-opus 的 $15/$75
		{"claude-opus-4-8", opus5InputPricePerToken, opus5OutputPricePerToken},
		// 相邻型号不能被 "opus-5" 误匹配
		{"claude-opus-4-5-20251101", 5e-6, 25e-6},
		{"claude-opus-4-1-20250805", 15e-6, 75e-6},
		{"claude-3-opus-20240229", 15e-6, 75e-6},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.NotNil(t, pricing)
			assert.InDelta(t, tt.input, pricing.InputPricePerToken, 1e-12)
			assert.InDelta(t, tt.output, pricing.OutputPricePerToken, 1e-12)
		})
	}

	opus5, err := svc.GetModelPricing("claude-opus-5")
	require.NoError(t, err)
	assert.InDelta(t, opus5CacheCreationPricePerToken, opus5.CacheCreationPricePerToken, 1e-12)
	assert.InDelta(t, opus5CacheReadPricePerToken, opus5.CacheReadPricePerToken, 1e-12)
}

// TestClaudeOpus5_BedrockCapabilityGates 锁定只有主版本号的模型 ID
// （claude-opus-5 / claude-sonnet-5）能被版本闸门识别。
// 修复前 claudeVersionRe 强制要求 major-minor，这类 ID 完全不匹配，
// 会被当成旧模型降级。
func TestClaudeOpus5_BedrockCapabilityGates(t *testing.T) {
	tests := []struct {
		modelID       string
		claude45Newer bool
		toolSearch    bool
		opus47Newer   bool
	}{
		{"claude-opus-5", true, true, true},
		{"us.anthropic.claude-opus-5-v1", true, true, true},
		{"eu.anthropic.claude-opus-5-v1", true, true, true},
		{"claude-sonnet-5", true, true, false},
		{"us.anthropic.claude-sonnet-5-v1", true, true, false},
		// 回归保护：旧模型不能因为 minor 可选而被误判为新版本
		{"anthropic.claude-opus-4-1-v1", false, false, false},
		{"anthropic.claude-sonnet-4-0-v1", false, false, false},
		{"anthropic.claude-3-opus-20240229-v1:0", false, false, false},
		{"us.anthropic.claude-opus-4-8-v1", true, true, true},
		// Haiku 不支持 tool search
		{"us.anthropic.claude-haiku-4-5-20251001-v1:0", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			assert.Equal(t, tt.claude45Newer, isBedrockClaude45OrNewer(tt.modelID), "isBedrockClaude45OrNewer")
			assert.Equal(t, tt.toolSearch, bedrockModelSupportsToolSearch(tt.modelID), "bedrockModelSupportsToolSearch")
			assert.Equal(t, tt.opus47Newer, isBedrockOpus47OrNewer(tt.modelID), "isBedrockOpus47OrNewer")
		})
	}
}

// TestClaudeOpus5_BedrockThinkingConvertedToAdaptive 验证 Opus 5 在 Bedrock 路径上
// 会把 thinking.type=enabled 转成 adaptive 并移除 budget_tokens。
// 上游 Opus 5 已移除 budget_tokens，透传过去会直接 400。
func TestClaudeOpus5_BedrockThinkingConvertedToAdaptive(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled","budget_tokens":10000}}`)
	got := sanitizeBedrockThinking(body, "us.anthropic.claude-opus-5-v1")

	assert.JSONEq(t, `{"thinking":{"type":"adaptive"}}`, string(got))
}

// TestClaudeOpus5_CatalogAndBedrockMapping 锁定模型清单与 Bedrock 默认映射。
func TestClaudeOpus5_CatalogAndBedrockMapping(t *testing.T) {
	assert.Contains(t, claude.DefaultModelIDs(), "claude-opus-5")

	mapped, ok := domain.DefaultBedrockModelMapping["claude-opus-5"]
	require.True(t, ok, "claude-opus-5 missing from DefaultBedrockModelMapping")
	assert.Equal(t, "us.anthropic.claude-opus-5-v1", mapped)
}

func TestClaudeOpus55_HardcodedFallbackPricing(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)
	pricing, err := svc.GetModelPricing("claude-opus-5-5")
	require.NoError(t, err)
	require.NotNil(t, pricing)
	assert.InDelta(t, opus55InputPricePerToken, pricing.InputPricePerToken, 1e-12)
	assert.InDelta(t, opus55OutputPricePerToken, pricing.OutputPricePerToken, 1e-12)
	assert.InDelta(t, opus55CacheCreationPricePerToken, pricing.CacheCreation5mPrice, 1e-12)
	assert.InDelta(t, 8e-6, pricing.CacheCreation1hPrice, 1e-12)
	assert.InDelta(t, opus55CacheReadPricePerToken, pricing.CacheReadPricePerToken, 1e-12)
	assert.InDelta(t, 8e-6, pricing.InputPricePerTokenPriority, 1e-12)
}

func TestClaudeOpus55_FamilyPricingDoesNotMatchOpus5(t *testing.T) {
	want := &LiteLLMModelPricing{InputCostPerToken: opus55InputPricePerToken, OutputCostPerToken: opus55OutputPricePerToken}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"claude-opus-5":   {InputCostPerToken: opus5InputPricePerToken, OutputCostPerToken: opus5OutputPricePerToken},
		"claude-opus-5-5": want,
	}}
	assert.Same(t, want, svc.matchByModelFamily("anthropic.claude-opus-5-5"))
}

func TestClaudeOpus55_RequestCompatibility(t *testing.T) {
	for _, thinkingType := range []string{"enabled", "disabled", "adaptive"} {
		body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"` + thinkingType + `","budget_tokens":9000},"tool_choice":{"type":"tool","name":"bash"}}`)
		got := sanitizeClaudeOpus55RequestBody(body, "")
		assert.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
		assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
		assert.True(t, gjson.GetBytes(got, "tool_choice").IsObject())
		assert.Equal(t, "auto", gjson.GetBytes(got, "tool_choice.type").String())
		assert.False(t, gjson.GetBytes(got, "tool_choice.name").Exists())
	}

	untouched := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":9000},"tool_choice":{"type":"tool","name":"bash"}}`)
	assert.Equal(t, string(untouched), string(sanitizeClaudeOpus55RequestBody(untouched, "")))

	direct := sanitizeClaudeOpus55DirectRequestBody([]byte(`{"model":"claude-opus-5-5","tools":[{"type":"computer_20251124","name":"computer","display_width_px":1024,"display_height_px":768,"cache_control":{"type":"ephemeral"}}]}`), "")
	assert.Equal(t, "computer_toolset_20260801", gjson.GetBytes(direct, "tools.0.type").String())
	assert.False(t, gjson.GetBytes(direct, "tools.0.name").Exists())
	assert.False(t, gjson.GetBytes(direct, "tools.0.display_width_px").Exists())
	assert.False(t, gjson.GetBytes(direct, "tools.0.display_height_px").Exists())
	assert.Equal(t, "ephemeral", gjson.GetBytes(direct, "tools.0.cache_control.type").String())
	assert.Equal(t, "context-1m-2025-08-07", sanitizeClaudeOpus55DirectBetaHeader("computer-use-2025-11-24,context-1m-2025-08-07", "claude-opus-5-5"))
}

func TestClaudeOpus55_BedrockMappingAndPreparation(t *testing.T) {
	assert.Contains(t, claude.DefaultModelIDs(), "claude-opus-5-5")
	mapped, ok := domain.DefaultBedrockModelMapping["claude-opus-5-5"]
	require.True(t, ok)
	assert.Equal(t, "anthropic.claude-opus-5-5", mapped)
	assert.Equal(t, mapped, AdjustBedrockModelRegionPrefix(mapped, "eu-west-1"))

	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"disabled","budget_tokens":9000},"tool_choice":{"type":"any"},"tools":[{"type":"computer_20251124","name":"computer"}],"messages":[]}`)
	got, err := PrepareBedrockRequestBodyWithTokens(body, mapped, nil, false)
	require.NoError(t, err)
	assert.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
	assert.True(t, gjson.GetBytes(got, "tool_choice").IsObject())
	assert.Equal(t, "auto", gjson.GetBytes(got, "tool_choice.type").String())
	assert.Equal(t, "computer_20251124", gjson.GetBytes(got, "tools.0.type").String())
}

func assertClaudeOpus55CompatibleBody(t *testing.T, body []byte) {
	t.Helper()
	assert.Equal(t, "adaptive", gjson.GetBytes(body, "thinking.type").String())
	assert.False(t, gjson.GetBytes(body, "thinking.budget_tokens").Exists())
	assert.True(t, gjson.GetBytes(body, "tool_choice").IsObject())
	assert.Equal(t, "auto", gjson.GetBytes(body, "tool_choice.type").String())
	assert.False(t, gjson.GetBytes(body, "tool_choice.name").Exists())
}

func assertClaudeOpus55DirectBody(t *testing.T, body []byte) {
	t.Helper()
	assertClaudeOpus55CompatibleBody(t, body)
	assert.Equal(t, "computer_toolset_20260801", gjson.GetBytes(body, "tools.0.type").String())
	assert.False(t, gjson.GetBytes(body, "tools.0.name").Exists())
	assert.False(t, gjson.GetBytes(body, "tools.0.display_width_px").Exists())
	assert.False(t, gjson.GetBytes(body, "tools.0.display_height_px").Exists())
}

func assertClaudeOpus55DirectRequest(t *testing.T, req *http.Request) {
	t.Helper()
	assertClaudeOpus55DirectBody(t, readRequestBodyForTest(t, req))
	assert.NotContains(t, getHeaderRaw(req.Header, "anthropic-beta"), claudeOpus55LegacyComputerBeta)
}

func opus55RequestTestContext(path string) *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("anthropic-beta", claudeOpus55LegacyComputerBeta+",context-1m-2025-08-07")
	return c
}

func TestClaudeOpus55_RequestBuildersApplyCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":9000},"tool_choice":{"type":"tool","name":"bash"},"tools":[{"type":"computer_20251124","name":"computer","display_width_px":1024,"display_height_px":768}],"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		},
	}}

	t.Run("native anthropic", func(t *testing.T) {
		account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
		req, wireBody, err := svc.buildUpstreamRequest(
			context.Background(), opus55RequestTestContext("/v1/messages"), account,
			body, "sk-ant-test", "apikey", "claude-opus-5-5", false, false,
		)
		require.NoError(t, err)
		assertClaudeOpus55DirectBody(t, wireBody)
		assertClaudeOpus55DirectRequest(t, req)
	})

	t.Run("mapped native anthropic", func(t *testing.T) {
		account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
		aliasBody := []byte(`{"model":"team-opus","thinking":{"type":"disabled","budget_tokens":9000},"tool_choice":{"type":"any"},"messages":[]}`)
		_, wireBody, err := svc.buildUpstreamRequest(
			context.Background(), opus55RequestTestContext("/v1/messages"), account,
			aliasBody, "sk-ant-test", "apikey", "claude-opus-5-5", false, false,
		)
		require.NoError(t, err)
		assertClaudeOpus55CompatibleBody(t, wireBody)
	})

	t.Run("vertex", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAnthropic,
			Type:     AccountTypeServiceAccount,
			Credentials: map[string]any{
				"project_id": "vertex-project",
				"location":   "us-east5",
			},
		}
		req, _, err := svc.buildUpstreamRequest(
			context.Background(), opus55RequestTestContext("/v1/messages"), account,
			body, "vertex-token", "service_account", "claude-opus-5-5", false, false,
		)
		require.NoError(t, err)
		assertClaudeOpus55DirectRequest(t, req)
	})

	t.Run("API key passthrough", func(t *testing.T) {
		account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
		req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
			context.Background(), opus55RequestTestContext("/v1/messages"), account, body, "sk-ant-test",
		)
		require.NoError(t, err)
		assertClaudeOpus55DirectBody(t, wireBody)
		assertClaudeOpus55DirectRequest(t, req)
	})

	t.Run("count tokens", func(t *testing.T) {
		account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
		req, wireBody, err := svc.buildCountTokensRequest(
			context.Background(), opus55RequestTestContext("/v1/messages/count_tokens"), account,
			body, "sk-ant-test", "apikey", "claude-opus-5-5", false,
		)
		require.NoError(t, err)
		assertClaudeOpus55DirectBody(t, wireBody)
		assertClaudeOpus55DirectRequest(t, req)
	})

	t.Run("count tokens API key passthrough", func(t *testing.T) {
		account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
		req, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(
			context.Background(), opus55RequestTestContext("/v1/messages/count_tokens"), account, body, "sk-ant-test",
		)
		require.NoError(t, err)
		assertClaudeOpus55DirectRequest(t, req)
	})
}
