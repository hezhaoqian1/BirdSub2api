package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func hybridTestRule() HybridMonitorRule {
	return HybridMonitorRule{ID: "test", Name: "Test", GroupID: 1, MonitorID: 2, Model: "model", Enabled: true, Public: true, RedSuccess: .6, GreenSuccess: .95, WarningTTFT: 10000, CriticalTTFT: 25000, MinimumTTFTSamples: 5}
}

func TestHybridThresholds(t *testing.T) {
	for _, test := range []struct {
		name      string
		successes int64
		ttft      int64
		samples   int
		status    string
	}{
		{"green", 100, 9999, 5, "operational"}, {"exact yellow", 100, 10000, 5, "degraded"}, {"before red", 100, 24999, 5, "degraded"}, {"exact red", 100, 25000, 5, "failed"}, {"sixty percent", 60, 2000, 5, "degraded"}, {"below sixty", 59, 2000, 5, "failed"}, {"ninety five", 95, 2000, 5, "operational"}, {"low TTFT samples", 100, 30000, 4, "operational"},
	} {
		t.Run(test.name, func(t *testing.T) {
			point := evaluateHybridMinute(hybridTestRule(), HybridMonitorMinute{Source: "passive", Requests: 100, Successes: test.successes, Failures: 100 - test.successes, TTFTMs: &test.ttft, TTFTSamples: test.samples})
			require.Equal(t, test.status, point.Status)
		})
	}
	point := evaluateHybridMinute(hybridTestRule(), HybridMonitorMinute{Source: "passive", Requests: 10, Successes: 6, Failures: 4, Excluded: 4})
	require.Equal(t, .6, *point.SuccessRate)
	require.Equal(t, 1., *point.AlertSuccessRate)
	require.Equal(t, "unknown", evaluateHybridMinute(hybridTestRule(), HybridMonitorMinute{Source: "passive"}).Status)
}

func TestHybridStateTransitions(t *testing.T) {
	base := time.Date(2026, 9, 18, 1, 0, 0, 0, time.UTC)
	state := HybridMonitorState{}
	for index := 0; index < 3; index++ {
		var kind string
		state, kind = advanceHybridState(state, HybridMonitorMinute{Minute: base.Add(time.Duration(index) * time.Minute), Status: "failed"})
		if index < 2 {
			require.Empty(t, kind)
		} else {
			require.Equal(t, "incident", kind)
		}
	}
	repeated, kind := advanceHybridState(state, HybridMonitorMinute{Minute: state.Minute, Status: "failed"})
	require.Empty(t, kind)
	require.Equal(t, state, repeated)
	state, kind = advanceHybridState(state, HybridMonitorMinute{Minute: base.Add(3 * time.Minute), Status: "degraded"})
	require.True(t, state.Incident)
	require.Empty(t, kind)
	state, kind = advanceHybridState(state, HybridMonitorMinute{Minute: base.Add(4 * time.Minute), Status: "operational"})
	require.Empty(t, kind)
	state, kind = advanceHybridState(state, HybridMonitorMinute{Minute: base.Add(5 * time.Minute), Status: "operational"})
	require.Equal(t, "recovery", kind)
	require.False(t, state.Incident)
	state = HybridMonitorState{Minute: base, RedCount: 2}
	state, kind = advanceHybridState(state, HybridMonitorMinute{Minute: base.Add(2 * time.Minute), Status: "error"})
	require.Equal(t, 1, state.RedCount)
	require.Empty(t, kind)
	state, kind = advanceHybridState(state, HybridMonitorMinute{Minute: base.Add(3 * time.Minute), Status: "unknown"})
	require.Zero(t, state.RedCount)
	require.Empty(t, kind)
}

func TestHybridStreamingProbe(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"valid", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", 200, "operational"},
		{"metadata only", "data: {\"type\":\"message_start\"}\n\ndata: [DONE]\n\n", 200, "failed"},
		{"unterminated", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n", 200, "error"},
		{"stream error", "data: {\"error\":{\"message\":\"bad\"}}\n\n", 200, "error"},
		{"http failure", "", 503, "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				require.Equal(t, true, body["stream"])
				writer.Header().Set("Content-Type", "text/event-stream")
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			point := runHybridProbe(context.Background(), &ChannelMonitor{Provider: MonitorProviderOpenAI, Endpoint: server.URL, APIKey: "test", BodyOverrideMode: MonitorBodyOverrideModeReplace, BodyOverride: map[string]any{"messages": []map[string]string{{"role": "user", "content": "hi"}}}}, "model", server.Client())
			require.Equal(t, test.want, point.Status)
			if test.want == "operational" {
				require.NotNil(t, point.TTFTMs)
			}
		})
	}
}

func TestHybridWebhookValidation(t *testing.T) {
	for _, raw := range []string{"http://oapi.dingtalk.com/robot/send?access_token=x", "https://evil.test/robot/send?access_token=x", "https://oapi.dingtalk.com:444/robot/send?access_token=x", "https://oapi.dingtalk.com/robot/send"} {
		_, err := signedHybridWebhook(raw, "", time.Now())
		require.Error(t, err)
	}
	signed, err := signedHybridWebhook("https://oapi.dingtalk.com/robot/send?access_token=test", "secret", time.UnixMilli(12345))
	require.NoError(t, err)
	parsed, err := url.Parse(signed)
	require.NoError(t, err)
	require.Equal(t, "12345", parsed.Query().Get("timestamp"))
	require.NotEmpty(t, parsed.Query().Get("sign"))
}

func TestShouldScheduleHybridProbe(t *testing.T) {
	minute := time.Date(2026, time.September, 18, 1, 0, 0, 0, time.UTC)

	require.True(t, shouldScheduleHybridProbe(0, time.Time{}, minute))
	require.True(t, shouldScheduleHybridProbe(0, minute.Add(-time.Nanosecond), minute))
	require.False(t, shouldScheduleHybridProbe(1, minute.Add(-time.Minute), minute))
	require.False(t, shouldScheduleHybridProbe(0, minute, minute))
	require.False(t, shouldScheduleHybridProbe(0, minute.Add(20*time.Second), minute))
}

func TestHybridFirstOutputIgnoresMetadata(t *testing.T) {
	for _, input := range []string{`{"type":"message_start"}`, `{"type":"ping"}`, `{"choices":[{"delta":{"role":"assistant"}}]}`, `[DONE]`} {
		require.False(t, monitorStreamHasOutput(input))
	}
	for _, input := range []string{`{"type":"content_block_delta","delta":{"text":"hi"}}`, `{"type":"content_block_delta","delta":{"thinking":"reason"}}`, `{"choices":[{"delta":{"content":"hi"}}]}`} {
		require.True(t, monitorStreamHasOutput(input))
	}
}

func TestHybridConfigValidation(t *testing.T) {
	cfg := HybridMonitorConfig{Rules: []HybridMonitorRule{hybridTestRule()}}
	require.NoError(t, validateHybridConfig(&cfg))
	cfg.Rules[0].RedSuccess = .99
	require.Error(t, validateHybridConfig(&cfg))
	cfg.Rules = []HybridMonitorRule{hybridTestRule(), hybridTestRule()}
	cfg.Rules[1].ID = "other"
	require.Error(t, validateHybridConfig(&cfg))
}

func TestHybridPassiveCountsAndExclusions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	minute := time.Now().UTC().Truncate(time.Minute)
	mock.ExpectQuery("WITH usage_observations AS").WithArgs(int64(1), "model", int64(9), minute, minute.Add(time.Minute)).WillReturnRows(sqlmock.NewRows([]string{"success", "ttft", "owner", "status", "upstream_status", "error_type", "message"}).AddRow(true, 2000, "", 0, 0, "", "").AddRow(false, nil, "client", 401, 0, "", "invalid api key").AddRow(false, nil, "provider", 502, 401, "", "invalid api key"))
	svc := &HybridMonitorService{db: db}
	point, err := svc.passive(context.Background(), hybridTestRule(), 9, minute)
	require.NoError(t, err)
	require.EqualValues(t, 3, point.Requests)
	require.EqualValues(t, 1, point.Successes)
	require.EqualValues(t, 1, point.Excluded)
	require.EqualValues(t, 2000, *point.TTFTMs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHybridUserSnapshotPrivacy(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	rule := hybridTestRule()
	cfg := HybridMonitorConfig{Enabled: true, Rules: []HybridMonitorRule{rule}}
	raw, _ := json.Marshal(cfg)
	mock.ExpectQuery("SELECT version,config").WillReturnRows(sqlmock.NewRows([]string{"version", "config", "webhook", "secret"}).AddRow(1, raw, "ciphertext", "secret"))
	point := HybridMonitorMinute{Source: "passive", Requests: 99, Failures: 10, Detail: "private upstream", TTFTSamples: 89}
	encoded, _ := json.Marshal(point)
	mock.ExpectQuery("SELECT targets.rule_id, recent.result").WithArgs(pq.Array([]string{"test"})).WillReturnRows(sqlmock.NewRows([]string{"rule_id", "result"}).AddRow("test", encoded))
	service := &HybridMonitorService{db: db}
	public, items, err := service.Snapshot(context.Background(), false, []int64{1})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Nil(t, items[0].State)
	require.Zero(t, items[0].Rule.GroupID)
	require.Zero(t, items[0].Rule.MonitorID)
	require.Zero(t, items[0].Minutes[0].Requests)
	require.Empty(t, items[0].Minutes[0].Detail)
	require.Empty(t, public.Rules)
	output, _ := json.Marshal(items)
	require.False(t, strings.Contains(string(output), "private upstream"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHybridProbeUsesExecutionMinute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	before := time.Now().UTC().Truncate(time.Minute)
	point := runHybridProbe(context.Background(), &ChannelMonitor{Provider: MonitorProviderOpenAI, Endpoint: server.URL, APIKey: "test", BodyOverrideMode: MonitorBodyOverrideModeReplace, BodyOverride: map[string]any{"messages": []map[string]string{{"role": "user", "content": "hi"}}}}, "model", server.Client())
	require.Equal(t, before, point.Minute)
	require.False(t, point.Finalized)
}

func TestHybridBusinessActivityIsScopedByGroup(t *testing.T) {
	finish := BeginHybridBusinessRequest(101)
	inFlight, _ := HybridBusinessActivity(101)
	require.EqualValues(t, 1, inFlight)
	other, _ := HybridBusinessActivity(202)
	require.Zero(t, other)
	finish()
	inFlight, lastActivity := HybridBusinessActivity(101)
	require.Zero(t, inFlight)
	require.False(t, lastActivity.IsZero())
}
