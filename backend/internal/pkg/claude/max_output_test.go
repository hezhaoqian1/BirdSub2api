package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultMaxTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		want  int
	}{
		{"claude-fable-5-2", LongOutputMaxTokens},
		{"claude-fable-5-1", LongOutputMaxTokens},
		{"claude-fable-5", LongOutputMaxTokens},
		{"claude-mythos-5-2", LongOutputMaxTokens},
		{"claude-opus-5-5", LongOutputMaxTokens},
		{"claude-opus-5.5", LongOutputMaxTokens},
		{"claude-opus-5", LongOutputMaxTokens},
		{"claude-opus-4-8", LongOutputMaxTokens},
		{"claude-opus-4-7", LongOutputMaxTokens},
		{"claude-opus-4-6", LongOutputMaxTokens},
		{"claude-opus-4-6-thinking", LongOutputMaxTokens},
		{"claude-opus-4-6[1m]", LongOutputMaxTokens},
		{"claude-sonnet-5-5", LongOutputMaxTokens},
		{"claude-sonnet-5", LongOutputMaxTokens},
		{"claude-sonnet-4-6", LongOutputMaxTokens},
		{"claude-haiku-5-5", LongOutputMaxTokens},
		{"anthropic.claude-opus-5-5", LongOutputMaxTokens},
		{"us.anthropic.claude-sonnet-4-6", LongOutputMaxTokens},
		{"models/claude-fable-5-2", LongOutputMaxTokens},
		{" Claude-Opus-5-5 ", LongOutputMaxTokens},

		// Lower or unknown ceilings keep the caller's fallback.
		{"claude-opus-4-5-20251101", 0},
		{"claude-opus-4-5", 0},
		{"claude-sonnet-4-5", 0},
		{"claude-haiku-4-5", 0},
		{"claude-opus-4-1", 0},
		{"claude-3-5-sonnet-20241022", 0},
		{"claude-mythos-preview", 0},
		{"claude-opus-55", 0},
		{"deepseek-v4-pro", 0},
		{"gemini-4-neon", 0},
		{"gpt-6.2-sol", 0},
		{"", 0},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, DefaultMaxTokens(tt.model), tt.model)
	}
}
