package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsHaiku55(t *testing.T) {
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

func TestDefaultModelsContainsHaiku55(t *testing.T) {
	t.Parallel()

	for _, model := range DefaultModels {
		if model.ID == "claude-haiku-5-5" {
			require.Equal(t, "Claude Haiku 5.5", model.DisplayName)
			require.Equal(t, "2026-10-07T00:00:00Z", model.CreatedAt)
			return
		}
	}
	t.Fatal("claude-haiku-5-5 missing from DefaultModels")
}
