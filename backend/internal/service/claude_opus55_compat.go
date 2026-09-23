package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const claudeOpus55LegacyComputerBeta = "computer-use-2025-11-24"

func isClaudeOpus55Model(modelID string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(modelID)), "claude-opus-5-5")
}

// Opus 5.5 requires always-on adaptive thinking and only accepts auto/none
// tool choice. Normalize legacy client fields before provider-specific conversion.
func sanitizeClaudeOpus55RequestBody(body []byte, modelID string) []byte {
	if len(body) == 0 {
		return body
	}
	if strings.TrimSpace(modelID) == "" {
		modelID = gjson.GetBytes(body, "model").String()
	}
	if !isClaudeOpus55Model(modelID) {
		return body
	}
	if thinking := gjson.GetBytes(body, "thinking"); thinking.Exists() && thinking.IsObject() {
		typ := strings.ToLower(strings.TrimSpace(thinking.Get("type").String()))
		if typ == "enabled" || typ == "disabled" {
			if next, err := sjson.SetBytes(body, "thinking.type", "adaptive"); err == nil {
				body = next
			}
			typ = "adaptive"
		}
		if typ == "adaptive" {
			if next, err := sjson.DeleteBytes(body, "thinking.budget_tokens"); err == nil {
				body = next
			}
		}
	}
	if choice := gjson.GetBytes(body, "tool_choice"); choice.Exists() && choice.IsObject() {
		typ := strings.ToLower(strings.TrimSpace(choice.Get("type").String()))
		if typ == "any" || typ == "tool" {
			if next, err := sjson.SetRawBytes(body, "tool_choice", []byte(`{"type":"auto"}`)); err == nil {
				body = next
			}
		}
	}
	return body
}

// Claude API and Vertex require the 2026 computer toolset for Opus 5.5.
// Bedrock still requires computer_20251124, so Bedrock callers intentionally
// use sanitizeClaudeOpus55RequestBody without this additional conversion.
func sanitizeClaudeOpus55DirectRequestBody(body []byte, modelID string) []byte {
	if strings.TrimSpace(modelID) == "" {
		modelID = gjson.GetBytes(body, "model").String()
	}
	body = sanitizeClaudeOpus55RequestBody(body, modelID)
	if !isClaudeOpus55Model(modelID) {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	for i, tool := range tools.Array() {
		if tool.Get("type").String() != "computer_20251124" {
			continue
		}
		basePath := fmt.Sprintf("tools.%d", i)
		if next, err := sjson.SetBytes(body, basePath+".type", "computer_toolset_20260801"); err == nil {
			body = next
		}
		for _, field := range []string{"name", "display_width_px", "display_height_px", "display_number"} {
			if next, err := sjson.DeleteBytes(body, basePath+"."+field); err == nil {
				body = next
			}
		}
	}
	return body
}

func sanitizeClaudeOpus55DirectBetaHeader(header, modelID string) string {
	if !isClaudeOpus55Model(modelID) {
		return header
	}
	return stripBetaTokens(header, []string{claudeOpus55LegacyComputerBeta})
}

func sanitizeClaudeOpus55DirectRequestHeader(header http.Header, modelID string) {
	if !isClaudeOpus55Model(modelID) {
		return
	}
	beta := sanitizeClaudeOpus55DirectBetaHeader(getHeaderRaw(header, "anthropic-beta"), modelID)
	deleteHeaderAllForms(header, "anthropic-beta")
	if beta != "" {
		setHeaderRaw(header, "anthropic-beta", beta)
	}
}
