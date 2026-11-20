package service

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// 上游以 413 拒绝请求体大小时换号（Claude / Anthropic 平台各转发路径共用）。
//
// 各渠道的请求体上限不同：Anthropic 官方 Messages 为 128MB，部分转发站与托管平台为
// 32MiB，Bedrock 为 20MB。一个渠道以「请求体过大」拒绝的请求，换到上限更高的渠道往往
// 就能成功（例如 32MiB 上限的渠道拒绝的 33–35MiB 请求，上限 128MB 的渠道可以正常处理）。
//
// 这是请求本身的属性、与账号健康无关：
//   - 不在同账号重试：同一请求体必然再次被拒，还会重复上传几十 MB；
//   - 不触发限流 / 临时不可调度 / 自定义错误码等账号副作用；
//   - 上下文超长（prompt is too long 等）导致的 413 换号无用，保持原有的直接返回。
//
// 所有账号都失败时，各 exhausted 处理器经 ClassifyClientCausedUpstreamError 回中英双语的
// 「请求体过大」提示（若最后一个渠道是 429/5xx 等临时错误，则按该临时错误返回）。

// isAnthropicRequestBodyTooLargeError 判断上游错误是否为「请求体超过该渠道的大小上限」。
func isAnthropicRequestBodyTooLargeError(statusCode int, body []byte) bool {
	if statusCode != http.StatusRequestEntityTooLarge {
		return false
	}
	msg := extractUpstreamErrorMessage(body)
	if isOpenAIContextWindowError(msg, body) {
		return false
	}
	return !strings.Contains(strings.ToLower(msg), "prompt is too long")
}

// IsRequestBodyTooLarge 报告该 failover 错误是否为上游拒绝请求体大小（非上下文超长）。
// 返回该错误的账号在同一请求内不应再被选中（同一请求体会被再次拒绝）。
func (e *UpstreamFailoverError) IsRequestBodyTooLarge() bool {
	return e != nil && isAnthropicRequestBodyTooLargeError(e.StatusCode, e.ResponseBody)
}

// requestBodyTooLargeFailover 把「请求体超过该账号上游上限」的 413 转成换号错误；
// 不是这种情况时返回 nil。body 为已读取的上游错误体。
func (s *GatewayService) requestBodyTooLargeFailover(c *gin.Context, account *Account, resp *http.Response, body []byte, passthrough bool) *UpstreamFailoverError {
	if resp == nil || account == nil || !isAnthropicRequestBodyTooLargeError(resp.StatusCode, body) {
		return nil
	}
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	logger.LegacyPrintf("service.gateway", "[Forward] Upstream rejected request body size (413), failover to next account: Account=%d(%s) RequestID=%s Message=%s",
		account.ID, account.Name, resp.Header.Get("x-request-id"), truncateString(upstreamMsg, 300))
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Passthrough:        passthrough,
		Kind:               "request_body_too_large_failover",
		Message:            upstreamMsg,
		Detail: func() string {
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				return truncateString(string(body), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
			}
			return ""
		}(),
	})
	return &UpstreamFailoverError{
		StatusCode:   resp.StatusCode,
		ResponseBody: body,
	}
}

// peekRequestBodyTooLargeFailover 读取并回填上游 413 错误体后调用 requestBodyTooLargeFailover，
// 使不换号时后续错误处理仍能读到完整响应体。
func (s *GatewayService) peekRequestBodyTooLargeFailover(c *gin.Context, account *Account, resp *http.Response, passthrough bool) *UpstreamFailoverError {
	if resp == nil || resp.Body == nil || resp.StatusCode != http.StatusRequestEntityTooLarge {
		return nil
	}
	body, _ := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return s.requestBodyTooLargeFailover(c, account, resp, body, passthrough)
}
