package po

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Tool 是 Runtime 可以执行的本地工具，与只描述协议的 ToolSpec 职责不同。
// Agent Loop 通过 ToolRegistry 按名称查找并执行 Tool；实现本身不会发送给模型。
type Tool interface {
	// Spec 返回工具对模型公开的协议说明。
	Spec() ToolSpec

	// Execute 执行一条已经通过协议检查的 ToolCall。
	Execute(ctx context.Context, call ToolCall, emit ToolUpdateEmitter) (ToolResult, error)
}

// ToolCall 表示模型请求 Runtime 调用一个工具。
// ID 关联调用和结果，Name 用于查找工具，Arguments 保存模型生成的原始 JSON 参数。
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// NewToolCall 是业务代码构造 ToolCall 的推荐入口。
// args 可以是结构体、map 或其他 json.Marshal 支持的值。函数先把它编码成 JSON，
// 再调用 Validate 检查协议不变量。这样正常调用者不用自己手写 RawMessage，
// 同时所有 Tool Call 都经过同一套校验。
func NewToolCall(id, name string, args any) (ToolCall, error) {
	arguments, err := json.Marshal(args)
	if err != nil {
		return ToolCall{}, fmt.Errorf("encode tool arguments: %w", err)
	}
	call := ToolCall{
		ID:        id,
		Name:      name,
		Arguments: arguments,
	}

	// 构造完成后立即校验，避免非法对象进入后续 Agent 状态。
	if err := call.Validate(); err != nil {
		return ToolCall{}, err
	}

	return call, nil
}

// Validate 只检查协议层不变量，不检查具体工具的参数语义。
func (c ToolCall) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("%w: tool id is required", ErrInvalidToolCall)
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("%w: tool name is required", ErrInvalidToolCall)
	}

	// 空参数不是合法 ToolCall。
	if len(bytes.TrimSpace(c.Arguments)) == 0 {
		return fmt.Errorf("%w: tool arguments are required", ErrInvalidToolCall)
	}

	decoder := json.NewDecoder(bytes.NewReader(c.Arguments))

	// UseNumber 防止 JSON 数字提前转换成 float64。
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: tool arguments must be a valid JSON object", ErrInvalidToolCall)
	}

	if err := ensureDecoderEOF(decoder); err != nil {
		return fmt.Errorf("%w: arguments must contain exactly one JSON value: %v", ErrInvalidToolCall, err)
	}

	// 工具参数统一要求顶层为 JSON object。
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("%w: tool arguments must be a valid JSON object", ErrInvalidToolCall)
	}
	return nil
}

// Clone 返回 ToolCall 的深拷贝。
// ToolCall 结构体按值复制时，Arguments 的 Slice 头会复制，但底层字节仍共享。
// bytes.Clone 会创建新的底层数组，防止调用者修改原始 []byte 后篡改消息历史。
func (c ToolCall) Clone() ToolCall {
	clone := c
	clone.Arguments = bytes.Clone(c.Arguments)
	return clone
}

// ToolResult 是工具执行后返回给 Runtime 的结构化结果。
// 它把 content 与 details 分开：
//   - content 会被转换成 ToolResultMessage，再次发送给模型；
//   - details 主要用于 CLI、审计日志、Session 和程序化处理，不必全部消耗模型 Token；
//   - terminate 表示这个结果是否希望把当前工具批次视为最终输出。
//
// ToolResult 不包含 IsError。工具函数返回的非 nil error 才表示执行失败；
// Agent Loop 会把 error 转换成 IsError=true 的 ToolResultMessage，让模型看到失败并决定下一步。
type ToolResult struct {
	content   []ContentPart
	details   json.RawMessage
	terminate bool
}

// NewToolResult 创建并校验一个工具结果。
//
// details 接收任意可被 json.Marshal 编码的 Go 值。传入 nil 时保存 JSON null；
// 如果调用者已经有 json.RawMessage，则函数会验证并复制原始 JSON，而不是把 []byte
// 错误编码成 Base64 字符串。
func NewToolResult(content []ContentPart, details any, terminate bool) (ToolResult, error) {
	encodedDetails, err := encodeToolDetails(details)
	if err != nil {
		return ToolResult{}, err
	}

	result := ToolResult{
		content:   cloneParts(content),
		details:   encodedDetails,
		terminate: terminate,
	}
	if err := result.Validate(); err != nil {
		return ToolResult{}, err
	}
	return result, nil
}

// NewTextToolResult 是大多数文本工具的便利构造函数。
//
// 例如 read_file 可以把适合模型阅读的文件片段放进 text，
// 再把路径、字节数、是否截断等结构化信息放进 details。
func NewTextToolResult(text string, details any, terminate bool) (ToolResult, error) {
	return NewToolResult(
		[]ContentPart{TextPart(text)},
		details,
		terminate,
	)
}

// Content 返回会发送给模型的内容副本。
func (r ToolResult) Content() []ContentPart {
	return cloneParts(r.content)
}

// Details 返回结构化详情的字节副本
func (r ToolResult) Details() json.RawMessage {
	return bytes.Clone(r.details)
}

// Terminate 表示工具结果是否请求结束后续模型循环。
// 真正的批次终止规则由 Agent Loop 决定。
func (r ToolResult) Terminate() bool {
	return r.terminate
}

// Validate 检查 ToolResult 自身的协议不变量。
func (r ToolResult) Validate() error {
	if len(r.content) == 0 {
		return fmt.Errorf(
			"%w: tool result must contain at least one content part",
			ErrInvalidToolResult,
		)
	}

	for index, part := range r.content {
		if err := part.Validate(); err != nil {
			return fmt.Errorf(
				"%w: content part %d: %v",
				ErrInvalidToolResult,
				index,
				err,
			)
		}

		// 协议 v1 中工具结果只允许文本。
		// 结构化数据放在 details；Tool Call 只能由 AssistantMessage 产生。
		if part.Type != ContentText {
			return fmt.Errorf(
				"%w: tool result content only supports text in protocol v1",
				ErrInvalidToolResult,
			)
		}
	}

	if len(bytes.TrimSpace(r.details)) == 0 {
		return fmt.Errorf("%w: details JSON is required", ErrInvalidToolResult)
	}
	if err := validateSingleJSONValue(r.details); err != nil {
		return fmt.Errorf("%w: details: %v", ErrInvalidToolResult, err)
	}
	return nil
}

// encodeToolDetails把不同形式的details统一转换成拥有独立底层数组的JSON
func encodeToolDetails(details any) (json.RawMessage, error) {
	switch value := details.(type) {
	case json.RawMessage:
		cloned := bytes.Clone(value)
		if err := validateSingleJSONValue(cloned); err != nil {
			return nil, fmt.Errorf("%w: details: %v", ErrInvalidToolResult, err)
		}
		return cloned, nil
	case []byte:
		// []byte 在 encoding/json 中默认会被编码为 Base64 字符串。
		// 对 Tool details 来说，这通常不是调用者想要的，因此把它视为原始 JSON。
		cloned := bytes.Clone(value)
		if err := validateSingleJSONValue(cloned); err != nil {
			return nil, fmt.Errorf("%w: details: %v", ErrInvalidToolResult, err)
		}
		return cloned, nil
	default:
		encoded, err := json.Marshal(details)
		if err != nil {
			return nil, fmt.Errorf("%w: encode details: %v", ErrInvalidToolResult, err)
		}
		return encoded, nil
	}
}

// validateSingleJSONValue 确保输入恰好是一份完整 JSON，而不是空输入或两个值拼接。
func validateSingleJSONValue(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := ensureDecoderEOF(decoder); err != nil {
		return err
	}
	return nil
}

// ToolUpdate 描述长时间工具执行过程中的一次临时进度更新。
//
// 它不是最终 ToolResult，不会自动写入模型上下文。CLI 可以显示它，
// Event 系统也可以把它转成 tool_updated 事件。
type ToolUpdate struct {
	// Message 是面向用户的当前状态，例如“正在扫描 153 个文件”。
	Message string

	// Progress 是可选的 0 到 1 进度。
	// nil 表示工具无法可靠计算百分比，而不是 0%。
	Progress *float64
}

// NewToolUpdate 创建只有文字、没有确定百分比的更新。
func NewToolUpdate(message string) ToolUpdate {
	return ToolUpdate{Message: message}
}

// NewProgressToolUpdate 创建带进度比例的更新。
func NewProgressToolUpdate(message string, progress float64) ToolUpdate {
	// progress 是局部变量，把它的地址保存到返回值中是安全的；
	// Go 的逃逸分析会把它移动到堆上，生命周期不会在函数返回时结束。
	return ToolUpdate{
		Message:  message,
		Progress: &progress,
	}
}

// Clone 复制更新，并为 Progress 创建独立指针。
func (u ToolUpdate) Clone() ToolUpdate {
	clone := u
	if u.Progress != nil {
		progress := *u.Progress
		clone.Progress = &progress
	}
	return clone
}

// Validate 检查进度更新是否可以可靠展示。
func (u ToolUpdate) Validate() error {
	if strings.TrimSpace(u.Message) == "" && u.Progress == nil {
		return fmt.Errorf("%w: message and progress cannot both be empty", ErrInvalidToolUpdate)
	}
	if u.Progress != nil {
		progress := *u.Progress
		if math.IsNaN(progress) || math.IsInf(progress, 0) || progress < 0 || progress > 1 {
			return fmt.Errorf("%w: progress must be a finite number between 0 and 1", ErrInvalidToolUpdate)
		}
	}
	return nil
}

// ToolUpdateEmitter 是工具进度的同步回调。
//
// 同步回调使更新顺序天然明确，并允许 CLI 或事件系统通过返回错误施加背压。
// 事件适配层可以在这个基础上提供异步 Adapter，而不是让每个 Tool 自己管理 Channel。
type ToolUpdateEmitter func(context.Context, ToolUpdate) error

// EmitToolUpdate 统一处理 Context、更新校验和 nil Emitter。
func EmitToolUpdate(ctx context.Context, emit ToolUpdateEmitter, update ToolUpdate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := update.Validate(); err != nil {
		return err
	}
	if emit == nil {
		return nil
	}
	return emit(ctx, update.Clone())
}

// ToolExecutionMode 决定同一条AssistantMessage中多个ToolCall的执行方式
type ToolExecutionMode string

const (
	// ToolExecutionParallel 是默认模式。所有 ToolCall 先按模型顺序完成
	// Lookup、Schema 和 BeforeToolCall，再由资源 FIFO 调度互不冲突的调用。
	ToolExecutionParallel   ToolExecutionMode = "parallel"
	ToolExecutionSequential ToolExecutionMode = "sequential"
)

func (m ToolExecutionMode) Validate() error {
	switch m {
	case ToolExecutionParallel, ToolExecutionSequential:
		return nil
	default:
		return fmt.Errorf("invalid tool execution mode %q", m)
	}
}
