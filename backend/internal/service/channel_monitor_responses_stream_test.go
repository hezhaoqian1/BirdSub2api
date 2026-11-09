//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// responsesSSEHandler 模拟流式 Responses 上游：按 challenge 算出答案，再用 events 模板拼 SSE。
// events 中的 %s 会被替换成答案（JSON 字符串已转义）。
type responsesSSEHandler struct {
	lastBody    map[string]any
	lastHeaders http.Header
	contentType string
	events      []string
}

func (h *responsesSSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.lastHeaders = r.Header.Clone()
	defer func() { _ = r.Body.Close() }()
	var parsed map[string]any
	_ = json.NewDecoder(r.Body).Decode(&parsed)
	h.lastBody = parsed

	answer := answerFromOpenAIRequest(parsed)
	quoted, _ := json.Marshal(answer)
	escaped := strings.Trim(string(quoted), `"`)

	contentType := h.contentType
	if contentType == "" {
		contentType = "text/event-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	for _, ev := range h.events {
		if strings.Contains(ev, "%s") {
			ev = fmt.Sprintf(ev, escaped)
		}
		_, _ = w.Write([]byte(ev))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func setupFakeResponsesSSE(t *testing.T, h *responsesSSEHandler) string {
	t.Helper()
	swapMonitorHTTPClient(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func runMonitorResponsesCheck(endpoint string) *CheckResult {
	return runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-test", &CheckOptions{
		APIMode: MonitorAPIModeResponses,
	})
}

const (
	sseCreated   = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
	sseReasoning = "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\"}}\n\n"
	sseDelta     = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"%s\"}\n\n"
	sseCompleted = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"error\":null}}\n\n"
)

func TestRunCheckForModel_OpenAIResponsesStream_DeltasPass(t *testing.T) {
	h := &responsesSSEHandler{events: []string{sseCreated, sseReasoning, sseDelta, sseCompleted}}
	endpoint := setupFakeResponsesSSE(t, h)

	res := runMonitorResponsesCheck(endpoint)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("streamed deltas should pass challenge, got status=%s message=%q", res.Status, res.Message)
	}
	if h.lastBody["stream"] != true {
		t.Errorf("default responses probe must request stream=true, got %v", h.lastBody["stream"])
	}
}

func TestRunCheckForModel_OpenAIResponsesStream_TextOnlyInCompletedEvent(t *testing.T) {
	completedWithOutput := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[" +
		"{\"type\":\"reasoning\",\"summary\":[]}," +
		"{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"%s\"}]}]}}\n\n"
	h := &responsesSSEHandler{events: []string{sseCreated, completedWithOutput}}
	endpoint := setupFakeResponsesSSE(t, h)

	res := runMonitorResponsesCheck(endpoint)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("text in response.completed should pass, got status=%s message=%q", res.Status, res.Message)
	}
}

func TestRunCheckForModel_OpenAIResponsesStream_OutputTextDone(t *testing.T) {
	done := "data: {\"type\":\"response.output_text.done\",\"text\":\"%s\"}\n\n"
	h := &responsesSSEHandler{events: []string{sseCreated, done, "data: [DONE]\n\n"}}
	endpoint := setupFakeResponsesSSE(t, h)

	res := runMonitorResponsesCheck(endpoint)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("output_text.done + [DONE] should pass, got status=%s message=%q", res.Status, res.Message)
	}
}

func TestRunCheckForModel_OpenAIResponsesStream_FailedEventIsError(t *testing.T) {
	failed := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"upstream exploded\"}}}\n\n"
	h := &responsesSSEHandler{events: []string{sseCreated, failed}}
	endpoint := setupFakeResponsesSSE(t, h)

	res := runMonitorResponsesCheck(endpoint)

	if res.Status != MonitorStatusError {
		t.Fatalf("response.failed should be error, got status=%s message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "upstream exploded") {
		t.Errorf("error message should carry upstream reason, got %q", res.Message)
	}
}

func TestRunCheckForModel_OpenAIResponsesStream_BareErrorEventIsError(t *testing.T) {
	errEvent := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"upstream_error\",\"message\":\"upstream error\"}}\n\n"
	h := &responsesSSEHandler{events: []string{sseCreated, sseDelta, errEvent}}
	endpoint := setupFakeResponsesSSE(t, h)

	res := runMonitorResponsesCheck(endpoint)

	if res.Status != MonitorStatusError {
		t.Fatalf("error event should be error even after deltas, got status=%s message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "upstream error") {
		t.Errorf("unexpected message %q", res.Message)
	}
}

func TestRunCheckForModel_OpenAIResponsesStream_TruncatedStreamIsError(t *testing.T) {
	h := &responsesSSEHandler{events: []string{sseCreated, sseDelta}}
	endpoint := setupFakeResponsesSSE(t, h)

	res := runMonitorResponsesCheck(endpoint)

	if res.Status != MonitorStatusError {
		t.Fatalf("stream without terminal event should be error, got status=%s message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "stream ended before response.completed") {
		t.Errorf("unexpected message %q", res.Message)
	}
}

func TestRunCheckForModel_OpenAIResponsesStream_SniffsSSEWithoutContentType(t *testing.T) {
	h := &responsesSSEHandler{contentType: "text/plain", events: []string{sseCreated, sseDelta, sseCompleted}}
	endpoint := setupFakeResponsesSSE(t, h)

	res := runMonitorResponsesCheck(endpoint)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("SSE body without event-stream content type should still parse, got status=%s message=%q", res.Status, res.Message)
	}
}

func TestRunCheckForModel_OpenAIResponsesStream_WrongAnswerFailsChallenge(t *testing.T) {
	wrong := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"[placeholder / NOT EXECUTED]\"}\n\n"
	h := &responsesSSEHandler{events: []string{sseCreated, wrong, sseCompleted}}
	endpoint := setupFakeResponsesSSE(t, h)

	res := runMonitorResponsesCheck(endpoint)

	if res.Status != MonitorStatusFailed {
		t.Fatalf("placeholder text should fail challenge, got status=%s message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "NOT EXECUTED") {
		t.Errorf("challenge mismatch should quote the streamed text, got %q", res.Message)
	}
}

func TestRunCheckForModel_OpenAIResponsesReplaceNonStreamJSONStillWorks(t *testing.T) {
	h := &openAICaptureHandler{rawResponse: `{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-test", &CheckOptions{
		APIMode:          MonitorAPIModeResponses,
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride: map[string]any{
			"model":        "gpt-test",
			"instructions": "health check",
			"input":        "Reply with exactly: ok",
			"stream":       false,
		},
	})

	if res.Status != MonitorStatusOperational {
		t.Fatalf("replace-mode non-stream JSON should still work, got status=%s message=%q", res.Status, res.Message)
	}
	if got := h.lastHeaders.Get("Accept"); got != "application/json" {
		t.Errorf("non-stream replace body should send Accept: application/json, got %q", got)
	}
}

func TestRunCheckForModel_OpenAIResponsesStream_HTTPErrorKeepsBody(t *testing.T) {
	h := &openAICaptureHandler{status: http.StatusBadGateway, rawResponse: `{"error":{"message":"Upstream access forbidden","type":"upstream_error"}}`}
	endpoint := setupFakeOpenAI(t, h)

	res := runMonitorResponsesCheck(endpoint)

	if res.Status != MonitorStatusError {
		t.Fatalf("non-2xx should be error, got %s", res.Status)
	}
	if !strings.Contains(res.Message, "upstream HTTP 502") || !strings.Contains(res.Message, "Upstream access forbidden") {
		t.Errorf("error message should keep upstream body, got %q", res.Message)
	}
}
