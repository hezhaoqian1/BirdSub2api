package service

// 本文件实现 Anthropic API Key 透传分支的「非流式 → 强制上游流式 → 聚合回完整
// Messages JSON」路径（forced upstream streaming + aggregate）。
//
// 背景：部分上游中转对非流式（stream:false）请求采用「缓冲式」处理——先在服务端
// 把整段 Claude 响应生成完，再一次性返回。这类中转又常挂在 Cloudflare 后面，其
// origin 读超时约 120s；当单次生成耗时超过该阈值时，Cloudflare 在整个窗口内从
// origin 收不到任何字节，直接返回 524（origin_response_timeout）。
//
// 规避办法：客户端即便发的是非流式请求，网关也以 stream:true 向上游发起，边收边
// 读 SSE（链路全程有数据流动，到不了 120s 空闲），读完后把 SSE 事件重新拼成一个
// 与原生非流式等价的 Messages JSON，再整体（非流式）返回给客户端。客户端无感。
//
// 与 gateway_forward_as_responses.go 的缓冲聚合不同，这里按原样保留上游 JSON 的
// 全部字段（citations、container、service_tier 等），透传场景需要字段级保真。
//
// 失败语义：聚合完成前客户端尚未收到任何字节，因此流中断 / 数据间隔超时 /
// 上游 error 事件都可以像非流式一样 failover 到其它账号（不计部分 usage，与原
// 非流式路径一致）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// responseIsEventStream 判断上游响应是否为 SSE（text/event-stream）。
// 某些上游会忽略请求体里的 stream:true 而仍返回 JSON，此时应回退到普通非流式处理。
func responseIsEventStream(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

// anthropicStreamEventError 表示上游在 HTTP 200 之后通过 SSE `error` 事件报告的错误。
// StatusCode 由 error.type 映射而来，Body 为 Anthropic 错误 JSON，便于按普通 HTTP
// 错误走 failover / 账号冷却 / 错误透传逻辑。
type anthropicStreamEventError struct {
	StatusCode int
	Body       []byte
}

func (e *anthropicStreamEventError) Error() string {
	return fmt.Sprintf("anthropic upstream stream error event: status=%d body=%s", e.StatusCode, truncateString(string(e.Body), 500))
}

// response 构造一个等价的上游错误响应（每次调用返回独立可读的 Body）。
func (e *anthropicStreamEventError) response(upstreamHeader http.Header) *http.Response {
	header := http.Header{}
	if upstreamHeader != nil {
		header = upstreamHeader.Clone()
	}
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: e.StatusCode,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(e.Body)),
	}
}

// anthropicErrorTypeStatus 把 Anthropic error.type 映射为对应的 HTTP 状态码。
func anthropicErrorTypeStatus(errType string) int {
	switch errType {
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error":
		return http.StatusForbidden
	case "not_found_error":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "overloaded_error":
		return 529
	case "timeout_error":
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// errAnthropicStreamIncomplete 表示上游流在终止事件之前结束（断流 / 读错误）。
var errAnthropicStreamIncomplete = errors.New("anthropic stream aggregation: stream ended before terminal event")

// errAnthropicStreamIdleTimeout 表示上游流超过数据间隔超时没有任何数据。
var errAnthropicStreamIdleTimeout = errors.New("anthropic stream aggregation: stream data interval timeout")

// assembledBlock 是一个 content block 的增量构建状态。字符串字段用 Builder 累积，
// 避免逐个 delta `+` 拼接带来的 O(n^2) 复制。
type assembledBlock struct {
	fields    map[string]any
	text      strings.Builder
	thinking  strings.Builder
	signature strings.Builder
	inputJSON strings.Builder
	hasText   bool
	hasThink  bool
	hasSig    bool
	hasInput  bool
}

func newAssembledBlock(fields map[string]any) *assembledBlock {
	if fields == nil {
		fields = map[string]any{}
	}
	b := &assembledBlock{fields: fields}
	// content_block_start 里的初始值（通常为空串）作为累积的前缀。
	if s, ok := fields["text"].(string); ok {
		b.text.WriteString(s)
		b.hasText = true
	}
	if s, ok := fields["thinking"].(string); ok {
		b.thinking.WriteString(s)
		b.hasThink = true
	}
	if s, ok := fields["signature"].(string); ok {
		b.signature.WriteString(s)
		b.hasSig = true
	}
	return b
}

// finalize 把累积的字符串写回 fields 并返回最终 block。
func (b *assembledBlock) finalize() map[string]any {
	if b.hasText {
		b.fields["text"] = b.text.String()
	}
	if b.hasThink {
		b.fields["thinking"] = b.thinking.String()
	}
	if b.hasSig {
		b.fields["signature"] = b.signature.String()
	}
	if b.hasInput {
		raw := b.inputJSON.String()
		if strings.TrimSpace(raw) != "" {
			var parsed any
			if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
				b.fields["input"] = parsed
			} else {
				// 解析失败时退化为原始字符串，避免丢失内容。
				b.fields["input"] = raw
			}
		}
	}
	// tool_use/server_tool_use 无参数时上游可能不下发任何 input_json_delta。
	if t, _ := b.fields["type"].(string); (t == "tool_use" || t == "server_tool_use") && b.fields["input"] == nil {
		b.fields["input"] = map[string]any{}
	}
	return b.fields
}

// anthropicStreamAssembler 增量重建一个 Anthropic Messages 对象。
type anthropicStreamAssembler struct {
	message     map[string]any // 来自 message_start.message 的骨架
	blocks      map[int]*assembledBlock
	maxIndex    int
	sawTerminal bool
	streamErr   *anthropicStreamEventError
	// contentBytes 是累积到 block 中的字符串总字节数，用于限制聚合内存。
	contentBytes int64
	lastEvent    string
}

func newAnthropicStreamAssembler() *anthropicStreamAssembler {
	return &anthropicStreamAssembler{
		blocks:   make(map[int]*assembledBlock),
		maxIndex: -1,
	}
}

// ingestLine 处理一行 SSE 文本（event:/data:/注释/空行）。
func (a *anthropicStreamAssembler) ingestLine(line string) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "event:") {
		a.lastEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		if anthropicStreamEventIsTerminal(a.lastEvent, "") {
			a.sawTerminal = true
		}
		return
	}
	data, ok := extractAnthropicSSEDataLine(line)
	if !ok {
		return
	}
	a.ingestData(data)
}

func (a *anthropicStreamAssembler) block(idx int, delta gjson.Result) *assembledBlock {
	if idx > a.maxIndex {
		a.maxIndex = idx
	}
	b := a.blocks[idx]
	if b == nil {
		// 上游漏发 content_block_start：按 delta 类型推断 block type，
		// 避免产出缺少 type 的 block 导致 SDK 反序列化失败。
		fields := map[string]any{}
		switch delta.Get("type").String() {
		case "text_delta", "citations_delta":
			fields["type"] = "text"
		case "thinking_delta", "signature_delta":
			fields["type"] = "thinking"
		}
		b = newAssembledBlock(fields)
		a.blocks[idx] = b
	}
	return b
}

// ingestData 处理单个 SSE data 行的 JSON 负载。
func (a *anthropicStreamAssembler) ingestData(data string) {
	trimmed := strings.TrimSpace(data)
	if anthropicStreamEventIsTerminal("", trimmed) {
		a.sawTerminal = true
	}
	if trimmed == "" || trimmed == "[DONE]" {
		return
	}
	parsed := gjson.Parse(trimmed)
	eventType := parsed.Get("type").String()
	if eventType == "" {
		eventType = a.lastEvent
	}
	switch eventType {
	case "message_start":
		if msg := parsed.Get("message"); msg.Exists() {
			m := map[string]any{}
			if err := json.Unmarshal([]byte(msg.Raw), &m); err == nil {
				a.message = m
			}
		}
	case "content_block_start":
		idx := int(parsed.Get("index").Int())
		fields := map[string]any{}
		if cb := parsed.Get("content_block"); cb.Exists() {
			_ = json.Unmarshal([]byte(cb.Raw), &fields)
		}
		a.blocks[idx] = newAssembledBlock(fields)
		if idx > a.maxIndex {
			a.maxIndex = idx
		}
	case "content_block_delta":
		delta := parsed.Get("delta")
		b := a.block(int(parsed.Get("index").Int()), delta)
		switch delta.Get("type").String() {
		case "text_delta":
			s := delta.Get("text").String()
			b.text.WriteString(s)
			b.hasText = true
			a.contentBytes += int64(len(s))
		case "thinking_delta":
			s := delta.Get("thinking").String()
			b.thinking.WriteString(s)
			b.hasThink = true
			a.contentBytes += int64(len(s))
		case "signature_delta":
			s := delta.Get("signature").String()
			b.signature.WriteString(s)
			b.hasSig = true
			a.contentBytes += int64(len(s))
		case "input_json_delta":
			s := delta.Get("partial_json").String()
			b.inputJSON.WriteString(s)
			b.hasInput = true
			a.contentBytes += int64(len(s))
		case "citations_delta":
			if cit := delta.Get("citation"); cit.Exists() {
				var citVal any
				if err := json.Unmarshal([]byte(cit.Raw), &citVal); err == nil {
					arr, _ := b.fields["citations"].([]any)
					b.fields["citations"] = append(arr, citVal)
					a.contentBytes += int64(len(cit.Raw))
				}
			}
		}
	case "message_delta":
		if a.message == nil {
			return
		}
		if d := parsed.Get("delta"); d.Exists() {
			d.ForEach(func(key, value gjson.Result) bool {
				a.message[key.String()] = jsonValue(value)
				return true
			})
		}
		if u := parsed.Get("usage"); u.Exists() {
			usage, _ := a.message["usage"].(map[string]any)
			if usage == nil {
				usage = map[string]any{}
				a.message["usage"] = usage
			}
			mergeAnthropicUsageMap(usage, u)
		}
	case "error":
		errType := parsed.Get("error.type").String()
		body := []byte(trimmed)
		if !parsed.Get("type").Exists() {
			// 只有 event: error 行、data 是裸 error 对象时补齐为标准错误信封。
			if wrapped, err := json.Marshal(map[string]any{"type": "error", "error": jsonValue(parsed)}); err == nil {
				body = wrapped
			}
			errType = parsed.Get("type").String()
		}
		a.streamErr = &anthropicStreamEventError{
			StatusCode: anthropicErrorTypeStatus(errType),
			Body:       body,
		}
	}
}

// mergeAnthropicUsageMap 把 message_delta.usage 合并进骨架 usage。
// 与 parseSSEUsagePassthrough 的计费口径保持一致：数值 0 不覆盖已有的非零值
// （GLM/Kimi 等上游会在 message_delta 里下发 input_tokens:0），嵌套对象递归合并。
func mergeAnthropicUsageMap(dst map[string]any, src gjson.Result) {
	src.ForEach(func(key, value gjson.Result) bool {
		k := key.String()
		switch {
		case value.Type == gjson.Number && value.Num == 0:
			if _, exists := dst[k]; !exists {
				dst[k] = jsonValue(value)
			}
		case value.IsObject():
			nested, _ := dst[k].(map[string]any)
			if nested == nil {
				dst[k] = jsonValue(value)
			} else {
				mergeAnthropicUsageMap(nested, value)
			}
		default:
			dst[k] = jsonValue(value)
		}
		return true
	})
}

// build 产出最终的 Messages JSON。
func (a *anthropicStreamAssembler) build() ([]byte, error) {
	if a.message == nil {
		return nil, errors.New("anthropic stream aggregation: missing message_start")
	}
	content := make([]any, 0, a.maxIndex+1)
	for i := 0; i <= a.maxIndex; i++ {
		if b, ok := a.blocks[i]; ok {
			content = append(content, b.finalize())
		}
	}
	a.message["content"] = content
	return json.Marshal(a.message)
}

// jsonValue 把 gjson.Result 转成可被 encoding/json 复用的 Go 值。
func jsonValue(r gjson.Result) any {
	var v any
	if err := json.Unmarshal([]byte(r.Raw), &v); err != nil {
		return r.Value()
	}
	return v
}

// anthropicStreamAggregateOptions 控制聚合的资源边界。
type anthropicStreamAggregateOptions struct {
	// MaxLineSize 为单行 SSE 上限（<=0 使用默认值）。
	MaxLineSize int
	// MaxContentBytes 为累积内容总字节上限（<=0 不限制）。
	MaxContentBytes int64
	// IdleTimeout 为两次读到数据之间的最长间隔（<=0 不限制）。
	IdleTimeout time.Duration
}

// AssembleAnthropicStreamToMessage 消费一个 Anthropic /v1/messages 的 SSE 流，
// 重建出等价的非流式 Messages JSON，并解析出 usage（供计费）。
//
// 返回的错误：
//   - *anthropicStreamEventError：上游通过 SSE error 事件报错；
//   - errAnthropicStreamIncomplete：终止事件前断流 / 读错误；
//   - errAnthropicStreamIdleTimeout：超过数据间隔超时；
//   - ErrUpstreamResponseBodyTooLarge：累积内容超过上限。
func AssembleAnthropicStreamToMessage(r io.Reader, opts anthropicStreamAggregateOptions) ([]byte, *ClaudeUsage, error) {
	maxLineSize := opts.MaxLineSize
	if maxLineSize <= 0 {
		maxLineSize = defaultMaxLineSize
	}
	assembler := newAnthropicStreamAssembler()
	usage := &ClaudeUsage{}

	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
		for scanner.Scan() {
			select {
			case events <- scanEvent{line: scanner.Text()}:
			case <-done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case events <- scanEvent{err: err}:
			case <-done:
			}
		}
	}()

	var idleTimer *time.Timer
	var idleCh <-chan time.Time
	if opts.IdleTimeout > 0 {
		idleTimer = time.NewTimer(opts.IdleTimeout)
		defer idleTimer.Stop()
		idleCh = idleTimer.C
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if assembler.streamErr != nil {
					return nil, usage, assembler.streamErr
				}
				if !assembler.sawTerminal {
					return nil, usage, errAnthropicStreamIncomplete
				}
				message, err := assembler.build()
				if err != nil {
					return nil, usage, fmt.Errorf("%w: %v", errAnthropicStreamIncomplete, err)
				}
				return message, usage, nil
			}
			if ev.err != nil {
				if assembler.streamErr != nil {
					return nil, usage, assembler.streamErr
				}
				if errors.Is(ev.err, bufio.ErrTooLong) {
					return nil, usage, ErrUpstreamResponseBodyTooLarge
				}
				return nil, usage, fmt.Errorf("%w: %v", errAnthropicStreamIncomplete, ev.err)
			}
			if idleTimer != nil {
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(opts.IdleTimeout)
			}
			assembler.ingestLine(ev.line)
			if data, ok := extractAnthropicSSEDataLine(ev.line); ok {
				parseSSEUsagePassthrough(data, usage)
			}
			if opts.MaxContentBytes > 0 && assembler.contentBytes > opts.MaxContentBytes {
				return nil, usage, ErrUpstreamResponseBodyTooLarge
			}
		case <-idleCh:
			return nil, usage, errAnthropicStreamIdleTimeout
		}
	}
}

// handleForcedUpstreamStreamAggregationAnthropicAPIKeyPassthrough 读取（强制）流式
// 上游响应、聚合成完整 JSON，并以非流式形式返回给客户端。
//
// 聚合失败时客户端尚未收到任何字节：断流 / 间隔超时返回 UpstreamFailoverError
// 以便切换账号；上游 SSE error 事件以 *anthropicStreamEventError 返回，由调用方按
// 普通上游 HTTP 错误处理；内容超限直接向客户端写 Anthropic 格式错误。
func (s *GatewayService) handleForcedUpstreamStreamAggregationAnthropicAPIKeyPassthrough(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	model string,
) (*ClaudeUsage, error) {
	if s.rateLimitService != nil {
		s.rateLimitService.UpdateSessionWindow(ctx, account, resp.Header)
	}

	opts := anthropicStreamAggregateOptions{
		MaxContentBytes: resolveUpstreamResponseReadLimit(s.cfg),
	}
	if s.cfg != nil {
		opts.MaxLineSize = s.cfg.Gateway.MaxLineSize
		if s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
			opts.IdleTimeout = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
		}
	}

	body, usage, err := AssembleAnthropicStreamToMessage(resp.Body, opts)
	if err != nil {
		var evErr *anthropicStreamEventError
		switch {
		case errors.As(err, &evErr):
			return nil, evErr
		case errors.Is(err, ErrUpstreamResponseBodyTooLarge):
			setOpsUpstreamError(c, http.StatusBadGateway, "upstream response too large", "")
			anthropicTooLargeError(c)
			return nil, err
		case errors.Is(err, errAnthropicStreamIdleTimeout):
			logger.LegacyPrintf("service.gateway",
				"[Anthropic passthrough] forced upstream stream data interval timeout: account=%d(%s) model=%s interval=%s",
				account.ID, account.Name, model, opts.IdleTimeout)
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, model)
			}
			return nil, forcedStreamFailoverError(account, resp, err, nil)
		default:
			logger.LegacyPrintf("service.gateway",
				"[Anthropic passthrough] forced upstream stream aggregation failed, failover: account=%d(%s) error=%v",
				account.ID, account.Name, err)
			return nil, forcedStreamFailoverError(account, resp, err, func(statusCode int, respBody []byte) {
				if s.rateLimitService != nil {
					s.rateLimitService.HandleUpstreamError(ctx, account, statusCode, resp.Header, respBody, model)
				}
			})
		}
	}

	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	observer.ObserveAnthropic(body)

	if IsForceCacheBilling(ctx) && usage.InputTokens > 0 {
		body, err = classifyAnthropicResponseInputAsCacheRead(body, usage)
		if err != nil {
			return nil, err
		}
	}

	writeAnthropicPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	// 返回给客户端的是聚合后的完整 JSON，必须显式覆盖上面复制过来的上游
	// text/event-stream：gin 的 c.Data 在 Content-Type 已存在时不会改写它。
	c.Header("Content-Type", "application/json")
	body = reverseToolNamesIfPresent(c, body)
	c.Data(http.StatusOK, "application/json", body)
	return usage, nil
}

// forcedStreamFailoverError 把聚合阶段的断流 / 超时归一为 502 failover 错误。
// notify 非 nil 时用于触发账号侧错误记账（间隔超时已由 HandleStreamTimeout 处理）。
func forcedStreamFailoverError(account *Account, resp *http.Response, cause error, notify func(int, []byte)) error {
	const statusCode = http.StatusBadGateway
	respBody, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "upstream_error",
			"message": cause.Error(),
		},
	})
	if notify != nil {
		notify(statusCode, respBody)
	}
	retryable := false
	if account != nil {
		retryable = account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode)
	}
	var header http.Header
	if resp != nil {
		header = resp.Header
	}
	return &UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           respBody,
		ResponseHeaders:        header,
		RetryableOnSameAccount: retryable,
	}
}

// handleForcedStreamEventErrorAnthropicAPIKeyPassthrough 把聚合阶段收到的上游 SSE
// error 事件按普通上游 HTTP 错误处理：可 failover 的状态（429/529/5xx 等）触发账号
// 冷却并切换账号；其余错误以原始 Anthropic 错误体透传给客户端。
func (s *GatewayService) handleForcedStreamEventErrorAnthropicAPIKeyPassthrough(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	upstreamResp *http.Response,
	evErr *anthropicStreamEventError,
	model string,
) (*ForwardResult, error) {
	requestID := ""
	if upstreamResp != nil {
		requestID = upstreamResp.Header.Get("x-request-id")
	}
	logger.LegacyPrintf("service.gateway", "[Anthropic Passthrough] Upstream stream error event during forced aggregation: Account=%d(%s) Status=%d RequestID=%s Body=%s",
		account.ID, account.Name, evErr.StatusCode, requestID, truncateString(string(evErr.Body), 1000))

	var upstreamHeader http.Header
	if upstreamResp != nil {
		upstreamHeader = upstreamResp.Header
	}
	if !s.shouldFailoverUpstreamError(evErr.StatusCode) {
		return s.handleErrorResponse(ctx, evErr.response(upstreamHeader), c, account, model)
	}

	s.handleFailoverSideEffects(ctx, evErr.response(upstreamHeader), account, model)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: evErr.StatusCode,
		UpstreamRequestID:  requestID,
		Passthrough:        true,
		Kind:               "failover",
		Message:            extractUpstreamErrorMessage(evErr.Body),
		Detail: func() string {
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				return truncateString(string(evErr.Body), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
			}
			return ""
		}(),
	})
	return nil, &UpstreamFailoverError{
		StatusCode:             evErr.StatusCode,
		ResponseBody:           evErr.Body,
		ResponseHeaders:        upstreamHeader,
		RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(evErr.StatusCode),
	}
}
