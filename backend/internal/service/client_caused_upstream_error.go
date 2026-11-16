package service

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// 客户端原因导致的上游拒绝：问题出在客户自己的请求/会话上，而不是账号或上游。
// 这类错误是确定性的——同账号重试、切换账号都不会成功（只会把大请求体反复上传、
// 并让客户端把 502 当成临时故障继续重试），因此网关应立即停止，并用中英双语
// 告诉客户该怎么做。文案只描述原因与处理方式，绝不包含上游名称、域名、请求 ID
// 或上游自定义错误码，避免暴露渠道。

// ClaudeSessionBlockedClientMessage 是 Claude 拒答后会话被锁定时返回给客户的文案。
const ClaudeSessionBlockedClientMessage = "该对话已被 Claude 安全策略锁定（此前的请求触发了拒答），继续重试无效，请新开一个对话后再试 / " +
	"This conversation has been blocked by Claude's safety policy after a refusal. Retrying will not help; please start a new conversation."

// RequestBodyTooLargeClientMessage 是上游以 413 拒绝请求体大小时返回给客户的文案。
const RequestBodyTooLargeClientMessage = "请求体过大，超出允许的大小上限。请减少或压缩图片/附件、缩短上下文后重试 / " +
	"Request body is too large. Please reduce or compress images/attachments or shorten the context, then retry."

const (
	// ClientCausedKindClaudeSessionBlocked Claude 拒答后同一会话被锁定。
	ClientCausedKindClaudeSessionBlocked = "claude_session_blocked"
	// ClientCausedKindRequestBodyTooLarge 请求体超过上游大小上限。
	ClientCausedKindRequestBodyTooLarge = "request_body_too_large"
)

// claudeSessionBlockedErrorCode 是上游返回的错误码（仅用于识别，不回显给客户）。
const claudeSessionBlockedErrorCode = "claude_safety_session_blocked"

// ClientCausedUpstreamError 描述一次被识别为客户端原因的上游拒绝。
type ClientCausedUpstreamError struct {
	Kind string
	// StatusCode 返回给客户端的状态码（4xx：客户端 SDK 不会自动重试）。
	StatusCode int
	// ErrType Anthropic 风格的错误类型。
	ErrType string
	// Message 中英双语、不含上游信息的客户端文案。
	Message string
	// StopFailover 为 true 时不再同账号重试、也不再切换账号。
	StopFailover bool
}

// ClassifyClientCausedUpstreamError 判断上游错误是否由客户端请求本身导致。
func ClassifyClientCausedUpstreamError(statusCode int, body []byte) (ClientCausedUpstreamError, bool) {
	if isClaudeSessionBlockedError(statusCode, body) {
		return ClientCausedUpstreamError{
			Kind:         ClientCausedKindClaudeSessionBlocked,
			StatusCode:   http.StatusForbidden,
			ErrType:      "permission_error",
			Message:      ClaudeSessionBlockedClientMessage,
			StopFailover: true,
		}, true
	}
	if statusCode == http.StatusRequestEntityTooLarge {
		// 不同账号的大小上限可能不同（部分平台会据此切换账号），因此 413 只改写
		// 客户端文案，不改变既有的 failover 行为。
		return ClientCausedUpstreamError{
			Kind:       ClientCausedKindRequestBodyTooLarge,
			StatusCode: http.StatusRequestEntityTooLarge,
			ErrType:    "request_too_large",
			Message:    RequestBodyTooLargeClientMessage,
		}, true
	}
	return ClientCausedUpstreamError{}, false
}

// IsClientCausedTerminalUpstreamError 报告该错误是否应立即终止重试与 failover。
func IsClientCausedTerminalUpstreamError(statusCode int, body []byte) bool {
	cc, ok := ClassifyClientCausedUpstreamError(statusCode, body)
	return ok && cc.StopFailover
}

// IsClientCausedTerminal 报告 failover 错误是否由客户端原因导致且不应再重试/切换账号。
func (e *UpstreamFailoverError) IsClientCausedTerminal() bool {
	return e != nil && IsClientCausedTerminalUpstreamError(e.StatusCode, e.ResponseBody)
}

// peekClientCausedTerminal 读取并回填上游 4xx 错误体，报告其是否为客户端原因的终止性错误。
// 供服务层同账号重试循环在重试前短路使用（重试同一被锁会话只会重复上传请求体）。
func (s *GatewayService) peekClientCausedTerminal(resp *http.Response) bool {
	if resp == nil || resp.Body == nil || resp.StatusCode < 400 || resp.StatusCode >= 500 {
		return false
	}
	body, _ := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return IsClientCausedTerminalUpstreamError(resp.StatusCode, body)
}

func isClaudeSessionBlockedError(statusCode int, body []byte) bool {
	if statusCode < 400 || statusCode >= 500 || len(body) == 0 {
		return false
	}
	for _, path := range []string{"error.code", "code", "error.type"} {
		if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, path).String()), claudeSessionBlockedErrorCode) {
			return true
		}
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, claudeSessionBlockedErrorCode) ||
		strings.Contains(lower, "session is blocked after a claude refusal")
}
