package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

// postOpenAIResponses 发送 Responses 探测并抽取 assistant 文本。
//
// 默认 body 带 stream: true，上游回 SSE；replace 模式下用户 body 可能是 stream: false，
// 或上游忽略 stream 直接回 JSON，所以按响应实际形态解析：
//   - 2xx + text/event-stream（或无 Content-Type 但 body 是 SSE）：逐行解析事件
//   - 其它：按 JSON 处理（与旧的非流式路径一致；非 2xx 的错误 body 原样返回给上层）
func postOpenAIResponses(ctx context.Context, fullURL string, payload []byte, headers map[string]string) (extractedText, rawBody string, status int, err error) {
	accept := "application/json"
	if gjson.GetBytes(payload, "stream").Bool() {
		accept = "text/event-stream"
	}
	resp, err := doMonitorPost(ctx, fullURL, payload, headers, accept)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if success && isEventStreamContentType(resp.Header.Get("Content-Type")) {
		text, raw, streamErr := readOpenAIResponsesStream(io.LimitReader(resp.Body, monitorStreamResponseMaxBytes))
		return text, raw, resp.StatusCode, streamErr
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, monitorResponseMaxBytes))
	if err != nil {
		return "", "", resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	if success && looksLikeSSEBody(respBody) {
		text, raw, streamErr := readOpenAIResponsesStream(bytes.NewReader(respBody))
		return text, raw, resp.StatusCode, streamErr
	}
	return extractOpenAIResponsesText(respBody), string(respBody), resp.StatusCode, nil
}

// readOpenAIResponsesStream 解析 Responses SSE，返回最终 assistant 文本。
//
// 文本优先级：output_text.delta 累积 > output_text.done 全文 > 终态事件里的 response 对象。
// 终态事件（completed / done / incomplete）之前流就断了视为失败：真实客户这时也拿不到完整回复。
// response.failed / cancelled / error 事件直接返回 error，带上上游给的错误信息。
func readOpenAIResponsesStream(r io.Reader) (extractedText, rawBody string, err error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), monitorStreamLineMaxBytes)

	var (
		raw       strings.Builder
		deltas    strings.Builder
		doneTexts []string
		final     string
		terminal  bool
	)
	for !terminal && scanner.Scan() {
		line := scanner.Text()
		if raw.Len() < monitorResponseMaxBytes {
			_, _ = raw.WriteString(line)
			_ = raw.WriteByte('\n')
		}
		data, ok := sseDataPayload(line)
		if !ok || data == "" {
			continue
		}
		if data == "[DONE]" {
			terminal = true
			continue
		}

		event := gjson.Parse(data)
		eventType := event.Get("type").String()
		switch eventType {
		case "response.output_text.delta":
			_, _ = deltas.WriteString(event.Get("delta").String())
		case "response.output_text.done":
			doneTexts = append(doneTexts, event.Get("text").String())
		case "response.completed", "response.done", "response.incomplete":
			final = event.Get("response").Raw
			terminal = true
		case "response.failed", "response.cancelled", "response.canceled", "error":
			return "", raw.String(), fmt.Errorf("stream %s: %s", eventType, responsesStreamErrorMessage(event))
		default:
			if eventType == "" && event.Get("error").IsObject() {
				return "", raw.String(), fmt.Errorf("stream error: %s", responsesStreamErrorMessage(event))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", raw.String(), fmt.Errorf("read stream: %w", err)
	}
	if !terminal {
		return "", raw.String(), errors.New("stream ended before response.completed")
	}

	text := deltas.String()
	if strings.TrimSpace(text) == "" {
		text = strings.Join(doneTexts, "")
	}
	if strings.TrimSpace(text) == "" && final != "" {
		text = extractOpenAIResponsesText([]byte(final))
	}
	return text, raw.String(), nil
}

// responsesStreamErrorMessage 从失败事件中取最有信息量的一段错误描述。
func responsesStreamErrorMessage(event gjson.Result) string {
	for _, path := range []string{
		"error.message",
		"response.error.message",
		"message",
		"error.code",
		"response.error.code",
		"code",
		"response.incomplete_details.reason",
	} {
		if v := strings.TrimSpace(event.Get(path).String()); v != "" {
			return v
		}
	}
	return "upstream reported failure"
}

func sseDataPayload(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
}

func isEventStreamContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

// looksLikeSSEBody 兜底：个别网关流式回包不带 text/event-stream 头。
func looksLikeSSEBody(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:"))
}
