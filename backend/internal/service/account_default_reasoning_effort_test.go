//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func defaultEffortTestAccount(effort any) *Account {
	account := rawChatCompletionsTestAccount()
	if effort != nil {
		account.Credentials[credKeyDefaultReasoningEffort] = effort
	}
	return account
}

func TestNormalizeDefaultReasoningEffortCredentials(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		creds   map[string]any
		want    any
		wantErr bool
	}{
		{name: "absent", creds: map[string]any{}, want: nil},
		{name: "valid lowercased and trimmed", creds: map[string]any{credKeyDefaultReasoningEffort: " HIGH "}, want: "high"},
		{name: "max allowed", creds: map[string]any{credKeyDefaultReasoningEffort: "max"}, want: "max"},
		{name: "empty means off", creds: map[string]any{credKeyDefaultReasoningEffort: ""}, want: ""},
		{name: "unknown value rejected", creds: map[string]any{credKeyDefaultReasoningEffort: "xhigh"}, wantErr: true},
		{name: "non-string rejected", creds: map[string]any{credKeyDefaultReasoningEffort: 3}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := NormalizeDefaultReasoningEffortCredentials(tc.creds)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, tc.creds[credKeyDefaultReasoningEffort])
		})
	}
	require.NoError(t, NormalizeDefaultReasoningEffortCredentials(nil))
}

func TestApplyAccountDefaultReasoningEffort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		effort     any
		body       string
		wantEffort string
		wantInject bool
	}{
		{name: "unset leaves body", effort: nil, body: `{"model":"m","messages":[]}`},
		{name: "invalid stored value ignored", effort: "ultra", body: `{"model":"m","messages":[]}`},
		{name: "missing effort is injected", effort: "high", body: `{"model":"m","messages":[]}`, wantEffort: "high", wantInject: true},
		{name: "thinking enabled still injected", effort: "high", body: `{"model":"m","thinking":{"type":"enabled"}}`, wantEffort: "high", wantInject: true},
		{name: "client reasoning_effort wins", effort: "high", body: `{"model":"m","reasoning_effort":"max"}`, wantEffort: "max"},
		{name: "client none wins", effort: "high", body: `{"model":"m","reasoning_effort":"none"}`, wantEffort: "none"},
		{name: "client reasoning.effort wins", effort: "high", body: `{"model":"m","reasoning":{"effort":"low"}}`},
		{name: "thinking disabled skips", effort: "high", body: `{"model":"m","thinking":{"type":"disabled"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, injected := applyAccountDefaultReasoningEffort([]byte(tc.body), defaultEffortTestAccount(tc.effort))
			require.Equal(t, tc.wantInject, injected)
			require.Equal(t, tc.wantEffort, gjson.GetBytes(out, "reasoning_effort").String())
			if !tc.wantInject {
				require.Equal(t, tc.body, string(out))
			}
		})
	}
}

func forwardRawChatWithDefaultEffort(t *testing.T, ctx context.Context, account *Account, body string) (*httpUpstreamRecorder, *OpenAIForwardResult) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"c1","object":"chat.completion","model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	result, err := svc.forwardAsRawChatCompletions(ctx, c, account, []byte(body), "")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastBody)
	return upstream, result
}

func TestForwardAsRawChatCompletions_InjectsAccountDefaultReasoningEffort(t *testing.T) {
	body := `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`
	upstream, result := forwardRawChatWithDefaultEffort(t, context.Background(), defaultEffortTestAccount("high"), body)

	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
}

func TestForwardAsRawChatCompletions_KeepsClientReasoningEffort(t *testing.T) {
	body := `{"model":"deepseek-v4-pro","reasoning_effort":"max","messages":[{"role":"user","content":"hi"}]}`
	upstream, _ := forwardRawChatWithDefaultEffort(t, context.Background(), defaultEffortTestAccount("high"), body)

	require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
}

func TestForwardAsRawChatCompletions_NoDefaultLeavesEffortAbsent(t *testing.T) {
	body := `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`
	upstream, _ := forwardRawChatWithDefaultEffort(t, context.Background(), defaultEffortTestAccount(nil), body)

	require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning_effort").Exists())
}

func TestForwardAsRawChatCompletions_DefaultEffortRespectsGroupCeiling(t *testing.T) {
	body := `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`

	downgrade := WithOpenAIReasoningEffortPolicy(context.Background(), "medium", nil, ReasoningEffortOverLimitDowngrade)
	upstream, _ := forwardRawChatWithDefaultEffort(t, downgrade, defaultEffortTestAccount("max"), body)
	require.Equal(t, "medium", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())

	// A deny ceiling must not reject the client's request because of a value the
	// gateway added itself: the default is simply not injected.
	deny := WithOpenAIReasoningEffortPolicy(context.Background(), "medium", nil, ReasoningEffortOverLimitDeny)
	upstream, _ = forwardRawChatWithDefaultEffort(t, deny, defaultEffortTestAccount("max"), body)
	require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning_effort").Exists())
}
