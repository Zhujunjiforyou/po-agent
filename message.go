package po

import (
	"fmt"
	"strings"
)

// ContentType 是 ContentPart 的判别字段
type ContentType string

const (
	// ContentText 最终展示给用户的普通文本
	ContentText ContentType = "text"

	// ContentThinking 模型的思考或推理片段
	ContentThinking ContentType = "thinking"

	// ContentToolCall 表示模型请求执行一个结构化工具调用
	ContentToolCall ContentType = "tool_call"
)

// ContentPart 是一个带判别字段的联合类型
// go没有代数数据类型，因此使用Type决定哪个载荷有效
type ContentPart struct {
	Type     ContentType `json:"type"`
	Text     string      `json:"text,omitempty"`
	ToolCall *ToolCall   `json:"tool_call,omitempty"`
}

// TextPart 创建普通文本片段。
// 使用构造函数可以自动填入正确 Type，减少调用者手写错误组合的机会。
func TextPart(text string) ContentPart {
	return ContentPart{
		Type: ContentText,
		Text: text,
	}
}

// ThinkingPart 创建思考片段
func ThinkingPart(text string) ContentPart {
	return ContentPart{
		Type: ContentThinking,
		Text: text,
	}
}

// ToolCallPart 创建工具调用片段。
func ToolCallPart(call ToolCall) ContentPart {
	// 先深拷贝 ToolCall，确保 ContentPart 不共享调用者的 Arguments 字节。
	cloned := call.Clone()

	return ContentPart{
		Type:     ContentToolCall,
		ToolCall: &cloned,
	}
}

// Validate 检查Type与载荷字段是否一致
func (p ContentPart) Validate() error {
	switch p.Type {
	// 文本类片段不允许同时携带ToolCall
	case ContentText, ContentThinking:
		if p.ToolCall != nil {
			return fmt.Errorf(
				"%w: %s part cannot contain a tool call",
				ErrInvalidContentPart,
				p.Type,
			)
		}
	case ContentToolCall:
		// tool_call类型必须真的包含toolcall数据
		if p.ToolCall == nil {
			return fmt.Errorf(
				"%w: tool_call payload is required",
				ErrInvalidContentPart,
			)
		}
		// 工具调用是结构化数据，不允许再把命令文本混进 Text 字段。
		if p.Text != "" {
			return fmt.Errorf(
				"%w: tool_call part cannot contain text",
				ErrInvalidContentPart,
			)
		}
		// ContentPart 合法还要求内部 ToolCall 自身通过协议校验。
		if err := p.ToolCall.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidContentPart, err)
		}
	default:
		// 未知 Type 必须拒绝，避免新旧版本之间静默丢失语义。
		return fmt.Errorf(
			"%w: unsupported type %q",
			ErrInvalidContentPart,
			p.Type,
		)
	}
	return nil
}

// Clone 返回ContentPart的深拷贝
func (p ContentPart) Clone() ContentPart {
	clone := p
	// 主要是复制toolcall内部的[]byte
	if p.ToolCall != nil {
		call := p.ToolCall.Clone()
		clone.ToolCall = &call
	}
	return clone
}

// cloneParts 复制整个ContentPart Slice，并深拷贝其中的TooCall
func cloneParts(parts []ContentPart) []ContentPart {
	cloned := make([]ContentPart, len(parts))
	for i, part := range parts {
		cloned[i] = part.Clone()
	}
	return cloned
}

type MessageKind string

// 持久化协议中的消息判别字段
const (
	MessageUser       MessageKind = "user"
	MessageAssistant  MessageKind = "assistant"
	MessageToolResult MessageKind = "tool_result"
)

// Message 是所有事实消息共同实现的接口
// 设计目标：
//   - Agent Loop 可以统一处理不同消息；
//   - 每种消息仍保留自己的字段约束；
//   - Codec 能通过 Kind 和具体类型可靠分派；
//   - 外部包不能随意创造 Codec 不认识的新消息类型。
type Message interface {
	// Kind 返回消息类别，用于 Loop 判断和 Wire Codec 分派。
	Kind() MessageKind

	// MessageID 返回消息唯一 ID。后续事件、Session 和回放都会依赖它。
	MessageID() string

	// Validate 检查当前具体消息是否满足协议不变量。
	Validate() error

	// isMessage 是未导出标记方法，用于把 Message 的实现集合限制在 package po 内。
	// 外部包无法声明同名的未导出方法，因此不能制造 Codec 无法恢复的自定义事实消息。
	isMessage()
}

type UserMessage struct {
	id    string
	parts []ContentPart
}

func NewUserMessage(id string, parts ...ContentPart) (UserMessage, error) {
	message := UserMessage{
		id: id,
		// 不直接保存调用者传入的slice，避免外部修改影响消息内部状态
		parts: cloneParts(parts),
	}
	if err := message.Validate(); err != nil {
		return UserMessage{}, err
	}
	return message, nil
}

func NewUserTextMessage(id, text string) (UserMessage, error) {
	return NewUserMessage(id, TextPart(text))
}

func (m UserMessage) Kind() MessageKind {
	// kind不从字段中取，而是由具体go类型固定返回
	return MessageUser
}

func (m UserMessage) MessageID() string {
	// 只返回 string 值，不暴露可修改的内部状态。
	// 后续事件系统会用这个 ID 关联 message_started 和 message_completed。
	return m.id
}

// Parts 返回副本，而不是内部 Slice 本身，保持消息的值语义。
func (m UserMessage) Parts() []ContentPart {
	// 必须返回深拷贝，不能直接 return m.parts。
	// Slice 本身只是一个指向底层数组的描述符；直接返回会允许调用者修改历史消息。
	return cloneParts(m.parts)
}

func (UserMessage) isMessage() {
	// 这是“标记方法”，运行时不执行任何逻辑。
	// 因为方法名未导出，package po 之外的类型无法实现 Message 接口。
}

// Validate 检查UserMessage的协议约束
func (m UserMessage) Validate() error {
	if err := validateMessageID(m.id); err != nil {
		return err
	}

	if len(m.parts) == 0 {
		return fmt.Errorf("%w: user message must contain at least one part", ErrInvalidMessage)
	}

	for i, part := range m.parts {
		// 先检查part本身是否合法
		if err := part.Validate(); err != nil {
			return fmt.Errorf("%w: user part %d: %v", ErrInvalidMessage, i, err)
		}

		// 协议v1中用户只能提交文本; tool call只由AssistantMessage产生
		if part.Type != ContentText {
			return fmt.Errorf("%w: user messages only support text parts in protocol v1", ErrInvalidMessage)
		}
	}
	return nil
}

// AssistantMessage 表示模型生成的消息。
// 它可以同时包含文本、思考和一个或多个 Tool Call。
type AssistantMessage struct {
	id    string
	parts []ContentPart
}

// NewAssistantMessage 构造并校验 AssistantMessage。
func NewAssistantMessage(id string, parts ...ContentPart) (AssistantMessage, error) {
	message := AssistantMessage{
		id:    id,
		parts: cloneParts(parts),
	}

	if err := message.Validate(); err != nil {
		return AssistantMessage{}, err
	}
	return message, nil
}

func (m AssistantMessage) Kind() MessageKind {
	// 具体类型决定固定消息种类，避免公开 role 字段被任意篡改。
	return MessageAssistant
}

func (m AssistantMessage) MessageID() string {
	// Assistant 消息也必须有稳定 ID，后续 Streaming 会持续更新同一条消息。
	return m.id
}

func (m AssistantMessage) Parts() []ContentPart {
	// AssistantMessage 可能包含 ToolCall，而 ToolCall 内部又含有 []byte。
	// 因此这里使用深拷贝，而不是只复制最外层 Slice。
	return cloneParts(m.parts)
}

func (AssistantMessage) isMessage() {
	// 仅用于封闭 Message 实现集合，不承担业务逻辑。
}

func (m AssistantMessage) Validate() error {
	if err := validateMessageID(m.id); err != nil {
		return err
	}

	if len(m.parts) == 0 {
		return fmt.Errorf("%w: assistant message must contain at least one part", ErrInvalidMessage)
	}

	// AssistantMessage 允许 text、thinking、tool_call，因此这里只检查 Part 自身合法性。
	for i, part := range m.parts {
		if err := part.Validate(); err != nil {
			return fmt.Errorf("%w: assistant part %d: %v", ErrInvalidMessage, i, err)
		}
	}

	return nil
}

// ToolResultMessage 表示工具执行完成后写回Transcript的事实消息
// toolCallID 是最关键的关联键：模型可能在一条 AssistantMessage 中发出多个 Tool Call，
// Runtime 必须让每个结果准确对应原始调用，而不能只依赖工具名或执行顺序。
type ToolResultMessage struct {
	id         string
	toolCallID string
	toolName   string
	isError    bool
	parts      []ContentPart
}

// NewToolResultMessage 构造并校验工具结果消息
func NewToolResultMessage(
	id string,
	toolCallID string,
	toolName string,
	isError bool,
	parts ...ContentPart,
) (ToolResultMessage, error) {
	message := ToolResultMessage{
		id:         id,
		toolCallID: toolCallID,
		toolName:   toolName,
		isError:    isError,
		parts:      cloneParts(parts),
	}
	if err := message.Validate(); err != nil {
		return ToolResultMessage{}, err
	}
	return message, nil
}

func (m ToolResultMessage) Kind() MessageKind {
	// ToolResultMessage 的种类同样由类型固定，不能由外部字段冒充。
	return MessageToolResult
}

func (m ToolResultMessage) MessageID() string {
	// 这是“消息自身”的 ID，不要和 toolCallID 混淆。
	// 一条 ToolResultMessage 既需要自己的消息 ID，也需要关联原调用的 ID。
	return m.id
}

func (m ToolResultMessage) ToolCallID() string {
	// 返回被本结果响应的 Tool Call ID。
	// 当模型一次发出多个工具调用时，Runtime 依靠它准确配对结果。
	return m.toolCallID
}

func (m ToolResultMessage) ToolName() string {
	// 保留工具名便于日志、UI 和 Provider 转换。
	// 真正的关联仍以 toolCallID 为准，不能只靠名称。
	return m.toolName
}

func (m ToolResultMessage) IsError() bool {
	// 工具执行失败也要形成一条 Tool Result 并返回模型，
	// 让模型有机会更换参数、改用其他工具或向用户解释失败。
	return m.isError
}

func (m ToolResultMessage) Parts() []ContentPart {
	// 保持和其他消息一致的只读值语义：调用者只能获得副本。
	return cloneParts(m.parts)
}

func (ToolResultMessage) isMessage() {
	// 标记方法：限制 Message 的实现集合。
}

func (m ToolResultMessage) Validate() error {
	if err := validateMessageID(m.id); err != nil {
		return err
	}
	if strings.TrimSpace(m.toolCallID) == "" {
		return fmt.Errorf("%w: tool result call id is required", ErrInvalidMessage)
	}
	if strings.TrimSpace(m.toolName) == "" {
		return fmt.Errorf("%w: tool result name is required", ErrInvalidMessage)
	}
	if len(m.parts) == 0 {
		return fmt.Errorf("%w: tool result must contain at least one part", ErrInvalidMessage)
	}
	for i, part := range m.parts {
		if err := part.Validate(); err != nil {
			return fmt.Errorf("%w: tool part %d: %v", ErrInvalidMessage, i, err)
		}
		// v1 中工具只返回文本
		if part.Type != ContentText {
			return fmt.Errorf(
				"%w: tool results only support text parts in protocol v1",
				ErrInvalidMessage,
			)
		}
	}
	return nil
}

// validateMessageID 集中处理所有消息共享的 ID 约束。
func validateMessageID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: message id is required", ErrInvalidMessage)
	}
	return nil
}

// ToolCalls 返回AssistantMessage 中所有工具调用的深拷贝
// Agent Loop、ModelResponse 校验和 CLI 都需要查询 Tool Call；把遍历逻辑集中到这里，
// 可以避免每个模块重复处理 nil 指针和 RawMessage 所有权。
func (m AssistantMessage) ToolCalls() []ToolCall {
	parts := m.Parts()
	calls := make([]ToolCall, 0)
	for _, part := range parts {
		if part.Type != ContentToolCall || part.ToolCall == nil {
			continue
		}
		calls = append(calls, part.ToolCall.Clone())
	}
	return calls
}

// Text 返回所有普通文本片段按原始顺序拼接后的结果。
// 它不会包含 Thinking，也不会把 Tool Call JSON 混入用户可见文本。
func (m AssistantMessage) Text() string {
	parts := m.Parts()
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == ContentText {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}
