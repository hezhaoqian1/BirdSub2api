package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultModelsHaveUniqueIDs guards DefaultModels against an entry that
// both the fork and upstream added. The fork once listed claude-haiku-5-5
// twice, so /v1/models returned it twice.
func TestDefaultModelsHaveUniqueIDs(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, len(DefaultModels))
	for _, model := range DefaultModels {
		require.False(t, seen[model.ID], "duplicate DefaultModels ID %q", model.ID)
		seen[model.ID] = true
	}
}
