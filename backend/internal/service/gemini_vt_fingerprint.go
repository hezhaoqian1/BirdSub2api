package service

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Gemini VT 被动检测：在真实客户流量里给每个 Gemini API Key 账号的回复打指纹，
// 发现混用 AI Studio 等非 Vertex 来源（主动体检每 15 分钟才几个请求，压满才兜底、
// 只分走一部分纯文本的上游很容易漏掉，2026-11-01 zzshu 就是这样）。
//
// 一条回复算"非 Vertex"的条件（任一）：
//   - 没有 responseId（Vertex 每个回复、每个流式分块都带；AI Studio 系没有）；
//   - thoughtSignature 不以 "AY" 开头（AI Studio 系以 "E" 开头）；
//   - 请求带了图片 / 视频，返回的分模态 token 明细里却没有 IMAGE / VIDEO（媒体被悄悄丢掉）。
//
// 性能：只对 Gemini API Key 账号的原生接口生效；每个分块只做定点的 gjson 读取
// （签名、token 明细先用 bytes.Contains 过滤），请求体只在含 inlineData/fileData 时才解析；
// 计数先攒在内存里按 (账号, 模型, 5 分钟桶) 合并，后台每分钟批量写库，请求路径上不碰数据库。

const (
	geminiFingerprintBucket     = 5 * time.Minute
	geminiFingerprintFlushEvery = time.Minute
	geminiFingerprintMaxKeys    = 5000
	geminiFingerprintRetention  = 7 * 24 * time.Hour
	geminiFingerprintCtxKey     = "gemini_vt_fingerprint"
)

var (
	geminiFingerprintSignatureKey = []byte("thoughtSignature")
	geminiFingerprintDetailsKey   = []byte("promptTokensDetails")
	geminiFingerprintMediaKeys    = [][]byte{[]byte("inlineData"), []byte("inline_data"), []byte("fileData"), []byte("file_data")}
)

// ---- 单个请求的指纹 ----

// geminiResponseFingerprint 一次回复（非流式一个 body，流式多个分块）的指纹。nil 安全。
type geminiResponseFingerprint struct {
	sawData     bool
	responseID  bool
	createTime  bool
	signature   string // 第一个 thoughtSignature 的前 6 个字符
	detailsSeen bool
	imageTokens int64
	videoTokens int64
}

// observe 读一个响应体或流式分块（已解包的 Gemini JSON）。
func (f *geminiResponseFingerprint) observe(raw []byte) {
	if f == nil || len(raw) == 0 {
		return
	}
	f.sawData = true
	if !f.responseID || !f.createTime {
		res := gjson.GetManyBytes(raw, "responseId", "createTime")
		if strings.TrimSpace(res[0].String()) != "" {
			f.responseID = true
		}
		if strings.TrimSpace(res[1].String()) != "" {
			f.createTime = true
		}
	}
	if f.signature == "" && bytes.Contains(raw, geminiFingerprintSignatureKey) {
		gjson.GetBytes(raw, "candidates.0.content.parts.#.thoughtSignature").ForEach(func(_, v gjson.Result) bool {
			if s := v.String(); s != "" {
				f.signature = truncateRunes(s, 6)
				return false
			}
			return true
		})
	}
	if bytes.Contains(raw, geminiFingerprintDetailsKey) {
		details := gjson.GetBytes(raw, "usageMetadata.promptTokensDetails")
		if details.IsArray() {
			// 流式时每个带 usage 的分块都是累计值，以最后一次为准。
			f.detailsSeen, f.imageTokens, f.videoTokens = true, 0, 0
			details.ForEach(func(_, v gjson.Result) bool {
				switch strings.ToUpper(v.Get("modality").String()) {
				case "IMAGE":
					f.imageTokens += v.Get("tokenCount").Int()
				case "VIDEO":
					f.videoTokens += v.Get("tokenCount").Int()
				}
				return true
			})
		}
	}
}

// geminiRequestMedia 请求里带了哪些媒体。
type geminiRequestMedia struct {
	image bool
	video bool
}

var geminiFingerprintMimeKeys = [][]byte{[]byte(`"mimeType"`), []byte(`"mime_type"`)}

// detectGeminiRequestMedia 只在请求体含 inlineData/fileData 时才看媒体类型，纯文本请求零成本。
// 不做 JSON 解析：请求体可能带几 MB 的 base64 图片，这里只用 bytes.Index 找 "mimeType" 键
// 再读紧跟的字符串值（base64 里没有引号；文本里的键会被转义成 \"mimeType\"，不会误中；
// responseMimeType 前面不是引号，也不会误中）。
func detectGeminiRequestMedia(body []byte) geminiRequestMedia {
	var m geminiRequestMedia
	hasMedia := false
	for _, key := range geminiFingerprintMediaKeys {
		if bytes.Contains(body, key) {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		return m
	}
	for _, key := range geminiFingerprintMimeKeys {
		rest := body
		for !m.image || !m.video {
			i := bytes.Index(rest, key)
			if i < 0 {
				break
			}
			rest = rest[i+len(key):]
			j := 0
			for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t' || rest[j] == '\r' || rest[j] == '\n' || rest[j] == ':') {
				j++
			}
			if j >= len(rest) || rest[j] != '"' {
				continue
			}
			value := rest[j+1:]
			if len(value) > 6 {
				value = value[:6]
			}
			switch strings.ToLower(string(value)) {
			case "image/":
				m.image = true
			case "video/":
				m.video = true
			}
		}
	}
	return m
}

// ---- 请求上下文 ----

// beginGeminiFingerprint 只给 Gemini API Key 账号挂观察器；记录器未启用时返回 nil（后续调用全部空操作）。
func beginGeminiFingerprint(c *gin.Context, account *Account) *geminiResponseFingerprint {
	if c == nil || account == nil || account.Platform != PlatformGemini || account.Type != AccountTypeAPIKey ||
		geminiFingerprints.Load() == nil {
		return nil
	}
	fp := &geminiResponseFingerprint{}
	c.Set(geminiFingerprintCtxKey, fp)
	return fp
}

func geminiFingerprintFromContext(c *gin.Context) *geminiResponseFingerprint {
	if c == nil {
		return nil
	}
	v, ok := c.Get(geminiFingerprintCtxKey)
	if !ok {
		return nil
	}
	fp, ok := v.(*geminiResponseFingerprint)
	if !ok {
		return nil
	}
	return fp
}

// finishGeminiFingerprint 请求成功结束时把这次回复计入记录器。
func finishGeminiFingerprint(fp *geminiResponseFingerprint, accountID int64, model string, requestBody []byte) {
	if fp == nil || !fp.sawData {
		return
	}
	rec := geminiFingerprints.Load()
	if rec == nil {
		return
	}
	rec.record(accountID, model, detectGeminiRequestMedia(requestBody), fp)
}

// ---- 记录器 ----

type geminiFingerprintKey struct {
	accountID int64
	model     string
	bucket    int64
}

// geminiFingerprintCounts 一个 (账号, 模型, 5 分钟) 桶的计数。
type geminiFingerprintCounts struct {
	Responses       int64
	NonVertex       int64
	NoResponseID    int64
	NoCreateTime    int64
	VertexSignature int64
	OtherSignature  int64
	MediaRequests   int64
	MediaDropped    int64
}

func (c *geminiFingerprintCounts) add(o geminiFingerprintCounts) {
	c.Responses += o.Responses
	c.NonVertex += o.NonVertex
	c.NoResponseID += o.NoResponseID
	c.NoCreateTime += o.NoCreateTime
	c.VertexSignature += o.VertexSignature
	c.OtherSignature += o.OtherSignature
	c.MediaRequests += o.MediaRequests
	c.MediaDropped += o.MediaDropped
}

// classifyGeminiFingerprint 把一次回复折算成计数。
func classifyGeminiFingerprint(media geminiRequestMedia, fp *geminiResponseFingerprint) geminiFingerprintCounts {
	c := geminiFingerprintCounts{Responses: 1}
	nonVertex := false
	if !fp.responseID {
		c.NoResponseID = 1
		nonVertex = true
	}
	if !fp.createTime {
		c.NoCreateTime = 1
	}
	if fp.signature != "" {
		if strings.HasPrefix(fp.signature, fidelityGeminiDefaultSignaturePrefix) {
			c.VertexSignature = 1
		} else {
			c.OtherSignature = 1
			nonVertex = true
		}
	}
	if media.image || media.video {
		c.MediaRequests = 1
		if fp.detailsSeen && ((media.image && fp.imageTokens == 0) || (media.video && fp.videoTokens == 0)) {
			c.MediaDropped = 1
			nonVertex = true
		}
	}
	if nonVertex {
		c.NonVertex = 1
	}
	return c
}

type geminiFingerprintRecorder struct {
	db      *sql.DB
	now     func() time.Time
	mu      sync.Mutex
	pending map[geminiFingerprintKey]*geminiFingerprintCounts
	dropped atomic.Int64
}

var geminiFingerprints atomic.Pointer[geminiFingerprintRecorder]

func newGeminiFingerprintRecorder(db *sql.DB) *geminiFingerprintRecorder {
	if db == nil {
		return nil
	}
	return &geminiFingerprintRecorder{db: db, now: time.Now, pending: map[geminiFingerprintKey]*geminiFingerprintCounts{}}
}

func (r *geminiFingerprintRecorder) record(accountID int64, model string, media geminiRequestMedia, fp *geminiResponseFingerprint) {
	model = truncateRunes(strings.TrimSpace(model), 100)
	key := geminiFingerprintKey{accountID: accountID, model: model, bucket: r.now().Truncate(geminiFingerprintBucket).Unix()}
	counts := classifyGeminiFingerprint(media, fp)
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.pending[key]
	if !ok {
		if len(r.pending) >= geminiFingerprintMaxKeys {
			r.dropped.Add(1)
			return
		}
		cur = &geminiFingerprintCounts{}
		r.pending[key] = cur
	}
	cur.add(counts)
}

// flush 把内存里的计数累加进数据库（失败的这批丢弃：只是监控数据）。
func (r *geminiFingerprintRecorder) flush(ctx context.Context) {
	r.mu.Lock()
	batch := r.pending
	r.pending = map[geminiFingerprintKey]*geminiFingerprintCounts{}
	r.mu.Unlock()
	if n := r.dropped.Swap(0); n > 0 {
		slog.Warn("[GeminiFingerprint] dropped observations, too many distinct keys", "count", n)
	}
	for key, c := range batch {
		if _, err := r.db.ExecContext(ctx, `INSERT INTO gemini_upstream_fingerprints(account_id, model, bucket_start,
 responses, non_vertex, no_response_id, no_create_time, vertex_signature, other_signature, media_requests, media_dropped)
 VALUES ($1, $2, to_timestamp($3), $4, $5, $6, $7, $8, $9, $10, $11)
 ON CONFLICT (account_id, model, bucket_start) DO UPDATE SET
 responses = gemini_upstream_fingerprints.responses + EXCLUDED.responses,
 non_vertex = gemini_upstream_fingerprints.non_vertex + EXCLUDED.non_vertex,
 no_response_id = gemini_upstream_fingerprints.no_response_id + EXCLUDED.no_response_id,
 no_create_time = gemini_upstream_fingerprints.no_create_time + EXCLUDED.no_create_time,
 vertex_signature = gemini_upstream_fingerprints.vertex_signature + EXCLUDED.vertex_signature,
 other_signature = gemini_upstream_fingerprints.other_signature + EXCLUDED.other_signature,
 media_requests = gemini_upstream_fingerprints.media_requests + EXCLUDED.media_requests,
 media_dropped = gemini_upstream_fingerprints.media_dropped + EXCLUDED.media_dropped`,
			key.accountID, key.model, key.bucket, c.Responses, c.NonVertex, c.NoResponseID, c.NoCreateTime,
			c.VertexSignature, c.OtherSignature, c.MediaRequests, c.MediaDropped); err != nil {
			slog.Warn("[GeminiFingerprint] flush failed", "account_id", key.accountID, "error", err)
		}
	}
}

// run 每分钟写一次库；停止时最后再写一次。
func (r *geminiFingerprintRecorder) run(stop <-chan struct{}) {
	ticker := time.NewTicker(geminiFingerprintFlushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			r.flush(ctx)
			cancel()
		case <-stop:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			r.flush(ctx)
			cancel()
			return
		}
	}
}
