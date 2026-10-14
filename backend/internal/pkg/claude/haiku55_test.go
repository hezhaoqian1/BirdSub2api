package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIsHaiku55ExtraRegions 覆盖上游 effort_catalog_test.go 未包含的区域前缀
// 与推理强度档位断言。命名避开上游同名用例，防止后续同步上游时重复声明。
func TestIsHaiku55ExtraRegions(t *testing.T) {
	t.Parallel()
	for _, model := range []string{
		"claude-haiku-5-5",
		"anthropic/claude-haiku-5.5",
		"anthropic.claude-haiku-5-5",
		"us.anthropic.claude-haiku-5-5",
		"global.anthropic.claude-haiku-5-5",
		"jp.anthropic.claude-haiku-5-5",
	} {
		require.True(t, IsHaiku55(model), model)
		require.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, EffortLevelsForModel(model), model)
	}
	for _, model := range []string{"claude-haiku-4-5", "claude-haiku-4-5-20251001", "claude-haiku-5-5-preview", "claude-sonnet-5-5"} {
		require.False(t, IsHaiku55(model), model)
	}
	require.Nil(t, EffortLevelsForModel("claude-haiku-4-5-20251001"))
}
