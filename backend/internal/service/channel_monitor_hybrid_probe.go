package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

func hybridStreamText(data string) (string, bool, bool) {
	if data == "[DONE]" {
		return "", true, false
	}
	value := gjson.Parse(data)
	kind := value.Get("type").String()
	if value.Get("error").Exists() || kind == "error" || kind == "response.failed" || kind == "response.incomplete" {
		return "", false, true
	}
	switch kind {
	case "response.output_text.delta":
		return value.Get("delta").String(), false, false
	case "content_block_delta":
		return value.Get("delta.text").String(), false, false
	case "message_stop", "response.completed":
		return "", true, false
	}
	text := value.Get("choices.0.delta.content").String()
	terminal := value.Get("choices.0.finish_reason").String() != ""
	if part := value.Get("candidates.0.content.parts.0.text").String(); part != "" {
		text = part
	}
	terminal = terminal || value.Get("candidates.0.finishReason").String() != ""
	return text, terminal, false
}

func runHybridProbe(ctx context.Context, monitor *ChannelMonitor, model string, client *http.Client) HybridMonitorMinute {
	startedAt := time.Now().UTC()
	point := HybridMonitorMinute{Minute: startedAt.Truncate(time.Minute), Source: "active", Status: "error", Reasons: []string{"probe_failed"}, ObservedAt: startedAt}
	adapter, mode, ok := providerAdapterFor(monitor.Provider, monitor.APIMode)
	if !ok {
		point.Status = "unknown"
		point.Detail = "不支持的探测协议"
		return point
	}
	challenge := generateChallenge()
	opts := &CheckOptions{APIMode: monitor.APIMode, ExtraHeaders: monitor.ExtraHeaders, BodyOverride: monitor.BodyOverride, BodyOverrideMode: monitor.BodyOverrideMode}
	raw, err := buildRequestBody(adapter, monitor.Provider, mode, model, challenge.Prompt, opts)
	if err != nil {
		point.Status = "unknown"
		point.Detail = "探测模板无效"
		return point
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		point.Status = "unknown"
		point.Detail = "探测模板无效"
		return point
	}
	path := adapter.buildPath(model)
	if monitor.Provider == MonitorProviderGemini {
		path = strings.Replace(path, ":generateContent", ":streamGenerateContent", 1) + "?alt=sse"
	} else {
		body["stream"] = true
	}
	if monitor.Provider != MonitorProviderGemini {
		body["model"] = model
	}
	raw, err = json.Marshal(body)
	if err != nil {
		point.Status = "unknown"
		return point
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(monitor.Endpoint, path), bytes.NewReader(raw))
	if err != nil {
		point.Status = "unknown"
		return point
	}
	for key, value := range mergeHeaders(adapter.buildHeaders(monitor.APIKey), opts) {
		request.Header.Set(key, value)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(HybridMonitorProbeHeader, "1")
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		point.Detail = "探测连接失败或请求超时"
		return point
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		point.Detail = fmt.Sprintf("探测返回 HTTP %d", response.StatusCode)
		return point
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		point.Detail = "探测未返回流式响应"
		return point
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 2<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var text strings.Builder
	terminal := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		delta, done, failed := hybridStreamText(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		if point.TTFTMs == nil && monitorStreamHasOutput(strings.TrimSpace(strings.TrimPrefix(line, "data:"))) {
			elapsed := time.Since(started).Milliseconds()
			point.TTFTMs = &elapsed
		}
		if failed {
			point.Detail = "流式响应报告失败"
			return point
		}
		if delta != "" {
			if point.TTFTMs == nil {
				elapsed := time.Since(started).Milliseconds()
				point.TTFTMs = &elapsed
			}
			text.WriteString(delta)
		}
		if done {
			terminal = true
			break
		}
	}
	if scanner.Err() != nil || !terminal {
		point.Detail = "响应未正常结束或超时"
		return point
	}
	if strings.TrimSpace(text.String()) == "" || (bodyOverrideMode(opts) != MonitorBodyOverrideModeReplace && !validateChallenge(text.String(), challenge.Expected)) {
		point.Status = "failed"
		point.Detail = "响应内容校验失败"
		return point
	}
	point.Status = "operational"
	point.Reasons = []string{}
	point.TTFTSamples = 1
	point.TTFTSufficient = point.TTFTMs != nil
	return point
}
