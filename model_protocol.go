package po

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ToolSpec 是发送给模型的工具说明书
type ToolSpec struct {
	name        string
	description string
	inputSchema json.RawMessage
}

func NewToolSpec(name, description string, inputSchema json.RawMessage) (ToolSpec, error) {
	spec := ToolSpec{
		name:        name,
		description: description,
		// json.RawMessage底层是[]byte，必须复制
		inputSchema: bytes.Clone(inputSchema),
	}
	if err := spec.Validate(); err != nil {
		return ToolSpec{}, err
	}
	return spec, nil
}

// Name 返回模型调用工具时使用的稳定标识。
//
// 该值会同时出现在发送给 Provider 的工具列表和模型返回的 Tool Call 中，
// 因此它不能被当作可随意修改的 UI 文案。面向用户展示的名称可以以后单独增加 Label。
func (s ToolSpec) Name() string {
	return s.name
}

// Description 返回给模型阅读的工具说明。
// 它不是普通注释：模型会根据这段文字决定“什么时候调用这个工具”。
// 描述过于模糊会降低工具选择准确率，描述中夸大能力则会诱导模型生成
// 无法执行的请求。
func (s ToolSpec) Description() string {
	return s.description
}

// InputSchema 返回 Schema 的副本，避免 Provider 适配器意外改写 Registry 中的定义。
func (s ToolSpec) InputSchema() json.RawMessage {
	return bytes.Clone(s.inputSchema)
}

// Validate 只校验 ToolSpec 的协议形状。
// 完整 JSON Schema 语义和工具参数验证由独立的 Schema 层处理。
func (s ToolSpec) Validate() error {
	if strings.TrimSpace(s.name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidToolSpec)
	}
	if strings.TrimSpace(s.description) == "" {
		return fmt.Errorf("%w: description is required", ErrInvalidToolSpec)
	}
	if len(bytes.TrimSpace(s.inputSchema)) == 0 {
		return fmt.Errorf("%w: input schema is required", ErrInvalidToolSpec)
	}

	decoder := json.NewDecoder(bytes.NewReader(s.inputSchema))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: schema is not valid JSON: %v", ErrInvalidToolSpec, err)
	}
	if err := ensureDecoderEOF(decoder); err != nil {
		return fmt.Errorf("%w: schema must contain one JSON value: %v", ErrInvalidToolSpec, err)
	}

	// 工具参数 Schema 顶层必须是 object，这与 ToolCall.Arguments 的约束保持一致。
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("%w: input schema must be a JSON object", ErrInvalidToolSpec)
	}
	return nil
}

// ModelRequest 是 Agent Core 在某一 Turn 发送给模型的完整请求。
//
// 字段保持私有，原因和 Message 相同：请求一旦交给 Model，调用期间不应该被外部
// goroutine 修改。构造时复制 Slice，getter 再返回副本，明确数据所有权。
type ModelRequest struct {
	systemPrompt    string
	messages        []Message
	tools           []ToolSpec
	maxOutputTokens int
}

// NewModelRequest 构造一个 Provider-neutral 请求。
//
// maxOutputTokens 为 0 表示使用模型或 Provider 默认值；正数表示本次请求的显式上限。
func NewModelRequest(systemPrompt string, messages []Message, tools []ToolSpec, maxOutputTokens int) (ModelRequest, error) {
	request := ModelRequest{
		systemPrompt: systemPrompt,

		// Message 自身已经通过私有字段和深拷贝实现“逻辑不可变”，
		// 因此这里复制接口 Slice 即可，不必再次序列化所有消息。
		messages: append([]Message(nil), messages...),

		// ToolSpec 内含 RawMessage，需要逐项深拷贝。
		tools:           cloneToolSpecs(tools),
		maxOutputTokens: maxOutputTokens,
	}
	if err := request.Validate(); err != nil {
		return ModelRequest{}, err
	}
	return request, nil
}

// SystemPrompt 返回本次请求使用的系统提示词。
//
// string 在 Go 中是只读值语义，直接返回不会暴露可修改的底层缓冲区。
func (r ModelRequest) SystemPrompt() string {
	return r.systemPrompt
}

// Messages 返回消息 Slice 的副本。
//
// 这里不能直接 return r.messages，否则调用者执行 append、排序或覆盖元素时，
// 可能修改 Model 正在读取的请求。消息对象本身保证逻辑不可变，
// 因此只需要复制 Slice 的顶层结构。
func (r ModelRequest) Messages() []Message {
	return append([]Message(nil), r.messages...)
}

// Tools 返回工具说明的深拷贝。
//
// ToolSpec 内部包含 json.RawMessage，而 RawMessage 的底层是 []byte，
// 所以仅复制 Slice 不够，还必须逐项 Clone。
func (r ModelRequest) Tools() []ToolSpec {
	return cloneToolSpecs(r.tools)
}

// MaxOutputTokens 返回本次请求显式要求的最大输出 Token 数。
// 返回 0 表示交给 Model 或 Provider 使用默认值。
func (r ModelRequest) MaxOutputTokens() int {
	return r.maxOutputTokens
}

// Validate 检查自身请求是否合法，但不依赖某个具体模型的能力
func (r ModelRequest) Validate() error {
	if len(r.messages) == 0 {
		return fmt.Errorf("%w: at least one message is required", ErrInvalidModelRequest)
	}

	for index, message := range r.messages {
		if message == nil || isNilMessage(message) {
			return fmt.Errorf("%w: message %d is nil", ErrInvalidModelRequest, index)
		}
		if err := message.Validate(); err != nil {
			return fmt.Errorf("%w: message %d: %v", ErrInvalidModelRequest, index, err)
		}
	}

	seenToolNames := make(map[string]struct{}, len(r.tools))
	for i, spec := range r.tools {
		if err := spec.Validate(); err != nil {
			return fmt.Errorf("%w: tool %d: %v", ErrInvalidModelRequest, i, err)
		}
		if _, exists := seenToolNames[spec.Name()]; exists {
			return fmt.Errorf("%w: duplicate tool name %q", ErrInvalidModelRequest, spec.Name())
		}
		seenToolNames[spec.Name()] = struct{}{}
	}
	if r.maxOutputTokens < 0 {
		return fmt.Errorf("%w: max output tokens cannot be negative", ErrInvalidModelRequest)
	}
	return nil
}

// ValidateFor 把一个合法请求与具体模型能力进行匹配。
//
// 这一步不放进 Provider 里，是为了让错误在发出网络请求前就被发现。
func (r ModelRequest) ValidateFor(info ModelInfo) error {
	if len(r.tools) > 0 && !info.Capabilities.Tools {
		return fmt.Errorf("%w: model %s does not support tools", ErrInvalidModelRequest, info.QualifiedID())
	}

	if r.maxOutputTokens > info.Limits.MaxOutputTokens {
		return fmt.Errorf("%w: requested output limit %d exceeds model limit %d", ErrInvalidModelRequest,
			r.maxOutputTokens, info.Limits.MaxOutputTokens)
	}
	return nil
}

func cloneToolSpecs(specs []ToolSpec) []ToolSpec {
	return append([]ToolSpec(nil), specs...)
}

// ModelStopReason 描述“本次模型生成为什么停止”。
//
// 它刻意不包含 max_turns、budget_exceeded 等 Agent 级原因：
//   - ModelStopReason 属于单次 Provider 调用；
//   - RunStopReason 属于整个 Agent Run，由 Agent Loop 定义。
//
// 把两个层次分开，可以避免出现“模型说自己因为 Agent 最大轮次而停止”
// 这种责任混乱。
type ModelStopReason string

const (
	// ModelStopEndTurn 表示模型已经给出当前 Turn 的正常最终回复。
	ModelStopEndTurn ModelStopReason = "end_turn"

	// ModelStopToolCall 表示模型要求 Runtime 执行一个或多个工具。
	ModelStopToolCall ModelStopReason = "tool_call"

	// ModelStopLength 表示输出被最大 Token 数截断。
	// 如果消息中包含 Tool Call，Runtime 不能直接执行，因为参数可能只生成了一部分。
	ModelStopLength ModelStopReason = "length"
)

func (r ModelStopReason) Validate() error {
	switch r {
	case ModelStopEndTurn, ModelStopToolCall, ModelStopLength:
		return nil
	default:
		return fmt.Errorf("%w: unsupported stop reason %q", ErrInvalidModelResponse, r)
	}
}

// Usage 保存一次模型调用的 Token 与费用信息。
//
// reasoning token 如果 Provider 有单独统计，可通过扩展字段提供；当前总量只要求
// input/output/cache 几项稳定存在。CostUSD 使用 float64 是工程折中：它适合展示和预算
// 粗控制，但不应用于严格财务结算；真正计费系统应使用整数微美元或十进制定点数。
type Usage struct {
	// InputTokens 是本次请求被模型计入的输入 Token 数。
	InputTokens int64

	// OutputTokens 是模型最终生成的输出 Token 数。
	OutputTokens int64

	// CacheReadTokens 表示从 Provider 提示缓存中命中的输入 Token。
	// 它通常已经包含在 InputTokens 中，不能再次加入 TotalTokens。
	CacheReadTokens int64

	// CacheWriteTokens 表示本次写入提示缓存的 Token 数。
	CacheWriteTokens int64

	// CostUSD 是 Provider 根据自身价格计算的美元成本。
	// 它适合 Runtime 预算和界面展示，不适合作为财务结算的精确小数类型。
	CostUSD float64
}

func (u Usage) Validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 ||
		u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 {
		return fmt.Errorf("%w: token counts cannot be negative", ErrInvalidUsage)
	}
	if math.IsNaN(u.CostUSD) || math.IsInf(u.CostUSD, 0) || u.CostUSD < 0 {
		return fmt.Errorf("%w: cost must be a finite non-negative number", ErrInvalidUsage)
	}
	return nil
}

// TotalTokens 返回 Provider 报告的输入与输出 Token 总和。
// CacheRead/CacheWrite 通常是输入 Token 的计费细分，因此不能再次相加，否则会重复统计。
func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens
}

// Add 聚合多个 Turn 的 Usage
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + other.InputTokens,
		OutputTokens:     u.OutputTokens + other.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + other.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + other.CacheWriteTokens,
		CostUSD:          u.CostUSD + other.CostUSD,
	}
}

// ModelResponse 是一次 Model.Generate 的最终、完整结果。
//
// 流式 Delta 只服务于实时 UI；最终状态仍必须由一个经过校验的 ModelResponse 收口
type ModelResponse struct {
	message    AssistantMessage
	stopReason ModelStopReason
	usage      Usage
	responseID string
}

// NewModelResponse 创建并校验最终响应。
func NewModelResponse(message AssistantMessage, stopReason ModelStopReason, usage Usage, responseID string) (ModelResponse, error) {
	response := ModelResponse{
		message:    message,
		stopReason: stopReason,
		usage:      usage,
		responseID: responseID,
	}
	if err := response.Validate(); err != nil {
		return ModelResponse{}, err
	}
	return response, nil
}

// Message 返回这次调用产生的最终 AssistantMessage。
func (r ModelResponse) Message() AssistantMessage {
	// AssistantMessage 对外已经是逻辑不可变值，因此按值返回即可。
	return r.message
}

// StopReason 返回模型结束本次生成的原因。
// Agent Loop 会根据它决定当前回复是最终答案、工具请求，还是被长度截断。
func (r ModelResponse) StopReason() ModelStopReason {
	return r.stopReason
}

// Usage 返回本次模型调用的 Token 与费用统计。
func (r ModelResponse) Usage() Usage {
	return r.usage
}

// ResponseID 返回 Provider 为本次响应分配的追踪标识。
// 它主要用于日志、问题定位和后续 Provider 特性，不参与 Agent 业务判断。
func (r ModelResponse) ResponseID() string {
	return r.responseID
}

// Validate检查StopReason与消息内容之间的交叉约束
func (r ModelResponse) Validate() error {
	if err := r.message.Validate(); err != nil {
		return fmt.Errorf("%w: assistant message: %v", ErrInvalidModelResponse, err)
	}
	if err := r.stopReason.Validate(); err != nil {
		return err
	}
	if err := r.usage.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidModelResponse, err)
	}

	toolCalls := r.message.ToolCalls()
	switch r.stopReason {
	case ModelStopEndTurn:
		// 正常结束不应该同时要求执行工具
		if len(toolCalls) > 0 {
			return fmt.Errorf("%w: end_turn response cannot contain tool calls", ErrInvalidModelResponse)
		}
	case ModelStopToolCall:
		// Provider 报告 tool_call，却没有任何结构化 Tool Call，说明映射层丢失了数据
		if len(toolCalls) == 0 {
			return fmt.Errorf("%w: tool_call response must contain at least one tool call", ErrInvalidModelResponse)
		}
	case ModelStopLength:
		// length 允许既有文本也有 Tool Call。Agent Loop 会把其中 Tool Call 视为不安全，
		// 返回错误结果要求模型重新生成，而不是执行可能截断的参数。
	}
	return nil
}

// ModelDeltaKind 描述流式响应中的增量类型。
//
// 协议层只建立稳定接口；SSE 解析、增量 Tool Call 组装和截断恢复由对应适配层实现。
type ModelDeltaKind string

const (
	// ModelDeltaText 表示一段面向用户展示的普通文本增量。
	ModelDeltaText ModelDeltaKind = "text"

	// ModelDeltaThinking 表示独立的推理或思考增量。
	// 是否向用户展示应由产品层决定，Runtime 只负责保持结构。
	ModelDeltaThinking ModelDeltaKind = "thinking"

	// ModelDeltaToolCallStart 表示一个新的 Tool Call 开始。
	ModelDeltaToolCallStart ModelDeltaKind = "tool_call_start"

	// ModelDeltaToolCallArguments 表示 Tool Call JSON 参数的一段字符串碎片。
	ModelDeltaToolCallArguments ModelDeltaKind = "tool_call_arguments"

	// ModelDeltaToolCallEnd 表示该 Tool Call 的流式参数已经结束。
	ModelDeltaToolCallEnd ModelDeltaKind = "tool_call_end"
)

type ModelDelta struct {
	Kind ModelDeltaKind

	// ContentIndex 标识该增量同时属于AssistantMessage.Parts 中的哪个位置
	ContentIndex int

	// Text用于text和thinking增量
	Text string

	// ToolCallID / ToolName 用于工具调用开始、参数和结束事件
	ToolCallID string
	ToolName   string

	// ArgumentsDelta 是 JSON 参数的字符串碎片。
	// 它可能不是完整 JSON，因此绝不能使用 json.RawMessage 或提前执行 Schema 校验。
	ArgumentsDelta string
}

func (d ModelDelta) Validate() error {
	if d.ContentIndex < 0 {
		return fmt.Errorf("%w: content index cannot be negative", ErrInvalidModelDelta)
	}

	switch d.Kind {
	case ModelDeltaText, ModelDeltaThinking:
		if d.Text == "" {
			return fmt.Errorf("%w: text delta cannot be empty", ErrInvalidModelDelta)
		}
		if d.ToolCallID != "" || d.ToolName != "" || d.ArgumentsDelta != "" {
			return fmt.Errorf("%w: text delta contains tool fields", ErrInvalidModelDelta)
		}

	case ModelDeltaToolCallStart:
		if strings.TrimSpace(d.ToolCallID) == "" || strings.TrimSpace(d.ToolName) == "" {
			return fmt.Errorf("%w: tool call start requires id and name", ErrInvalidModelDelta)
		}

	case ModelDeltaToolCallArguments:
		if strings.TrimSpace(d.ToolCallID) == "" {
			return fmt.Errorf("%w: tool argument delta requires call id", ErrInvalidModelDelta)
		}
		if d.ArgumentsDelta == "" {
			return fmt.Errorf("%w: tool argument delta cannot be empty", ErrInvalidModelDelta)
		}

	case ModelDeltaToolCallEnd:
		if strings.TrimSpace(d.ToolCallID) == "" {
			return fmt.Errorf("%w: tool call end requires call id", ErrInvalidModelDelta)
		}

	default:
		return fmt.Errorf("%w: unsupported kind %q", ErrInvalidModelDelta, d.Kind)
	}
	return nil
}

// DeltaEmitter 是模型流式增量的同步回调。
//
// 采用回调而不是底层直接返回 Channel，原因是：
//   - Model 不需要额外启动 goroutine；
//   - emit 返回错误即可向上游施加背压或停止流；
//   - 事件顺序天然与 Provider 读取顺序一致；
//   - 调用者不读取 Channel 导致泄漏的问题被消除。
type DeltaEmitter func(context.Context, ModelDelta) error

// EmitModelDelta 统一处理 nil Emitter、Context 取消和 Delta 校验。
// Provider 与 Scripted Model 都应通过这个辅助函数发送增量。
func EmitModelDelta(ctx context.Context, emit DeltaEmitter, delta ModelDelta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := delta.Validate(); err != nil {
		return err
	}
	if emit == nil {
		return nil
	}
	return emit(ctx, delta)
}
