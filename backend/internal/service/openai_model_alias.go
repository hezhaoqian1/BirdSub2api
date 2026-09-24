package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func lastOpenAIModelSegment(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if strings.Contains(model, "/") {
		parts := strings.Split(model, "/")
		model = parts[len(parts)-1]
	}
	return strings.TrimSpace(model)
}

func canonicalizeOpenAIModelAliasSpelling(model string) string {
	return openai.CanonicalizeOpenAIModelAliasSpelling(model)
}

func normalizeKnownOpenAICodexModel(model string) string {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	if normalized == "" {
		return ""
	}

	if mapped := getNormalizedCodexModel(normalized); mapped != "" {
		return mapped
	}
	if strings.HasSuffix(normalized, "-openai-compact") {
		if mapped := getNormalizedCodexModel(strings.TrimSuffix(normalized, "-openai-compact")); mapped != "" {
			return mapped
		}
	}

	switch {
	case isOpenAIGPT6SolModel(normalized):
		return "gpt-6-sol"
	case isOpenAIGPT6LunaModel(normalized):
		return "gpt-6-luna"
	case normalized == "gpt-6" || normalized == "gpt-6-astra":
		return "gpt-6-astra"
	case strings.Contains(normalized, "gpt-5.6-sol"):
		return "gpt-5.6-sol"
	case strings.Contains(normalized, "gpt-5.6-terra"):
		return "gpt-5.6-terra"
	case strings.Contains(normalized, "gpt-5.6-luna"):
		return "gpt-5.6-luna"
	case normalized == "gpt-5.6":
		return "gpt-5.6-sol"
	case strings.HasPrefix(normalized, "gpt-5.6-"):
		suffix := strings.TrimPrefix(normalized, "gpt-5.6-")
		if suffix == "max" || isKnownCodexModelSuffix(suffix) {
			return "gpt-5.6-sol"
		}
		return ""
	case strings.Contains(normalized, "gpt-5.5-pro"):
		return "gpt-5.5-pro"
	case strings.Contains(normalized, "gpt-5.5"):
		return "gpt-5.5"
	case strings.Contains(normalized, "gpt-5.4-mini"):
		return "gpt-5.4-mini"
	case strings.Contains(normalized, "gpt-5.4-nano"):
		return "gpt-5.4-nano"
	case strings.Contains(normalized, "gpt-5.4"):
		return "gpt-5.4"
	case strings.Contains(normalized, "gpt-5.2"):
		return "gpt-5.2"
	case strings.Contains(normalized, "gpt-5.3-codex-spark"):
		return "gpt-5.3-codex-spark"
	case strings.Contains(normalized, "gpt-5.3-codex"):
		return "gpt-5.3-codex"
	case strings.Contains(normalized, "gpt-5.3"):
		return "gpt-5.3-codex"
	case strings.Contains(normalized, "codex"):
		return "gpt-5.3-codex"
	case strings.Contains(normalized, "gpt-5"):
		return "gpt-5.4"
	default:
		return ""
	}
}

// isOpenAIGPT56Model 判断是否 GPT-5.6 系列模型；入参可为原始模型名
// （含大小写/路径/后缀变体）或已归一化的基名，两者均能正确识别。
func isOpenAIGPT56Model(model string) bool {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	if normalized == "gpt-5.6" {
		return true
	}
	if suffix, ok := strings.CutPrefix(normalized, "gpt-5.6-"); ok && (suffix == "max" || isKnownCodexModelSuffix(suffix)) {
		return true
	}
	for _, prefix := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+"-") {
			return true
		}
	}
	return false
}

func isOpenAIGPT6EffortSuffix(suffix string) bool {
	switch strings.ToLower(strings.TrimSpace(suffix)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func isOpenAIGPT6SolLunaEffortSuffix(suffix string) bool {
	switch strings.ToLower(strings.TrimSpace(suffix)) {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// isOpenAIGPT6AstraModel reports GPT-6 Astra and provider/date/effort variants.
// The public "gpt-6" alias routes to Astra.
func isOpenAIGPT6AstraModel(model string) bool {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	return normalized == "gpt-6" || normalized == "gpt-6-astra" || strings.HasPrefix(normalized, "gpt-6-astra-")
}

func isOpenAIGPT6SolModel(model string) bool {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	return normalized == "gpt-6-sol" || strings.HasPrefix(normalized, "gpt-6-sol-")
}

func isOpenAIGPT6LunaModel(model string) bool {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	return normalized == "gpt-6-luna" || strings.HasPrefix(normalized, "gpt-6-luna-")
}

// isOpenAIGPT6PromptCacheModel is intentionally stricter than the family
// predicates above. Automatic cache-key injection is only enabled for the
// catalogued IDs and their explicit reasoning-effort suffixes.
func isOpenAIGPT6PromptCacheModel(model string) bool {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	if normalized == "gpt-6" {
		return true
	}
	for _, family := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		if normalized == family {
			return true
		}
		if suffix, ok := strings.CutPrefix(normalized, family+"-"); ok {
			if family == "gpt-6-astra" {
				return isOpenAIGPT6EffortSuffix(suffix)
			}
			return isOpenAIGPT6SolLunaEffortSuffix(suffix)
		}
	}
	return false
}

func appendUsageBillingModelCandidate(candidates []string, seen map[string]struct{}, model string) []string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return candidates
	}
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}

	add(trimmed)
	if canonical := canonicalizeOpenAIModelAliasSpelling(trimmed); canonical != "" {
		add(canonical)
	}
	if normalized := normalizeKnownOpenAICodexModel(trimmed); normalized != "" {
		add(normalized)
	}
	return candidates
}

func usageBillingModelCandidates(primary string, alternates ...string) []string {
	seen := make(map[string]struct{}, 1+len(alternates))
	candidates := appendUsageBillingModelCandidate(nil, seen, primary)
	for _, alternate := range alternates {
		candidates = appendUsageBillingModelCandidate(candidates, seen, alternate)
	}
	return candidates
}

func firstUsageBillingModel(candidates []string) string {
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isOpenAIGPT6Model(model string) bool {
	return isOpenAIGPT6AstraModel(model) || openai.IsGPT6SolOrLunaModelSpelling(model)
}
