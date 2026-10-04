package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// buildSSE 把一组 (event, dataJSON) 拼成 Anthropic 风格的 SSE 文本。
func buildSSE(events [][2]string) string {
	var b strings.Builder
	for _, ev := range events {
		if ev[0] != "" {
			b.WriteString("event: " + ev[0] + "\n")
		}
		b.WriteString("data: " + ev[1] + "\n\n")
	}
	return b.String()
}

const testMessageStart = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":10}}}`

func assemble(t *testing.T, sse string) (gjson.Result, *ClaudeUsage) {
	t.Helper()
	msg, usage, err := AssembleAnthropicStreamToMessage(strings.NewReader(sse), anthropicStreamAggregateOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gjson.ValidBytes(msg) {
		t.Fatalf("assembled body is not valid JSON: %s", msg)
	}
	return gjson.ParseBytes(msg), usage
}

func TestAssembleAnthropicStreamToMessage_TextAndUsage(t *testing.T) {
	parsed, usage := assemble(t, buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"ping", `{"type":"ping"}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello, "}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world!"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":7}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}))

	checks := map[string]string{
		"id":             "msg_1",
		"type":           "message",
		"role":           "assistant",
		"content.0.type": "text",
		"content.0.text": "Hello, world!",
		"stop_reason":    "end_turn",
	}
	for path, want := range checks {
		if got := parsed.Get(path).String(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if !parsed.Get("stop_sequence").Exists() || parsed.Get("stop_sequence").Type != gjson.Null {
		t.Errorf("stop_sequence should be present as null, got %s", parsed.Get("stop_sequence").Raw)
	}
	if got := parsed.Get("usage.output_tokens").Int(); got != 7 {
		t.Errorf("usage.output_tokens = %d, want 7", got)
	}
	if got := parsed.Get("usage.input_tokens").Int(); got != 25 {
		t.Errorf("usage.input_tokens = %d, want 25", got)
	}
	if usage.InputTokens != 25 || usage.OutputTokens != 7 || usage.CacheReadInputTokens != 10 {
		t.Errorf("billing usage = %+v, want input=25 output=7 cache_read=10", *usage)
	}
}

func TestAssembleAnthropicStreamToMessage_ToolUse(t *testing.T) {
	parsed, _ := assemble(t, buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me check."}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Tokyo\"}"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"now","input":{}}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":2}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}))

	if got := parsed.Get("content.1.id").String(); got != "toolu_1" {
		t.Errorf("content[1].id = %q, want toolu_1", got)
	}
	if got := parsed.Get("content.1.name").String(); got != "get_weather" {
		t.Errorf("content[1].name = %q, want get_weather", got)
	}
	if got := parsed.Get("content.1.input.city").String(); got != "Tokyo" {
		t.Errorf("content[1].input.city = %q, want Tokyo", got)
	}
	// 无参数工具：没有 input_json_delta 时保留 {}。
	if got := parsed.Get("content.2.input").Raw; got != "{}" {
		t.Errorf("content[2].input = %s, want {}", got)
	}
	if got := parsed.Get("stop_reason").String(); got != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", got)
	}
}

func TestAssembleAnthropicStreamToMessage_ThinkingAndCitations(t *testing.T) {
	parsed, _ := assemble(t, buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Step 1. "}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Step 2."}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"x"}}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Answer."}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}))

	if got := parsed.Get("content.0.thinking").String(); got != "Step 1. Step 2." {
		t.Errorf("content[0].thinking = %q", got)
	}
	if got := parsed.Get("content.0.signature").String(); got != "sig123" {
		t.Errorf("content[0].signature = %q, want sig123", got)
	}
	if got := parsed.Get("content.1.text").String(); got != "Answer." {
		t.Errorf("content[1].text = %q, want Answer.", got)
	}
	if got := parsed.Get("content.1.citations.0.cited_text").String(); got != "x" {
		t.Errorf("content[1].citations[0].cited_text = %q, want x", got)
	}
}

func TestAssembleAnthropicStreamToMessage_AlternateTerminalEvents(t *testing.T) {
	body := [][2]string{
		{"message_start", testMessageStart},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`},
	}
	cases := map[string]string{
		"data [DONE]":             buildSSE(body) + "data: [DONE]\n\n",
		"bare event message_stop": buildSSE(body) + "event: message_stop\ndata: {}\n\n",
	}
	for name, sse := range cases {
		t.Run(name, func(t *testing.T) {
			parsed, _ := assemble(t, sse)
			if got := parsed.Get("content.0.text").String(); got != "ok" {
				t.Errorf("content[0].text = %q, want ok", got)
			}
		})
	}
}

func TestAssembleAnthropicStreamToMessage_ZeroDeltaUsageDoesNotOverwrite(t *testing.T) {
	parsed, usage := assemble(t, buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`},
		// GLM/Kimi 风格：message_delta 里 input_tokens 为 0。
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"output_tokens":5}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}))
	if got := parsed.Get("usage.input_tokens").Int(); got != 25 {
		t.Errorf("client usage.input_tokens = %d, want 25 (not overwritten by 0)", got)
	}
	if int64(usage.InputTokens) != parsed.Get("usage.input_tokens").Int() {
		t.Errorf("billing input_tokens %d != client input_tokens %d", usage.InputTokens, parsed.Get("usage.input_tokens").Int())
	}
}

func TestAssembleAnthropicStreamToMessage_DeltaWithoutBlockStartInfersType(t *testing.T) {
	parsed, _ := assemble(t, buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"orphan"}}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}))
	if got := parsed.Get("content.0.type").String(); got != "text" {
		t.Errorf("content[0].type = %q, want text", got)
	}
	if got := parsed.Get("content.0.text").String(); got != "orphan" {
		t.Errorf("content[0].text = %q, want orphan", got)
	}
}

func TestAssembleAnthropicStreamToMessage_MissingTerminal(t *testing.T) {
	sse := buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`},
	})
	_, usage, err := AssembleAnthropicStreamToMessage(strings.NewReader(sse), anthropicStreamAggregateOptions{})
	if !errors.Is(err, errAnthropicStreamIncomplete) {
		t.Fatalf("err = %v, want errAnthropicStreamIncomplete", err)
	}
	if usage == nil || usage.InputTokens != 25 {
		t.Fatalf("observed usage should still be returned, got %+v", usage)
	}
}

func TestAssembleAnthropicStreamToMessage_ErrorEventMapsStatus(t *testing.T) {
	cases := []struct {
		errType string
		status  int
	}{
		{"overloaded_error", 529},
		{"rate_limit_error", http.StatusTooManyRequests},
		{"invalid_request_error", http.StatusBadRequest},
		{"api_error", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.errType, func(t *testing.T) {
			sse := buildSSE([][2]string{
				{"message_start", testMessageStart},
				{"error", `{"type":"error","error":{"type":"` + tc.errType + `","message":"boom"}}`},
			})
			_, _, err := AssembleAnthropicStreamToMessage(strings.NewReader(sse), anthropicStreamAggregateOptions{})
			var evErr *anthropicStreamEventError
			if !errors.As(err, &evErr) {
				t.Fatalf("err = %v, want *anthropicStreamEventError", err)
			}
			if evErr.StatusCode != tc.status {
				t.Errorf("status = %d, want %d", evErr.StatusCode, tc.status)
			}
			if got := gjson.GetBytes(evErr.Body, "error.type").String(); got != tc.errType {
				t.Errorf("body error.type = %q, want %q", got, tc.errType)
			}
			resp := evErr.response(nil)
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.status || gjson.GetBytes(b, "error.message").String() != "boom" {
				t.Errorf("synthetic response = %d %s", resp.StatusCode, b)
			}
		})
	}
}

func TestAssembleAnthropicStreamToMessage_ContentLimit(t *testing.T) {
	sse := buildSSE([][2]string{
		{"message_start", testMessageStart},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"0123456789"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"0123456789"}}`},
		{"message_stop", `{"type":"message_stop"}`},
	})
	_, _, err := AssembleAnthropicStreamToMessage(strings.NewReader(sse), anthropicStreamAggregateOptions{MaxContentBytes: 15})
	if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
		t.Fatalf("err = %v, want ErrUpstreamResponseBodyTooLarge", err)
	}
}

func TestAssembleAnthropicStreamToMessage_IdleTimeout(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		_, _ = io.WriteString(pw, buildSSE([][2]string{{"message_start", testMessageStart}}))
		// 之后保持连接不关闭、也不再发送数据：模拟上游卡死。
	}()
	start := time.Now()
	_, _, err := AssembleAnthropicStreamToMessage(pr, anthropicStreamAggregateOptions{IdleTimeout: 100 * time.Millisecond})
	if !errors.Is(err, errAnthropicStreamIdleTimeout) {
		t.Fatalf("err = %v, want errAnthropicStreamIdleTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("idle timeout took too long: %s", elapsed)
	}
}

func TestAccountIsAnthropicForceUpstreamStreamEnabled(t *testing.T) {
	cases := []struct {
		name  string
		acct  *Account
		wants bool
	}{
		{"nil", nil, false},
		{"passthrough + flag", &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{"anthropic_passthrough": true, "anthropic_force_upstream_stream": true}}, true},
		{"flag without passthrough", &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{"anthropic_force_upstream_stream": true}}, false},
		{"passthrough without flag", &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{"anthropic_passthrough": true}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.acct.IsAnthropicForceUpstreamStreamEnabled(); got != tc.wants {
				t.Errorf("got %v, want %v", got, tc.wants)
			}
		})
	}
}
