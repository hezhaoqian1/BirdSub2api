package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToAnthropicRequest_DefaultMaxTokensByModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		want  int
	}{
		// 128K-output Claude families: thinking shares max_tokens, so the
		// converted default is the model ceiling instead of 8192.
		{"claude-fable-5-2", 128000},
		{"claude-opus-5-5", 128000},
		{"claude-sonnet-5-5", 128000},
		{"claude-haiku-5-5", 128000},
		{"claude-opus-4-8", 128000},
		{"claude-sonnet-4-6", 128000},
		// Everything else keeps the historical fallback.
		{"claude-opus-4-5", 8192},
		{"claude-haiku-4-5", 8192},
		{"deepseek-v4-pro", 8192},
		{"gemini-4-neon", 8192},
	}
	for _, tt := range tests {
		req := &ResponsesRequest{
			Model: tt.model,
			Input: json.RawMessage(`"hi"`),
		}
		out, err := ResponsesToAnthropicRequest(req)
		require.NoError(t, err, tt.model)
		require.Equal(t, tt.want, out.MaxTokens, tt.model)
	}
}

func TestResponsesToAnthropicRequest_ExplicitMaxOutputTokensWins(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"claude-fable-5-2", "claude-opus-4-5", "deepseek-v4-pro"} {
		limit := 4096
		req := &ResponsesRequest{
			Model:           model,
			Input:           json.RawMessage(`"hi"`),
			MaxOutputTokens: &limit,
		}
		out, err := ResponsesToAnthropicRequest(req)
		require.NoError(t, err, model)
		require.Equal(t, 4096, out.MaxTokens, model)
	}
}
