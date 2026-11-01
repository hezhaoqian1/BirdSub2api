//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 2026-11-01 实测：zzshu 恢复后纯文本回复（AI Studio 型）与 Vertex 回复。
const (
	vertexStreamChunk1 = `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}],"modelVersion":"gemini-3.8-flash","createTime":"2026-11-01T13:20:10.100000Z","responseId":"Yyrnap74BqeaotQP38PU-AM"}`
	vertexStreamChunk2 = `{"candidates":[{"content":{"role":"model","parts":[{"text":"lo","thoughtSignature":"AY89a1+x"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":937,"promptTokensDetails":[{"modality":"VIDEO","tokenCount":660},{"modality":"TEXT","tokenCount":27},{"modality":"AUDIO","tokenCount":250}]},"modelVersion":"gemini-3.8-flash","createTime":"2026-11-01T13:20:10.100000Z","responseId":"Yyrnap74BqeaotQP38PU-AM"}`
	aiStudioPlainBody  = `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello! How can I help you today?"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":9,"trafficType":"ON_DEMAND"},"modelVersion":"gemini-3.8-flash","createTime":"2026-11-01T13:33:40.100000Z"}`
)

func withGeminiRecorder(t *testing.T, rec *geminiFingerprintRecorder) {
	t.Helper()
	prev := geminiFingerprints.Load()
	geminiFingerprints.Store(rec)
	t.Cleanup(func() { geminiFingerprints.Store(prev) })
}

func TestGeminiFingerprintClassifiesVertexAndAIStudio(t *testing.T) {
	vertex := &geminiResponseFingerprint{}
	vertex.observe([]byte(vertexStreamChunk1))
	vertex.observe([]byte(vertexStreamChunk2))
	c := classifyGeminiFingerprint(geminiRequestMedia{video: true}, vertex)
	require.Equal(t, geminiFingerprintCounts{Responses: 1, VertexSignature: 1, MediaRequests: 1}, c)

	as := &geminiResponseFingerprint{}
	as.observe([]byte(aiStudioPlainBody))
	c = classifyGeminiFingerprint(geminiRequestMedia{}, as)
	require.Equal(t, int64(1), c.NonVertex)
	require.Equal(t, int64(1), c.NoResponseID)

	eSig := &geminiResponseFingerprint{}
	eSig.observe([]byte(`{"responseId":"x","createTime":"y","candidates":[{"content":{"parts":[{"functionCall":{"name":"f"},"thoughtSignature":"EooDCocD"}]}}]}`))
	c = classifyGeminiFingerprint(geminiRequestMedia{}, eSig)
	require.Equal(t, geminiFingerprintCounts{Responses: 1, NonVertex: 1, OtherSignature: 1}, c)

	// 请求带了视频，回复的明细里没有 VIDEO：视频被悄悄丢掉。
	dropped := &geminiResponseFingerprint{}
	dropped.observe([]byte(`{"responseId":"x","createTime":"y","usageMetadata":{"promptTokensDetails":[{"modality":"TEXT","tokenCount":27}]}}`))
	c = classifyGeminiFingerprint(geminiRequestMedia{video: true}, dropped)
	require.Equal(t, int64(1), c.MediaDropped)
	require.Equal(t, int64(1), c.NonVertex)

	// 没有分模态明细时不判断媒体。
	noDetails := &geminiResponseFingerprint{}
	noDetails.observe([]byte(`{"responseId":"x","createTime":"y"}`))
	require.Zero(t, classifyGeminiFingerprint(geminiRequestMedia{image: true}, noDetails).NonVertex)
}

func TestDetectGeminiRequestMedia(t *testing.T) {
	require.Equal(t, geminiRequestMedia{}, detectGeminiRequestMedia([]byte(`{"contents":[{"parts":[{"text":"describe image/png please"}]}]}`)))
	require.Equal(t, geminiRequestMedia{image: true}, detectGeminiRequestMedia([]byte(
		`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}},{"text":"hi"}]}]}`)))
	require.Equal(t, geminiRequestMedia{video: true}, detectGeminiRequestMedia([]byte(
		`{"contents":[{"parts":[{"file_data":{"mime_type":"video/mp4","file_uri":"gs://b/v.mp4"}}]}]}`)))
	require.Equal(t, geminiRequestMedia{image: true, video: true}, detectGeminiRequestMedia([]byte(
		`{"contents":[{"parts":[{"fileData":{"mimeType":"video/mp4","fileUri":"gs://b/v.mp4"}}]},{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"AA"}}]}]}`)))
}

func TestGeminiForwardNativeRecordsFingerprints(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rec := newGeminiFingerprintRecorder(db)
	fixed := time.Date(2026, 11, 1, 13, 33, 0, 0, time.UTC)
	rec.now = func() time.Time { return fixed }
	withGeminiRecorder(t, rec)
	gin.SetMode(gin.TestMode)

	// 非流式：AI Studio 型回复（没有 responseId）。
	svc, _, _ := newGeminiCachedContentTestService(http.StatusOK, aiStudioPlainBody)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.8-flash:generateContent", nil)
	_, err = svc.ForwardNative(context.Background(), c, geminiDefaultPolicyAPIKeyAccount(), "gemini-3.8-flash", "generateContent", false,
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}`))
	require.NoError(t, err)

	// 流式：Vertex 回复，请求带了视频且回复计了 VIDEO token。
	svc, _, _ = newGeminiCachedContentTestService(http.StatusOK, "data: "+vertexStreamChunk1+"\n\ndata: "+vertexStreamChunk2+"\n\n")
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse", nil)
	_, err = svc.ForwardNative(context.Background(), c, geminiDefaultPolicyAPIKeyAccount(), "gemini-3.8-flash", "streamGenerateContent", true,
		[]byte(`{"contents":[{"role":"user","parts":[{"fileData":{"mimeType":"video/mp4","fileUri":"gs://b/v.mp4"}},{"text":"who?"}]}]}`))
	require.NoError(t, err)

	// 非 Gemini API Key 账号不记录。
	other := geminiDefaultPolicyAPIKeyAccount()
	other.Type = AccountTypeOAuth
	require.Nil(t, beginGeminiFingerprint(c, other))

	bucket := fixed.Truncate(geminiFingerprintBucket).Unix()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gemini_upstream_fingerprints")).
		WithArgs(int64(710), "gemini-3.8-flash", bucket, int64(2), int64(1), int64(1), int64(0), int64(1), int64(0), int64(1), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	rec.flush(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
	require.Empty(t, rec.pending)
}

func TestBeginGeminiFingerprintDisabledWithoutRecorder(t *testing.T) {
	withGeminiRecorder(t, nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, beginGeminiFingerprint(c, geminiDefaultPolicyAPIKeyAccount()))
	// nil 观察器的所有调用都是空操作。
	var fp *geminiResponseFingerprint
	fp.observe([]byte(`{}`))
	finishGeminiFingerprint(fp, 1, "m", nil)
}

func TestGeminiPassiveMixingThreshold(t *testing.T) {
	cfg := DefaultChannelFidelityConfig()
	require.False(t, geminiPassiveMixing(GeminiPassiveStats{Responses: 19, NonVertex: 19}, cfg))   // 样本不够
	require.False(t, geminiPassiveMixing(GeminiPassiveStats{Responses: 1000, NonVertex: 2}, cfg))  // 不到 3 条
	require.False(t, geminiPassiveMixing(GeminiPassiveStats{Responses: 1000, NonVertex: 40}, cfg)) // 4% < 5%
	require.True(t, geminiPassiveMixing(GeminiPassiveStats{Responses: 1000, NonVertex: 50}, cfg))
	require.True(t, geminiPassiveMixing(GeminiPassiveStats{Responses: 21, NonVertex: 21}, cfg)) // argon 15/15 那种
}

type passiveAccountRepoStub struct {
	fidelityAccountRepoStub
	paused []int64
}

func (r *passiveAccountRepoStub) SetSchedulable(_ context.Context, id int64, schedulable bool) error {
	if !schedulable {
		r.paused = append(r.paused, id)
	}
	return nil
}

func passiveTestAccounts() []Account {
	return []Account{
		{ID: 174, Name: "zzshu Gemini VT", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
		{ID: 145, Name: "月亮 Gemini VT", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
		{ID: 151, Name: "Insloves Gemini VT", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false},
		{ID: 146, Name: "coai Gemini Ultra", Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
	}
}

func fingerprintRows(bucket time.Time) *sqlmock.Rows {
	cols := []string{"account_id", "model", "bucket_start", "responses", "non_vertex", "no_response_id", "no_create_time",
		"vertex_signature", "other_signature", "media_requests", "media_dropped"}
	return sqlmock.NewRows(cols).
		AddRow(174, "gemini-3.8-flash", bucket, 300, 70, 70, 0, 200, 0, 30, 0).
		AddRow(174, "gemini-4-argon", bucket, 100, 100, 100, 0, 0, 0, 0, 0).
		AddRow(145, "gemini-3.8-flash", bucket, 500, 0, 0, 0, 480, 0, 60, 0).
		AddRow(151, "gemini-3.8-flash", bucket, 80, 80, 80, 0, 0, 0, 0, 0). // 已停用：只记录
		AddRow(146, "gemini-3.8-flash", bucket, 80, 80, 80, 0, 0, 0, 0, 0)  // 不是 VT 账号
}

func TestCheckGeminiPassivePausesMixingAccountAndAlerts(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, `{"enabled":true}`, "")
	now := time.Date(2026, 11, 1, 13, 40, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	repo := &passiveAccountRepoStub{fidelityAccountRepoStub: fidelityAccountRepoStub{accounts: passiveTestAccounts()}}
	svc.SetChannelFidelityProber(&channelFidelityProber{accounts: repo})

	mock.ExpectQuery(regexp.QuoteMeta("FROM gemini_upstream_fingerprints WHERE bucket_start >= $1")).
		WillReturnRows(fingerprintRows(now.Add(-5 * time.Minute)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject LIKE 'vtpassive:%'")).
		WillReturnRows(sqlmock.NewRows([]string{"subject", "value"}))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_state(subject, level, value, updated_at)")).
		WithArgs("vtpassive:174", float64(now.Unix())).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindGeminiVT, "critical", "Gemini VT 混用（真实流量）", bodyContains{
			"账号：zzshu Gemini VT（#174）",
			"真实流量（最近 10 分钟）：400 条回复里 170 条不是 Vertex（42%）",
			"缺 responseId 170 条",
			"gemini-3.8-flash 70/300，gemini-4-argon 100/100",
			"已自动暂停该账号调度",
		}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	svc.checkGeminiPassiveFingerprints(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, []int64{174}, repo.paused)
}

func TestCheckGeminiPassiveAfterReenableOnlyCountsNewTraffic(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, `{"enabled":true}`, "")
	now := time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	repo := &passiveAccountRepoStub{fidelityAccountRepoStub: fidelityAccountRepoStub{accounts: passiveTestAccounts()[:1]}}
	svc.SetChannelFidelityProber(&channelFidelityProber{accounts: repo})
	triggered := now.Add(-4 * time.Minute)

	// 管理员已重新开启 174；触发前那个桶是混用的，触发后的桶是干净的：不再暂停。
	cols := []string{"account_id", "model", "bucket_start", "responses", "non_vertex", "no_response_id", "no_create_time",
		"vertex_signature", "other_signature", "media_requests", "media_dropped"}
	mock.ExpectQuery(regexp.QuoteMeta("FROM gemini_upstream_fingerprints")).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow(174, "gemini-3.8-flash", now.Add(-10*time.Minute), 300, 300, 300, 0, 0, 0, 0, 0).
			AddRow(174, "gemini-3.8-flash", now.Add(-3*time.Minute), 50, 0, 0, 0, 50, 0, 0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject LIKE 'vtpassive:%'")).
		WillReturnRows(sqlmock.NewRows([]string{"subject", "value"}).AddRow("vtpassive:174", float64(triggered.Unix())))
	svc.checkGeminiPassiveFingerprints(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
	require.Empty(t, repo.paused)

	// 仍在暂停中的账号直接跳过。
	repo.accounts[0].Schedulable = false
	mock.ExpectQuery(regexp.QuoteMeta("FROM gemini_upstream_fingerprints")).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(174, "gemini-3.8-flash", now.Add(-3*time.Minute), 300, 300, 300, 0, 0, 0, 0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM security_alert_state WHERE subject LIKE 'vtpassive:%'")).
		WillReturnRows(sqlmock.NewRows([]string{"subject", "value"}).AddRow("vtpassive:174", float64(triggered.Unix())))
	svc.checkGeminiPassiveFingerprints(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
	require.Empty(t, repo.paused)
}

func TestAutoPauseConfirmedGeminiOnlyOnRealFailure(t *testing.T) {
	svc, _, _ := newUserWatchTestService(t, "", "")
	repo := &passiveAccountRepoStub{}
	svc.SetChannelFidelityProber(&channelFidelityProber{accounts: repo})
	cfg := DefaultChannelFidelityConfig()
	target := fidelityTarget{Account: &Account{ID: 174, Name: "zzshu Gemini VT", Schedulable: true}, Kind: fidelityKindGeminiVT}

	// 复测出错（不是真的不通过）：不暂停。
	got := svc.autoPauseConfirmedGemini(context.Background(), cfg, target, ChannelFidelityCheck{Status: fidelityStatusError})
	require.False(t, got.autoPaused)
	require.Empty(t, repo.paused)

	// DeepSeek 体检不受影响。
	ds := fidelityTarget{Account: &Account{ID: 135, Schedulable: true}}
	require.False(t, svc.autoPauseConfirmedGemini(context.Background(), cfg, ds, ChannelFidelityCheck{Status: fidelityStatusFail}).autoPaused)

	got = svc.autoPauseConfirmedGemini(context.Background(), cfg, target, ChannelFidelityCheck{Status: fidelityStatusFail})
	require.True(t, got.autoPaused)
	require.Equal(t, []int64{174}, repo.paused)
	require.Contains(t, fidelityAlertDraft(got, nil, ChannelFidelityCheck{CheckedAt: time.Now(), Status: fidelityStatusFail, MetaCheck: fidelityCheckFail}, time.UTC).body(),
		"已自动暂停该账号调度")

	cfg.GeminiAutoPause = false
	target.Account.Schedulable = true
	require.False(t, svc.autoPauseConfirmedGemini(context.Background(), cfg, target, ChannelFidelityCheck{Status: fidelityStatusFail}).autoPaused)
}

// 性能：每个流式分块多出来的开销（典型分块与带 usage 的末块）。
func BenchmarkGeminiFingerprintObserveChunk(b *testing.B) {
	chunk1, chunk2 := []byte(vertexStreamChunk1), []byte(vertexStreamChunk2)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		fp := &geminiResponseFingerprint{}
		fp.observe(chunk1)
		fp.observe(chunk2)
	}
}

// 性能：纯文本请求体不解析；带 1MB 内联图片的请求体只扫一次。
func BenchmarkDetectGeminiRequestMedia(b *testing.B) {
	plain := []byte(`{"contents":[{"role":"user","parts":[{"text":"` + strings.Repeat("hello ", 2000) + `"}]}]}`)
	image := []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` + strings.Repeat("A", 1<<20) + `"}},{"text":"hi"}]}]}`)
	b.Run("plain_12KB", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			detectGeminiRequestMedia(plain)
		}
	})
	b.Run("inline_image_1MB", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			detectGeminiRequestMedia(image)
		}
	})
}
