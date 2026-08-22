// Package openai 把 OpenAI Chat Completions 协议适配成 po.Model。
//
// 这个包只依赖 package po 和标准库。它是 Provider Adapter：
// Agent Core 继续保持厂商无关，而这里负责 HTTP、OpenAI-compatible JSON、
// SSE、finish_reason 映射、Tool Call 增量解析以及 Provider Error 分类。
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/internal/sse"
	"github.com/lemonzjj/po-agent-go/provider"
)

const chatCompletionsPath = "/chat/completions"

// Config 描述一个具体的 OpenAI-compatible 模型端点。
// “OpenAI-compatible”只说明网络协议大体兼容，并不保证 Tool Parser、Reasoning、
// Context Window 或 Parallel Tool Call 等能力都可用。真正能力由模型权重、Serving
// Runtime、Endpoint 配置和 Po Adapter 共同决定，因此必须显式声明能力画像。
type Config struct {
	// 留空时默认为 "openai-compatible"。
	Provider string

	// https://example.com/v1
	BaseURL string

	APIKey string

	Model string

	Capabilities po.ModelCapabilities
	Limits       po.ModelLimits

	HTTPClient *http.Client

	// 常用采样参数，nil表示不取，0表示取值为0
	Temperature     *float64
	TopP            *float64
	PresencePenalty *float64

	// 该字段用于传递兼容服务端的扩展参数，例如：
	//   top_k、chat_template_kwargs enable_thinking、preserve_thinking
	ExtraBody map[string]any

	IncludeUsage bool

	MaxSSELineBytes      int
	MaxSSEEventBytes     int
	MaxToolArgumentBytes int
}

type Model struct {
	config      Config
	info        po.ModelInfo
	httpClient  *http.Client
	endpoint    string
	syntheticID atomic.Uint64
}

// New 在任何网络请求发生前完成配置校验，尽量把错误提前到启动阶段。
func New(config Config) (*Model, error) {
	if strings.TrimSpace(config.Provider) == "" {
		config.Provider = "openai-compatible"
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("openai-compatible base URL is required")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("openai-compatible model is required")
	}

	parsed, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse openai-compatible base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("openai-compatible base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("openai-compatible base URL must include a host")
	}
	if err := config.Capabilities.Validate(); err != nil {
		return nil, fmt.Errorf("openai-compatible capabilities: %w", err)
	}
	if err := config.Limits.Validate(); err != nil {
		return nil, fmt.Errorf("openai-compatible limits: %w", err)
	}
	if err := validateSampling(config); err != nil {
		return nil, err
	}
	if err := validateExtraBody(config.ExtraBody); err != nil {
		return nil, err
	}

	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	base := strings.TrimRight(config.BaseURL, "/")
	info := po.ModelInfo{
		Provider:     config.Provider,
		ID:           config.Model,
		DisplayName:  config.Model,
		Capabilities: config.Capabilities,
		Limits:       config.Limits,
	}
	if err := info.Validate(); err != nil {
		return nil, fmt.Errorf("openai-compatible model info: %w", err)
	}

	return &Model{
		config:     cloneConfig(config),
		info:       info,
		httpClient: client,
		endpoint:   base + chatCompletionsPath,
	}, nil
}

func MustNew(config Config) *Model {
	model, err := New(config)
	if err != nil {
		panic(err)
	}
	return model
}

func (m *Model) Info() po.ModelInfo { return m.info }

// Generate 把 Provider-neutral 的 ModelRequest 映射成 Chat Completions 请求。
// 如果能力画像声明支持 Streaming，即使 emit == nil 也继续走 SSE。这样“是否展示
// 流式 UI”和“Provider 如何生成最终响应”不会变成两套语义不同的实现。
func (m *Model) Generate(ctx context.Context, request po.ModelRequest, emit po.DeltaEmitter) (po.ModelResponse, error) {
	if ctx == nil {
		return po.ModelResponse{}, fmt.Errorf("openai-compatible context must not be nil")
	}
	if err := request.ValidateFor(m.info); err != nil {
		return po.ModelResponse{}, err
	}

	wireRequest, err := m.buildRequest(request, m.info.Capabilities.Streaming)
	if err != nil {
		return po.ModelResponse{}, err
	}

	if m.info.Capabilities.Streaming {
		return m.generateStream(ctx, wireRequest, emit)
	}
	return m.generateOnce(ctx, wireRequest)
}

func (m *Model) buildRequest(request po.ModelRequest, streaming bool) (map[string]any, error) {
	messages, err := encodeMessages(request.SystemPrompt(), request.Messages())
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":    m.config.Model,
		"messages": messages,
		"stream":   streaming,
	}
	if request.MaxOutputTokens() > 0 {
		body["max_tokens"] = request.MaxOutputTokens()
	}
	if m.config.Temperature != nil {
		body["temperature"] = *m.config.Temperature
	}
	if m.config.TopP != nil {
		body["top_p"] = *m.config.TopP
	}
	if m.config.PresencePenalty != nil {
		body["presence_penalty"] = *m.config.PresencePenalty
	}

	tools := request.Tools()
	if len(tools) > 0 {
		encoded := make([]chatTool, 0, len(tools))
		for _, spec := range tools {
			encoded = append(encoded, chatTool{
				Type: "function",
				Function: chatFunctionDefinition{
					Name:        spec.Name(),
					Description: spec.Description(),
					Parameters:  spec.InputSchema(),
				},
			})
		}
		body["tools"] = encoded
	}

	if streaming && m.config.IncludeUsage {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	for key, value := range m.config.ExtraBody {
		body[key] = deepCloneJSONValue(value)
	}
	return body, nil
}

func (m *Model) newRequest(ctx context.Context, body any) (*http.Request, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode openai-compatible request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create openai-compatible request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if m.info.Capabilities.Streaming {
		req.Header.Set("Accept", "text/event-stream")
	}
	if strings.TrimSpace(m.config.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+m.config.APIKey)
	}
	return req, nil
}

func (m *Model) do(ctx context.Context, body any) (*http.Response, error) {
	req, err := m.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	response, err := m.httpClient.Do(req)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		return nil, &provider.Error{
			Op:        "chat.completions",
			Retryable: true,
			Err:       err,
		}
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}

	defer response.Body.Close()
	return nil, classifyHTTPError(response)
}

func (m *Model) generateOnce(ctx context.Context, body map[string]any) (po.ModelResponse, error) {
	response, err := m.do(ctx, body)
	if err != nil {
		return po.ModelResponse{}, err
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	var payload chatCompletion
	if err := decoder.Decode(&payload); err != nil {
		return po.ModelResponse{}, protocolError("decode chat completion", err)
	}
	if len(payload.Choices) == 0 {
		return po.ModelResponse{}, protocolError("chat completion has no choices", nil)
	}

	choice := payload.Choices[0]
	stop, err := mapFinishReason(choice.FinishReason)
	if err != nil {
		return po.ModelResponse{}, err
	}
	parts, err := m.partsFromMessage(payload.ID, choice.Message)
	if err != nil {
		return po.ModelResponse{}, err
	}
	message, err := po.NewAssistantMessage(messageID(payload.ID), parts...)
	if err != nil {
		return po.ModelResponse{}, protocolError("build assistant message", err)
	}
	result, err := po.NewModelResponse(message, stop, payload.Usage.toPo(), payload.ID)
	if err != nil {
		return po.ModelResponse{}, protocolError("build model response", err)
	}
	return result, nil
}

func (m *Model) generateStream(ctx context.Context, body map[string]any, emit po.DeltaEmitter) (po.ModelResponse, error) {
	response, err := m.do(ctx, body)
	if err != nil {
		return po.ModelResponse{}, err
	}
	defer response.Body.Close()

	decoder, err := sse.NewDecoder(response.Body, m.config.MaxSSELineBytes, m.config.MaxSSEEventBytes)
	if err != nil {
		return po.ModelResponse{}, protocolError("create SSE decoder", err)
	}

	state := newStreamState(m, emit)
	for {
		event, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return po.ModelResponse{}, protocolError("read SSE stream", err)
		}
		if event.Data == "[DONE]" {
			break
		}

		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
			return po.ModelResponse{}, protocolError("decode chat completion chunk", err)
		}
		if chunk.Error != nil {
			return po.ModelResponse{}, &provider.Error{
				Op:        "chat.completions.stream",
				Code:      chunk.Error.codeString(),
				Retryable: false,
				Err:       errors.New(chunk.Error.Message),
			}
		}
		if err := state.apply(ctx, chunk); err != nil {
			return po.ModelResponse{}, err
		}
	}
	return state.finish(ctx)
}

func (m *Model) partsFromMessage(responseID string, message chatResponseMessage) ([]po.ContentPart, error) {
	parts := make([]po.ContentPart, 0, 2+len(message.ToolCalls))
	if message.ReasoningContent != "" {
		parts = append(parts, po.ThinkingPart(message.ReasoningContent))
	}
	if message.Content != "" {
		parts = append(parts, po.TextPart(message.Content))
	}
	for index, call := range message.ToolCalls {
		id := call.ID
		if id == "" {
			id = m.syntheticToolCallID(responseID, index)
		}
		toolCall := po.ToolCall{
			ID:        id,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		}
		if err := toolCall.Validate(); err != nil {
			return nil, protocolError("invalid tool call", err)
		}
		parts = append(parts, po.ToolCallPart(toolCall))
	}
	if len(parts) == 0 {
		return nil, protocolError("assistant message has no content", nil)
	}
	return parts, nil
}

func (m *Model) syntheticToolCallID(responseID string, index int) string {
	if responseID != "" {
		return fmt.Sprintf("%s-call-%d", responseID, index)
	}
	return fmt.Sprintf("po-call-%d", m.syntheticID.Add(1))
}

func validateSampling(config Config) error {
	if config.Temperature != nil && (*config.Temperature < 0 || *config.Temperature > 2) {
		return fmt.Errorf("openai-compatible temperature must be between 0 and 2")
	}
	if config.TopP != nil && (*config.TopP < 0 || *config.TopP > 1) {
		return fmt.Errorf("openai-compatible top_p must be between 0 and 1")
	}
	if config.PresencePenalty != nil && (*config.PresencePenalty < -2 || *config.PresencePenalty > 2) {
		return fmt.Errorf("openai-compatible presence_penalty must be between -2 and 2")
	}
	return nil
}

var reservedExtraBodyKeys = map[string]struct{}{
	"model": {}, "messages": {}, "tools": {}, "stream": {}, "stream_options": {}, "max_tokens": {},
	"temperature": {}, "top_p": {}, "presence_penalty": {},
}

func validateExtraBody(extra map[string]any) error {
	for key := range extra {
		if _, reserved := reservedExtraBodyKeys[key]; reserved {
			return fmt.Errorf("openai-compatible extra_body cannot override reserved key %q", key)
		}
	}
	return nil
}

func cloneConfig(config Config) Config {
	clone := config
	clone.ExtraBody = make(map[string]any, len(config.ExtraBody))
	for key, value := range config.ExtraBody {
		clone.ExtraBody[key] = deepCloneJSONValue(value)
	}
	return clone
}

func deepCloneJSONValue(value any) any {
	payload, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var clone any
	if err := json.Unmarshal(payload, &clone); err != nil {
		return value
	}
	return clone
}

func classifyHTTPError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	var payload errorEnvelope
	_ = json.Unmarshal(body, &payload)

	message := strings.TrimSpace(payload.Error.Message)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = response.Status
	}
	code := payload.Error.codeString()
	if code == "" {
		code = payload.Error.Type
	}

	return &provider.Error{
		Op:         "chat.completions",
		StatusCode: response.StatusCode,
		Code:       code,
		RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		Retryable:  retryableStatus(response.StatusCode),
		Err:        errors.New(message),
	}
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

func protocolError(message string, err error) error {
	if err == nil {
		err = errors.New(message)
	} else {
		err = fmt.Errorf("%s: %w", message, err)
	}
	return &provider.Error{
		Op:        "chat.completions.protocol",
		Retryable: false,
		Err:       err,
	}
}

func messageID(responseID string) string {
	if responseID != "" {
		return "assistant-" + responseID
	}
	return fmt.Sprintf("assistant-%d", time.Now().UnixNano())
}

var _ po.Model = (*Model)(nil)
