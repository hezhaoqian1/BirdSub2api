package service

import (
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 账号级默认推理强度。
//
// 部分第三方上游在请求未携带 reasoning_effort 时按偏低的默认推理深度处理（实测同一
// DeepSeek 模型在不同上游的默认推理量可差 3 倍以上）。管理员可在账号
// credentials.default_reasoning_effort 设置一个默认值，网关仅在客户端未显式指定
// effort 且未关闭 thinking 时注入；客户端自己的选择始终优先。
const credKeyDefaultReasoningEffort = "default_reasoning_effort"

var accountDefaultReasoningEffortValues = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"max":    {},
}

// normalizeAccountDefaultReasoningEffort 返回规范化后的取值；空串表示未开启。
func normalizeAccountDefaultReasoningEffort(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", true
	}
	if _, ok := accountDefaultReasoningEffortValues[value]; !ok {
		return "", false
	}
	return value, true
}

// GetDefaultReasoningEffort 返回账号配置的默认推理强度；未配置或非法时返回空串。
func (a *Account) GetDefaultReasoningEffort() string {
	if a == nil || a.Credentials == nil {
		return ""
	}
	raw, _ := a.Credentials[credKeyDefaultReasoningEffort].(string)
	value, ok := normalizeAccountDefaultReasoningEffort(raw)
	if !ok {
		return ""
	}
	return value
}

// NormalizeDefaultReasoningEffortCredentials 校验并规范化 credentials 中的默认推理强度。
func NormalizeDefaultReasoningEffortCredentials(credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	raw, ok := credentials[credKeyDefaultReasoningEffort]
	if !ok || raw == nil {
		return nil
	}
	str, isString := raw.(string)
	if !isString {
		return infraerrors.New(http.StatusBadRequest, "INVALID_DEFAULT_REASONING_EFFORT",
			"default_reasoning_effort must be a string")
	}
	value, valid := normalizeAccountDefaultReasoningEffort(str)
	if !valid {
		return infraerrors.New(http.StatusBadRequest, "INVALID_DEFAULT_REASONING_EFFORT",
			"default_reasoning_effort must be one of: low, medium, high, max")
	}
	credentials[credKeyDefaultReasoningEffort] = value
	return nil
}

// applyAccountDefaultReasoningEffort 在客户端未指定推理强度时注入账号默认值。
// 显式的 reasoning_effort / reasoning.effort / output_config.effort（含 "none"）
// 以及 thinking.type=disabled 都视为客户端已做出选择，保持原样。
func applyAccountDefaultReasoningEffort(body []byte, account *Account) ([]byte, bool) {
	effort := account.GetDefaultReasoningEffort()
	if effort == "" {
		return body, false
	}
	if explicitRequestedReasoningEffortFromBody(body) != "" {
		return body, false
	}
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()), "disabled") {
		return body, false
	}
	updated, err := sjson.SetBytes(body, "reasoning_effort", effort)
	if err != nil {
		return body, false
	}
	return updated, true
}
