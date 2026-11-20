package claude

import "strings"

// LongOutputMaxTokens is the documented output ceiling of the Claude families
// in longOutputFamilies.
const LongOutputMaxTokens = 128000

// longOutputFamilies are the Claude families that accept max_tokens up to
// 128K. Thinking on these models is adaptive (and always on for Opus 5.5 and
// Fable/Mythos 5.x), and thinking tokens are drawn from the same max_tokens
// budget, so a small converted default cuts responses off mid-answer.
var longOutputFamilies = []string{
	"claude-fable-5",
	"claude-mythos-5",
	"claude-opus-5",
	"claude-opus-4-8",
	"claude-opus-4-7",
	"claude-opus-4-6",
	"claude-sonnet-5",
	"claude-sonnet-4-6",
	"claude-haiku-5-5",
}

// DefaultMaxTokens returns the max_tokens to send upstream when a converted
// request carries none: the model's 128K ceiling for the families above, or 0
// for every other model so the caller keeps its own fallback.
func DefaultMaxTokens(model string) int {
	id := normalizeEffortModelID(model)
	for _, family := range longOutputFamilies {
		if id == family || strings.HasPrefix(id, family+"-") || strings.HasPrefix(id, family+"[") {
			return LongOutputMaxTokens
		}
	}
	return 0
}
