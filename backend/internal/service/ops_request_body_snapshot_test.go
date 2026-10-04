package service

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func decodeSnapshot(t *testing.T, raw string) OpsRequestBodySnapshot {
	t.Helper()
	require.NotEmpty(t, raw)
	require.True(t, json.Valid([]byte(raw)), "snapshot must be valid JSON")
	var snap OpsRequestBodySnapshot
	require.NoError(t, json.Unmarshal([]byte(raw), &snap))
	return snap
}

func TestBuildOpsRequestBodySnapshot_Empty(t *testing.T) {
	require.Equal(t, "", BuildOpsRequestBodySnapshot(nil))
}

func TestBuildOpsRequestBodySnapshot_AnthropicSmallBodyKeptWhole(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","max_tokens":20000,"stream":false,"system":[{"type":"text","text":"你好世界"}],` +
		`"thinking":{"type":"enabled","budget_tokens":1024},` +
		`"tools":[{"name":"get_weather","input_schema":{}},{"type":"web_search_20250305","name":"web_search"}],` +
		`"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"essay"}]}`
	snap := decodeSnapshot(t, BuildOpsRequestBodySnapshot([]byte(body)))

	require.False(t, snap.Truncated)
	require.Equal(t, body, snap.Body)
	require.Empty(t, snap.Head)
	s := snap.Summary
	require.Equal(t, len(body), s.SizeBytes)
	require.Equal(t, OpsRequestBodyFormatJSON, s.Format)
	require.Equal(t, "claude-sonnet-4-6", s.Model)
	require.NotNil(t, s.Stream)
	require.False(t, *s.Stream)
	require.EqualValues(t, 20000, *s.MaxTokens)
	require.Equal(t, 3, *s.MessageCount)
	require.Equal(t, 4, *s.SystemChars) // 按字符计数，而非字节
	require.Equal(t, 2, *s.ToolCount)
	require.Equal(t, []string{"get_weather", "web_search"}, s.ToolNames)
	require.JSONEq(t, `{"type":"enabled","budget_tokens":1024}`, string(s.Thinking))
}

func TestBuildOpsRequestBodySnapshot_OpenAIAndGeminiSummaries(t *testing.T) {
	chat := `{"model":"gpt-6.1","stream":true,"max_completion_tokens":512,"messages":[{"role":"user","content":"x"}],` +
		`"tools":[{"type":"function","function":{"name":"lookup"}}],"reasoning_effort":"high"}`
	s := decodeSnapshot(t, BuildOpsRequestBodySnapshot([]byte(chat))).Summary
	require.True(t, *s.Stream)
	require.EqualValues(t, 512, *s.MaxTokens)
	require.Equal(t, []string{"lookup"}, s.ToolNames)
	require.JSONEq(t, `"high"`, string(s.Reasoning))

	responses := `{"model":"gpt-6.1","instructions":"be brief","input":"hello","max_output_tokens":64}`
	s = decodeSnapshot(t, BuildOpsRequestBodySnapshot([]byte(responses))).Summary
	require.Equal(t, 1, *s.MessageCount)
	require.Equal(t, 8, *s.SystemChars)
	require.EqualValues(t, 64, *s.MaxTokens)

	gemini := `{"contents":[{"role":"user","parts":[{"text":"a"}]},{"role":"model","parts":[{"text":"b"}]}],` +
		`"systemInstruction":{"parts":[{"text":"abc"}]},"generationConfig":{"maxOutputTokens":99},` +
		`"tools":[{"functionDeclarations":[{"name":"f1"},{"name":"f2"}]}]}`
	s = decodeSnapshot(t, BuildOpsRequestBodySnapshot([]byte(gemini))).Summary
	require.Equal(t, 2, *s.MessageCount)
	require.Equal(t, 3, *s.SystemChars)
	require.EqualValues(t, 99, *s.MaxTokens)
	require.Equal(t, 2, *s.ToolCount)
	require.Equal(t, []string{"f1", "f2"}, s.ToolNames)
}

func TestBuildOpsRequestBodySnapshot_LargeBodyHeadTailRuneAligned(t *testing.T) {
	// 多字节字符铺满，确保截断点落在字符中间时也会被对齐。
	filler := strings.Repeat("长文本内容", 40000) // 每字 3 字节，约 600KB
	body := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"` + filler + `"}],"max_tokens":20000}`
	raw := BuildOpsRequestBodySnapshot([]byte(body))
	snap := decodeSnapshot(t, raw)

	require.True(t, snap.Truncated)
	require.Empty(t, snap.Body)
	require.LessOrEqual(t, len(snap.Head), OpsRequestBodySnapshotHeadBytes)
	require.LessOrEqual(t, len(snap.Tail), OpsRequestBodySnapshotTailBytes)
	require.True(t, utf8.ValidString(snap.Head))
	require.True(t, utf8.ValidString(snap.Tail))
	require.Equal(t, len(body), len(snap.Head)+snap.OmittedBytes+len(snap.Tail))
	require.True(t, strings.HasPrefix(body, snap.Head))
	require.True(t, strings.HasSuffix(body, snap.Tail))
	// 摘要基于完整请求体，即使 max_tokens 位于被省略区域之后也能取到。
	require.EqualValues(t, 20000, *snap.Summary.MaxTokens)
	require.Equal(t, 1, *snap.Summary.MessageCount)
	// 落库大小有界。
	require.Less(t, len(raw), 3*(OpsRequestBodySnapshotHeadBytes+OpsRequestBodySnapshotTailBytes))
}

func TestBuildOpsRequestBodySnapshot_BinaryBodyKeepsOnlySummary(t *testing.T) {
	body := append([]byte("--boundary\r\nContent-Type: image/png\r\n\r\n\x89PNG\x00\x01\x02"), make([]byte, 1024)...)
	snap := decodeSnapshot(t, BuildOpsRequestBodySnapshot(body))
	require.Equal(t, OpsRequestBodyFormatBinary, snap.Summary.Format)
	require.Equal(t, len(body), snap.Summary.SizeBytes)
	require.Empty(t, snap.Body)
	require.Empty(t, snap.Head)
	require.Empty(t, snap.Tail)
}

func TestBuildOpsRequestBodySnapshot_NonJSONText(t *testing.T) {
	snap := decodeSnapshot(t, BuildOpsRequestBodySnapshot([]byte("model=x&prompt=hello")))
	require.Equal(t, OpsRequestBodyFormatText, snap.Summary.Format)
	require.Equal(t, "model=x&prompt=hello", snap.Body)
}

func TestBuildOpsRequestBodySnapshot_RedactsCredentials(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","max_tokens":100,"mcp_servers":[{"type":"url","url":"https://mcp.example","authorization_token":"secret-mcp-token"}],` +
		`"metadata":{"api_key":"sk-live-123"},"messages":[{"role":"user","content":"keep this text"}]}`
	snap := decodeSnapshot(t, BuildOpsRequestBodySnapshot([]byte(body)))
	require.NotContains(t, snap.Body, "secret-mcp-token")
	require.NotContains(t, snap.Body, "sk-live-123")
	require.Contains(t, snap.Body, `"authorization_token":"[REDACTED]"`)
	require.Contains(t, snap.Body, `"api_key":"[REDACTED]"`)
	require.Contains(t, snap.Body, `"max_tokens":100`)
	require.Contains(t, snap.Body, "keep this text")
}

func TestRedactOpsRequestSnapshotText_UnterminatedValueAtFragmentEnd(t *testing.T) {
	// 首段恰好截断在敏感值中间。
	text := `{"model":"x","authorization_token":"secret-cut-in-the-mid`
	out := redactOpsRequestSnapshotText(text)
	require.NotContains(t, out, "secret-cut")
	require.Contains(t, out, `"authorization_token":"[REDACTED]`)
}
