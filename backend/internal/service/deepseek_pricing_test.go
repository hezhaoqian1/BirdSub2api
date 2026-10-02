//go:build unit

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// deepseekPeakMultiplierAt：本站默认价卡峰谷策略
// 高峰时段每日 01:00–04:00 与 06:00–10:00 UTC（半开区间）；
// 不排除周末及节假日；高峰价 = 2× 低谷价。
// 2026-08-24 为周一（工作日），2026-08-22 周六、2026-08-23 周日。
// ---------------------------------------------------------------------------

func TestDeepseekPeakMultiplierAt(t *testing.T) {
	mon := func(hour, min int) time.Time { return time.Date(2026, 8, 24, hour, min, 0, 0, time.UTC) }
	sat := func(hour, min int) time.Time { return time.Date(2026, 8, 22, hour, min, 0, 0, time.UTC) }
	sun := func(hour, min int) time.Time { return time.Date(2026, 8, 23, hour, min, 0, 0, time.UTC) }

	tests := []struct {
		name string
		now  time.Time
		want float64
	}{
		// 工作日高峰窗口边界（半开区间）
		{"weekday 01:00 peak start", mon(1, 0), 2.0},
		{"weekday 03:59 peak upper bound", mon(3, 59), 2.0},
		{"weekday 04:00 peak end", mon(4, 0), 1.0},
		{"weekday 06:00 peak start", mon(6, 0), 2.0},
		{"weekday 09:59 peak upper bound", mon(9, 59), 2.0},
		{"weekday 10:00 peak end", mon(10, 0), 1.0},
		// 工作日低谷时段
		{"weekday 00:00 off-peak", mon(0, 0), 1.0},
		{"weekday 05:00 off-peak", mon(5, 0), 1.0},
		{"weekday 12:00 off-peak", mon(12, 0), 1.0},
		{"weekday 23:59 off-peak", mon(23, 59), 1.0},
		{"saturday utc 02:00 beijing sat 10:00", sat(2, 0), 2.0},
		{"sunday utc 07:00 beijing sun 15:00", sun(7, 0), 2.0},
		// 北京时间与 UTC 跨日边界：UTC 周六 16:30 = 北京周日 00:30，位于低谷窗口。
		{"utc saturday 16:30 = beijing sunday 00:30", sat(16, 30), 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, deepseekPeakMultiplierAt(tt.now))
		})
	}
}

func TestIsDeepSeekModel(t *testing.T) {
	deepseek := []string{
		"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp",
		"deepseek-chat", "deepseek-reasoner", "deepseek-v3-2-251201",
		"deepseek-coder", "deepseek-foo", "deepseek-v4-pro-0813",
		"DEEPSEEK-V4-PRO", " deepseek-v4-flash ",
	}
	for _, m := range deepseek {
		require.True(t, isDeepSeekModel(m), "model %q should be deepseek", m)
	}

	nonDeepseek := []string{
		"gpt-5.4", "claude-sonnet-4", "deepseekcoder", // 无连字符不算 deepseek- 前缀
		"", " deepseek", // 无连字符后缀
	}
	for _, m := range nonDeepseek {
		require.False(t, isDeepSeekModel(m), "model %q should not be deepseek", m)
	}
}

func TestDeepseekPeakMultiplierAtDailyBoundaries(t *testing.T) {
	beijing := time.FixedZone("Asia/Shanghai", 8*3600)
	days := []time.Time{
		time.Date(2026, 9, 26, 0, 0, 0, 0, beijing),
		time.Date(2026, 9, 27, 0, 0, 0, 0, beijing),
		time.Date(2026, 9, 28, 0, 0, 0, 0, beijing),
		time.Date(2026, 10, 2, 0, 0, 0, 0, beijing),
	}
	windows := []struct {
		offset time.Duration
		want   float64
	}{
		{9*time.Hour - time.Nanosecond, 1},
		{9 * time.Hour, 2},
		{12*time.Hour - time.Nanosecond, 2},
		{12 * time.Hour, 1},
		{14*time.Hour - time.Nanosecond, 1},
		{14 * time.Hour, 2},
		{18*time.Hour - time.Nanosecond, 2},
		{18 * time.Hour, 1},
		{24*time.Hour - time.Nanosecond, 1},
	}
	for _, day := range days {
		for _, window := range windows {
			instant := day.Add(window.offset)
			t.Run(instant.Format(time.RFC3339Nano), func(t *testing.T) {
				require.Equal(t, window.want, deepseekPeakMultiplierAt(instant))
				require.Equal(t, window.want, deepseekPeakMultiplierAt(instant.UTC()))
			})
		}
	}
}

func TestCalculateCostUnified_DeepseekWeekendAndHoliday(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)
	beijing := time.FixedZone("Asia/Shanghai", 8*3600)
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 2000}
	models := []struct {
		name       string
		inputCost  float64
		outputCost float64
		cacheCost  float64
	}{
		{"deepseek-v4.1-flash", 0.00015, 0.0003, 0.000006},
		{"deepseek-v4-pro", 0.00066, 0.00099, 0.000044},
	}
	for _, model := range models {
		for _, day := range []time.Time{
			time.Date(2026, 9, 26, 0, 0, 0, 0, beijing),
			time.Date(2026, 9, 27, 0, 0, 0, 0, beijing),
			time.Date(2026, 10, 2, 0, 0, 0, 0, beijing),
		} {
			for _, window := range []struct {
				hour       int
				multiplier float64
			}{{9, 2}, {12, 1}, {14, 2}, {18, 1}} {
				instant := day.Add(time.Duration(window.hour) * time.Hour)
				t.Run(model.name+"/"+instant.Format(time.RFC3339), func(t *testing.T) {
					cost, err := bs.CalculateCostUnified(CostInput{
						Ctx: context.Background(), Model: model.name, Tokens: tokens,
						RateMultiplier: 2.68, Resolver: resolver, PricingAt: instant,
					})
					require.NoError(t, err)
					require.InDelta(t, model.inputCost*window.multiplier, cost.InputCost, 1e-12)
					require.InDelta(t, model.outputCost*window.multiplier, cost.OutputCost, 1e-12)
					require.InDelta(t, model.cacheCost*window.multiplier, cost.CacheReadCost, 1e-12)
					expected := (model.inputCost + model.outputCost + model.cacheCost) * window.multiplier
					require.InDelta(t, expected, cost.TotalCost, 1e-12)
					require.InDelta(t, expected*2.68, cost.ActualCost, 1e-12)
				})
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 默认价卡（Source=LiteLLM）按本站峰谷倍率计费；分组/渠道自定义定价不叠加
// ---------------------------------------------------------------------------

func TestCalculateCostUnified_DeepseekDefaultCardPeakMultiplier(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	// 低谷成本（2026-09-10 官方新价）：1000*1.5e-7 + 500*6e-7 + 1000*3e-9 = 4.53e-4
	offPeakTotal := 1000*1.5e-7 + 500*6e-7 + 1000*3e-9

	offPeak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), // 周一低谷
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal, offPeak.TotalCost, 1e-10)

	peak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC), // 周一高峰
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal*2, peak.TotalCost, 1e-10)
}

func TestCalculateCostUnified_DeepseekProDefaultCardPeakMultiplier(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	offPeakTotal := 1000*6.6e-7 + 500*1.98e-6 + 1000*2.2e-8

	offPeak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal, offPeak.TotalCost, 1e-10)

	peak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 6, 30, 0, 0, time.UTC), // 周一高峰
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal*2, peak.TotalCost, 1e-10)
}

func TestCalculateCostUnified_DeepseekVersionedNamePeakMultiplier(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	offPeakTotal := 1000*1.5e-7 + 500*6e-7 + 1000*3e-9

	offPeak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash-0731", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), // 周一低谷
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal, offPeak.TotalCost, 1e-10)

	peak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash-0731", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC), // 周一高峰
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal*2, peak.TotalCost, 1e-10)
}

func TestCalculateCostUnified_DeepseekGroupPricingNotScaledByPeak(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	inputPrice := 1e-6
	outputPrice := 2e-6
	group := &Group{
		ID: 1, Name: "ds-group", Platform: PlatformDeepseek, Status: StatusActive,
		ModelPricing: []ChannelModelPricing{{
			Models: []string{"deepseek-v4-flash"}, BillingMode: BillingModeToken,
			InputPrice: &inputPrice, OutputPrice: &outputPrice,
		}},
	}
	resolved := resolver.Resolve(context.Background(), PricingInput{Model: "deepseek-v4-flash", Group: group})
	require.Equal(t, PricingSourceGroup, resolved.Source)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	// 分组自定义价：1000*1e-6 + 500*2e-6 + 1000*3e-9（缓存读沿用官方 flash 价）
	groupTotal := 1000*1e-6 + 500*2e-6 + 1000*3e-9

	for _, pricingAt := range []time.Time{
		time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), // 低谷
		time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC),  // 高峰
		time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 2, 7, 0, 0, 0, time.UTC),
	} {
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "deepseek-v4-flash", Group: group,
			Tokens: tokens, RateMultiplier: 1.0, Resolver: resolver, PricingAt: pricingAt,
		})
		require.NoError(t, err)
		require.InDelta(t, groupTotal, cost.TotalCost, 1e-10,
			"分组自定义定价不应叠加默认峰谷倍率（pricingAt=%v）", pricingAt)
	}
}

func TestCalculateCostUnified_NonDeepseekDefaultCardNotScaledByPeak(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}
	total := 1000*3e-6 + 500*15e-6 // claude-sonnet-4 fallback

	for _, pricingAt := range []time.Time{
		time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC),
	} {
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "claude-sonnet-4", Tokens: tokens,
			RateMultiplier: 1.0, Resolver: resolver, PricingAt: pricingAt,
		})
		require.NoError(t, err)
		require.InDelta(t, total, cost.TotalCost, 1e-10,
			"非 DeepSeek 模型不应受默认峰谷倍率影响（pricingAt=%v）", pricingAt)
	}
}

func TestCalculateCostUnified_DeepseekPricingAtZeroFallsBackToNow(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}
	base := CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
	}

	// PricingAt 零值 → 回退 timezone.Now()，与显式传入当前时刻结果一致。
	costZero, err := bs.CalculateCostUnified(base)
	require.NoError(t, err)

	costNow, err := bs.CalculateCostUnified(CostInput{
		Ctx: base.Ctx, Model: base.Model, Tokens: base.Tokens,
		RateMultiplier: base.RateMultiplier, Resolver: base.Resolver,
		PricingAt: timezone.Now(),
	})
	require.NoError(t, err)
	require.Equal(t, costZero.TotalCost, costNow.TotalCost)
}

// ---------------------------------------------------------------------------
// 官方价强制覆盖（远端旧价兜底）与未知 deepseek-* flash 兜底
// ---------------------------------------------------------------------------

func TestGetModelPricing_DeepseekForcesOfficialRatesOverJSON(t *testing.T) {
	// JSON 给任意价（模拟远端旧价/占位价），deepseek-* 必须被强制覆盖为官方低谷价。
	pricingSvc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"deepseek-flash":               {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-v4-flash":            {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-v4-pro":              {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-v4-flash-vision-exp": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-chat":                {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
		"deepseek-reasoner":            {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheReadInputTokenCost: 1e-8},
	}}
	bs := NewBillingService(&config.Config{}, pricingSvc)

	tests := []struct {
		model                    string
		input, output, cacheRead float64
	}{
		// 2026-09-10 官方降价后：deepseek-flash（V4.1-Flash 新名）与旧名
		// deepseek-v4-flash 同按 Flash 新价。
		{"deepseek-flash", 1.5e-7, 6e-7, 3e-9},
		{"deepseek-v4-flash", 1.5e-7, 6e-7, 3e-9},
		{"deepseek-v4-flash-vision-exp", 1.5e-7, 6e-7, 3e-9},
		// 已停服的 chat/reasoner：即使 JSON 有旧条目也按 flash 价兜底。
		{"deepseek-chat", 1.5e-7, 6e-7, 3e-9},
		{"deepseek-reasoner", 1.5e-7, 6e-7, 3e-9},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			// Flash 档三档价不随计费时点变化。
			pricing, err := bs.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.InDelta(t, tt.input, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, tt.output, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, tt.cacheRead, pricing.CacheReadPricePerToken, 1e-15)
			// 固定时点复核：仍走 Flash 新价。
			atPricing, err := bs.getModelPricingAt(tt.model, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			require.InDelta(t, tt.input, atPricing.InputPricePerToken, 1e-15)
			require.InDelta(t, tt.output, atPricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, tt.cacheRead, atPricing.CacheReadPricePerToken, 1e-15)
			require.True(t, bs.HasIdentifiedTokenPricing(tt.model))
		})
	}

	// Pro 档（含版本化名称）在当前时点以及旧公告切换点前后均保持 Pro 价。
	for _, model := range []string{"deepseek-v4-pro", "deepseek-v4-pro-0813"} {
		for _, at := range []time.Time{
			time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 9, 14, 3, 59, 59, 0, time.UTC),
			time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		} {
			pricing, err := bs.getModelPricingAt(model, at)
			require.NoError(t, err)
			require.InDelta(t, deepseekProOffPeakInputPrice, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, deepseekProOffPeakOutputPrice, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, deepseekProOffPeakCacheRead, pricing.CacheReadPricePerToken, 1e-15)
		}
		pricing, err := bs.GetModelPricing(model)
		require.NoError(t, err)
		require.InDelta(t, deepseekProOffPeakInputPrice, pricing.InputPricePerToken, 1e-15)
		require.InDelta(t, deepseekProOffPeakOutputPrice, pricing.OutputPricePerToken, 1e-15)
		require.InDelta(t, deepseekProOffPeakCacheRead, pricing.CacheReadPricePerToken, 1e-15)
	}

	// 版本化名称（不在 JSON / fallbackPrices 精确表中）：按子串归档计价。
	// flash-0731 归 Flash 档，GetModelPricing 断言稳定。
	versioned := []struct {
		model                    string
		input, output, cacheRead float64
	}{
		{"deepseek-v4-flash-0731", 1.5e-7, 6e-7, 3e-9},
	}
	for _, tt := range versioned {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := bs.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.InDelta(t, tt.input, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, tt.output, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, tt.cacheRead, pricing.CacheReadPricePerToken, 1e-15)
		})
	}
}

func TestGetModelPricing_UnknownDeepseekMapsToFlash(t *testing.T) {
	// JSON 含 $0 占位条目（如旧 deepseek-v3-2-251201）：未知 deepseek-* 不再
	// fail-closed，统一按 flash 价兜底（1.5e-7/6e-7/3e-9），不得按 $0 计费。
	pricingSvc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"deepseek-v3-2-251201": {InputCostPerToken: 0, OutputCostPerToken: 0},
	}}
	bs := NewBillingService(&config.Config{}, pricingSvc)

	for _, m := range []string{"deepseek-v3-2-251201", "deepseek-chat", "deepseek-reasoner", "deepseek-foo"} {
		t.Run(m, func(t *testing.T) {
			pricing, err := bs.GetModelPricing(m)
			require.NoError(t, err)
			require.InDelta(t, 1.5e-7, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, 6e-7, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, 3e-9, pricing.CacheReadPricePerToken, 1e-15)
		})
	}
}

// ---------------------------------------------------------------------------
// 本地兜底 JSON：无 $0 占位条目，官方模型价格为官方低谷价
// ---------------------------------------------------------------------------

func TestDeepseekPricingFileMatchesOfficialRates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	pricingSvc := &PricingService{}
	pricingData, err := pricingSvc.parsePricingData(data)
	require.NoError(t, err)

	_, ok := pricingData["deepseek-v3-2-251201"]
	require.False(t, ok, "deepseek-v3-2-251201（$0 占位条目）必须从价格表中移除")
	for _, discontinued := range []string{"deepseek-chat", "deepseek-reasoner"} {
		_, ok := pricingData[discontinued]
		require.False(t, ok, "%s 已停止服务，必须从价格表中移除", discontinued)
	}

	tests := []struct {
		model                    string
		input, output, cacheRead float64
	}{
		{"deepseek-flash", 1.5e-7, 6e-7, 3e-9},
		{"deepseek-v4-flash", 1.5e-7, 6e-7, 3e-9},
		{"deepseek-v4-flash-vision-exp", 1.5e-7, 6e-7, 3e-9},
		{"deepseek-v4-pro", 6.6e-7, 1.98e-6, 2.2e-8},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			entry, ok := pricingData[tt.model]
			require.True(t, ok, "model %s must exist in pricing file", tt.model)
			require.InDelta(t, tt.input, entry.InputCostPerToken, 1e-15)
			require.InDelta(t, tt.output, entry.OutputCostPerToken, 1e-15)
			require.InDelta(t, tt.cacheRead, entry.CacheReadInputTokenCost, 1e-15)
		})
	}
}

// ---------------------------------------------------------------------------
// 2026-09-10 官方降价：deepseek-flash（V4.1-Flash 新名）与旧名同价；
// deepseek-v4-pro 按官方更新日志持续使用 Pro 价卡
// ---------------------------------------------------------------------------

func TestCalculateCostUnified_DeepseekFlashAndLegacyFlashShareNewRates(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	// 2026-09-10 官方新低谷价：1000*1.5e-7 + 500*6e-7 + 1000*3e-9 = 4.53e-4
	offPeakTotal := 1000*1.5e-7 + 500*6e-7 + 1000*3e-9

	// deepseek-flash 与 deepseek-v4-flash 都取 Flash 新价。
	// 时点取切换日 2026-09-14（周一）12:00 UTC 低谷，峰谷倍率不影响断言。
	for _, model := range []string{"deepseek-flash", "deepseek-v4-flash"} {
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: model, Tokens: tokens,
			RateMultiplier: 1.0, Resolver: resolver,
			PricingAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		})
		require.NoError(t, err)
		require.InDelta(t, offPeakTotal, cost.TotalCost, 1e-10, "model %s must use new flash rates", model)
	}
}

func TestCalculateCostUnified_DeepseekProKeepsProRates(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	proTotal := 1000*6.6e-7 + 500*1.98e-6 + 1000*2.2e-8

	// 旧公告切换时点之前（周末低谷）：按 Pro 价。
	before, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, proTotal, before.TotalCost, 1e-10,
		"deepseek-v4-pro must use Pro rates before the old cutoff")

	// 到达旧公告切换时点（周一低谷窗口）：仍按 Pro 价。
	after, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, proTotal, after.TotalCost, 1e-10,
		"deepseek-v4-pro must keep Pro rates after the old cutoff")

	// 版本化名称同口径：deepseek-v4-pro-0813 也按 Pro 价。
	versioned, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro-0813", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, proTotal, versioned.TotalCost, 1e-10,
		"versioned pro names must also retain Pro rates")

	// 高峰时段仍叠加 2x，不改变 Pro/Flash 档位。
	for _, model := range []string{"deepseek-v4-pro", "deepseek-v4-pro-0813"} {
		peak, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: model, Tokens: tokens,
			RateMultiplier: 1.0, Resolver: resolver,
			PricingAt: time.Date(2026, 9, 15, 2, 0, 0, 0, time.UTC),
		})
		require.NoError(t, err)
		require.InDelta(t, 2*proTotal, peak.TotalCost, 1e-10)
	}
}
