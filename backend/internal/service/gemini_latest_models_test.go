//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiLatestModelsPricing(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	pricing := &PricingService{}
	pricing.pricingData, err = pricing.parsePricingData(data)
	require.NoError(t, err)

	for _, tc := range []struct {
		model                string
		input, output, cache float64
	}{
		{"gemini-4-argon", 2, 10, 0.1},
		{"gemini-3.8-flash", 0.75, 3.75, 0.075},
		{"gemini-3.7-flash", 0.75, 3.75, 0.075},
		{"gemini-3.6-flash", 0.75, 3.75, 0.075},
		{"gemini-3.5-flash-lite", 0.3, 2.5, 0.03},
	} {
		for _, source := range []struct {
			name    string
			pricing *PricingService
		}{
			{"catalog", pricing}, {"fallback", nil},
		} {
			t.Run(tc.model+"/"+source.name, func(t *testing.T) {
				billing := NewBillingService(&config.Config{}, source.pricing)
				for _, prefix := range []string{"", "models/", "publishers/google/models/"} {
					cost, err := billing.CalculateCost(prefix+tc.model, UsageTokens{
						InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000,
					}, 0.5)
					require.NoError(t, err)
					require.InDelta(t, tc.input, cost.InputCost, 1e-12)
					require.InDelta(t, tc.output, cost.OutputCost, 1e-12)
					require.InDelta(t, tc.cache, cost.CacheReadCost, 1e-12)
					require.InDelta(t, (tc.input+tc.output+tc.cache)*0.5, cost.ActualCost, 1e-12)
				}
			})
		}
	}
	billing := NewBillingService(&config.Config{}, nil)
	for _, model := range []string{"gemini-4-argon-unknown", "gemini-3.5-flash-lite-image"} {
		require.Nil(t, billing.getFallbackPricing(model))
	}
}

func TestGeminiArgonNativeForwarding(t *testing.T) {
	for _, stream := range []bool{false, true} {
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
		t.Run(action, func(t *testing.T) {
			body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}}}`)
			responseBody := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":3,"cachedContentTokenCount":2}}`
			contentType := "application/json"
			if stream {
				contentType = "text/event-stream"
				responseBody = "data: " + responseBody + "\n\n"
			}
			upstream := &queuedHTTPUpstreamStub{
				responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(responseBody))}},
				onCall: func(req *http.Request, _ *queuedHTTPUpstreamStub) {
					require.Equal(t, "/v1beta/models/gemini-4-argon:"+action, req.URL.Path)
				},
			}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1beta/models/gemini-4-argon:"+action, body)
			result, err := svc.ForwardNative(context.Background(), c, geminiSignalTestAccount(), "gemini-4-argon", action, stream, body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), "ok")
			require.Len(t, upstream.requestBodies, 1)
			require.Equal(t, "medium", gjson.GetBytes(upstream.requestBodies[0], "generationConfig.thinkingConfig.thinkingLevel").String())
			require.Equal(t, "medium", optionalStringValue(result.ReasoningEffort))
			require.Equal(t, 8, result.Usage.OutputTokens)
			require.Equal(t, 2, result.Usage.CacheReadInputTokens)
		})
	}
}

// Argon can use a whole hop for thinking. Native clients must receive the
// checkpoint intact and be able to send it back without a model-name rewrite.
func TestGeminiArgonNativeContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
		t.Run(action, func(t *testing.T) {
			body := []byte(`{"contents":[{"role":"user","parts":[{"text":"continue"}]}],"generationConfig":{"maxOutputTokens":65536,"thinkingConfig":{"thinkingLevel":"high"}},"continuationToken":"previous-checkpoint"}`)
			responseBody := `{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"CONTINUATION","continuationToken":"next-checkpoint"}],"usageMetadata":{"promptTokenCount":142,"candidatesTokenCount":0,"thoughtsTokenCount":32768,"totalTokenCount":32910}}`
			contentType := "application/json"
			if stream {
				contentType = "text/event-stream"
				responseBody = "data: " + responseBody + "\n\n"
			}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}},
				Body: io.NopCloser(strings.NewReader(responseBody)),
			}}}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1beta/models/gemini-4-argon:"+action, body)
			result, err := svc.ForwardNative(context.Background(), c, geminiSignalTestAccount(), "gemini-4-argon", action, stream, body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"continuationToken":"next-checkpoint"`)
			require.Contains(t, recorder.Body.String(), `"finishReason":"CONTINUATION"`)
			require.Len(t, upstream.requestBodies, 1)
			require.Equal(t, "previous-checkpoint", gjson.GetBytes(upstream.requestBodies[0], "continuationToken").String())
			require.Equal(t, int64(65536), gjson.GetBytes(upstream.requestBodies[0], "generationConfig.maxOutputTokens").Int())
			require.Equal(t, 32768, result.Usage.OutputTokens)
		})
	}
}
