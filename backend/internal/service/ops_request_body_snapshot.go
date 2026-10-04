package service

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// 报错请求的客户端输入快照：保留请求体开头与结尾各一段（中间省略），并附带一份
// 结构化摘要，供运维在错误详情中排查与重放。请求体可能极大（百万 token 级上下文
// 可达数 MB），因此只存首尾片段，避免撑爆错误日志表与异步写入队列。
const (
	OpsRequestBodySnapshotHeadBytes = 32 * 1024
	OpsRequestBodySnapshotTailBytes = 32 * 1024

	opsRequestBodySnapshotMaxToolNames = 50
	opsRequestBodySnapshotMaxRawField  = 512
	opsRequestBodyBinaryProbeBytes     = 8 * 1024
)

const (
	OpsRequestBodyFormatJSON   = "json"
	OpsRequestBodyFormatText   = "text"
	OpsRequestBodyFormatBinary = "binary"
)

// OpsRequestBodySummary 是请求体的结构化摘要（兼容 Anthropic / OpenAI / Gemini 请求格式）。
type OpsRequestBodySummary struct {
	SizeBytes    int             `json:"size_bytes"`
	Format       string          `json:"format"`
	Model        string          `json:"model,omitempty"`
	Stream       *bool           `json:"stream,omitempty"`
	MaxTokens    *int64          `json:"max_tokens,omitempty"`
	MessageCount *int            `json:"message_count,omitempty"`
	SystemChars  *int            `json:"system_chars,omitempty"`
	ToolCount    *int            `json:"tool_count,omitempty"`
	ToolNames    []string        `json:"tool_names,omitempty"`
	Thinking     json.RawMessage `json:"thinking,omitempty"`
	Reasoning    json.RawMessage `json:"reasoning,omitempty"`
}

// OpsRequestBodySnapshot 是落库到 ops_error_logs.request_body 的 JSON 结构。
// 请求体不超过首尾片段之和时完整保存在 Body；否则拆为 Head / Tail，
// OmittedBytes 为中间省略的字节数。二进制请求体（如图片上传）只保存摘要。
type OpsRequestBodySnapshot struct {
	Summary      OpsRequestBodySummary `json:"summary"`
	Truncated    bool                  `json:"truncated"`
	Body         string                `json:"body,omitempty"`
	Head         string                `json:"head,omitempty"`
	Tail         string                `json:"tail,omitempty"`
	OmittedBytes int                   `json:"omitted_bytes,omitempty"`
}

// BuildOpsRequestBodySnapshot 把请求体编码为快照 JSON；空请求体返回 ""。
func BuildOpsRequestBodySnapshot(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	snapshot := OpsRequestBodySnapshot{
		Summary: OpsRequestBodySummary{SizeBytes: len(body)},
	}

	switch {
	case opsRequestBodyLooksBinary(body):
		snapshot.Summary.Format = OpsRequestBodyFormatBinary
		snapshot.Truncated = true
		snapshot.OmittedBytes = len(body)
	default:
		if gjson.ValidBytes(body) {
			snapshot.Summary.Format = OpsRequestBodyFormatJSON
			summarizeOpsRequestBodyJSON(body, &snapshot.Summary)
		} else {
			snapshot.Summary.Format = OpsRequestBodyFormatText
		}
		if len(body) <= OpsRequestBodySnapshotHeadBytes+OpsRequestBodySnapshotTailBytes {
			snapshot.Body = redactOpsRequestSnapshotText(string(body))
		} else {
			headEnd := opsRuneAlignedHeadEnd(body, OpsRequestBodySnapshotHeadBytes)
			tailStart := opsRuneAlignedTailStart(body, len(body)-OpsRequestBodySnapshotTailBytes)
			snapshot.Truncated = true
			snapshot.Head = redactOpsRequestSnapshotText(string(body[:headEnd]))
			snapshot.Tail = redactOpsRequestSnapshotText(string(body[tailStart:]))
			snapshot.OmittedBytes = tailStart - headEnd
		}
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// opsRequestBodyLooksBinary 以开头一段判断请求体是否为二进制（含 NUL 或非 UTF-8）。
func opsRequestBodyLooksBinary(body []byte) bool {
	probe := body
	if len(probe) > opsRequestBodyBinaryProbeBytes {
		probe = probe[:opsRuneAlignedHeadEnd(probe, opsRequestBodyBinaryProbeBytes)]
	}
	return bytes.IndexByte(probe, 0) >= 0 || !utf8.Valid(probe)
}

// opsRuneAlignedHeadEnd 返回不超过 limit、且不切断 UTF-8 字符的截断位置。
func opsRuneAlignedHeadEnd(body []byte, limit int) int {
	if limit >= len(body) {
		return len(body)
	}
	end := limit
	for end > 0 && !utf8.RuneStart(body[end]) {
		end--
	}
	return end
}

// opsRuneAlignedTailStart 返回不小于 start、且不切断 UTF-8 字符的起始位置。
func opsRuneAlignedTailStart(body []byte, start int) int {
	if start <= 0 {
		return 0
	}
	for start < len(body) && !utf8.RuneStart(body[start]) {
		start++
	}
	return start
}

// summarizeOpsRequestBodyJSON 只遍历一次顶层字段，嵌套查找限定在对应子树内，
// 避免对数 MB 的请求体做多次全文扫描。
func summarizeOpsRequestBodyJSON(body []byte, summary *OpsRequestBodySummary) {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return
	}
	var maxTokens, generationConfig gjson.Result
	root.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "model":
			summary.Model = value.String()
		case "stream":
			if value.IsBool() {
				v := value.Bool()
				summary.Stream = &v
			}
		case "max_tokens", "max_completion_tokens", "max_output_tokens":
			if !maxTokens.Exists() {
				maxTokens = value
			}
		case "generationConfig":
			generationConfig = value
		case "messages", "contents":
			if value.IsArray() {
				n := len(value.Array())
				summary.MessageCount = &n
			}
		case "input":
			if summary.MessageCount == nil {
				n := 0
				if value.IsArray() {
					n = len(value.Array())
				} else if value.Type == gjson.String {
					n = 1
				}
				summary.MessageCount = &n
			}
		case "system", "instructions", "systemInstruction":
			n := opsRequestBodyTextChars(value)
			summary.SystemChars = &n
		case "tools":
			summarizeOpsRequestBodyTools(value, summary)
		case "thinking":
			summary.Thinking = opsRequestBodySmallRaw(value)
		case "reasoning", "reasoning_effort":
			summary.Reasoning = opsRequestBodySmallRaw(value)
		}
		return true
	})
	if !maxTokens.Exists() && generationConfig.Exists() {
		maxTokens = generationConfig.Get("maxOutputTokens")
	}
	if maxTokens.Exists() && maxTokens.Type == gjson.Number {
		v := maxTokens.Int()
		summary.MaxTokens = &v
	}
}

// opsRequestBodyTextChars 统计 system 类字段中的文本字符数（字符串，或 text/parts 数组）。
func opsRequestBodyTextChars(value gjson.Result) int {
	switch {
	case value.Type == gjson.String:
		return utf8.RuneCountInString(value.Str)
	case value.IsArray():
		total := 0
		value.ForEach(func(_, item gjson.Result) bool {
			total += opsRequestBodyTextChars(item)
			return true
		})
		return total
	case value.IsObject():
		if text := value.Get("text"); text.Exists() {
			return opsRequestBodyTextChars(text)
		}
		if parts := value.Get("parts"); parts.Exists() {
			return opsRequestBodyTextChars(parts)
		}
	}
	return 0
}

func summarizeOpsRequestBodyTools(tools gjson.Result, summary *OpsRequestBodySummary) {
	if !tools.IsArray() {
		return
	}
	count := 0
	names := make([]string, 0)
	addName := func(name string) {
		count++
		if name != "" && len(names) < opsRequestBodySnapshotMaxToolNames {
			names = append(names, name)
		}
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		// Gemini: tools[].functionDeclarations[].name
		if decls := tool.Get("functionDeclarations"); decls.IsArray() {
			decls.ForEach(func(_, decl gjson.Result) bool {
				addName(decl.Get("name").String())
				return true
			})
			return true
		}
		name := tool.Get("name").String()
		if name == "" {
			name = tool.Get("function.name").String() // OpenAI chat
		}
		if name == "" {
			name = tool.Get("type").String() // 内置工具，如 web_search
		}
		addName(name)
		return true
	})
	summary.ToolCount = &count
	if len(names) > 0 {
		summary.ToolNames = names
	}
}

func opsRequestBodySmallRaw(value gjson.Result) json.RawMessage {
	if !value.Exists() || len(value.Raw) == 0 || len(value.Raw) > opsRequestBodySnapshotMaxRawField {
		return nil
	}
	return json.RawMessage(value.Raw)
}

// opsSnapshotKVPattern 匹配 JSON 中的 "key": "value" 字符串键值对；value 允许在
// 片段末尾未闭合（首段恰好截断在敏感值中间时同样需要擦除）。
var opsSnapshotKVPattern = regexp.MustCompile(`"((?:[^"\\]|\\.){1,128})"\s*:\s*"((?:[^"\\]|\\.)*)("|$)`)

// redactOpsRequestSnapshotText 按 isSensitiveKey（与 error_body 落库脱敏同一套规则）
// 擦除凭据类字段的字符串值，例如 mcp_servers[].authorization_token、api_key。
// 只做文本级替换，非敏感内容保持原样，便于直接复制重放。
func redactOpsRequestSnapshotText(text string) string {
	matches := opsSnapshotKVPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		key := text[m[2]:m[3]]
		if !isSensitiveKey(key) || m[5] == m[4] {
			continue
		}
		b.WriteString(text[last:m[4]])
		b.WriteString("[REDACTED]")
		last = m[5]
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}
