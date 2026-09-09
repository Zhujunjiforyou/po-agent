package po

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MessageWireVersion 是当前 JSON 持久化协议版本。
const MessageWireVersion = 1

// wireMessage 是专门用于 JSON 编解码的 DTO（Data Transfer Object）。
// Domain Message 使用私有字段和不同具体类型来保证约束；Wire Message 则使用一个
// Codec 负责在两者之间转换。
type wireMessage struct {
	Version int         `json:"version"`
	Kind    MessageKind `json:"kind"`
	ID      string      `json:"id"`

	// Parts 对三种消息都存在。
	Parts []ContentPart `json:"parts"`

	// 以下字段只对 ToolResultMessage 有效。
	// omitempty 让 User/Assistant 的 JSON 不携带无意义的空字段。
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// MarshalMessage 把Domain Message转换为版本化JSON
func MarshalMessage(message Message) ([]byte, error) {
	if message == nil {
		return nil, fmt.Errorf("%w: message is nil", ErrInvalidMessage)
	}
	if err := message.Validate(); err != nil {
		return nil, err
	}

	wire := wireMessage{
		Version: MessageWireVersion,
		Kind:    message.Kind(),
		ID:      message.MessageID(),
	}
	// 通过类型开关读取各具体消息独有的字段。
	switch typed := message.(type) {
	case UserMessage:
		wire.Parts = typed.Parts()

	case AssistantMessage:
		wire.Parts = typed.Parts()

	case ToolResultMessage:
		wire.Parts = typed.Parts()
		wire.ToolCallID = typed.ToolCallID()
		wire.ToolName = typed.ToolName()
		wire.IsError = typed.IsError()

	default:
		return nil, fmt.Errorf("%w: unsupported concrete type %T", ErrInvalidMessage, message)
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	return data, nil
}

// UnmarshalMessage 把版本化 JSON 恢复为经过校验的 Domain Message。
func UnmarshalMessage(data []byte) (Message, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))

	// 默认 json.Unmarshal 会忽略未知字段。协议层选择严格模式，
	// 让拼写错误和新旧版本不兼容尽早暴露。
	decoder.DisallowUnknownFields()

	var wire wireMessage
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}

	// 拒绝第一条消息后仍存在额外 JSON 值的输入。
	if err := ensureDecoderEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}

	// 先检查协议版本，再解释具体字段。未来这里可以按版本路由到不同 Decoder。
	if wire.Version != MessageWireVersion {
		return nil, fmt.Errorf(
			"%w: got %d, want %d", ErrUnsupportedWireVersion, wire.Version, MessageWireVersion)
	}

	// 根据 Kind 使用正式构造函数恢复 Domain Message。
	// 构造函数会再次执行各具体类型的完整语义校验。
	switch wire.Kind {
	case MessageUser:
		// UserMessage 不允许携带 ToolResult 专属字段。
		if wire.ToolCallID != "" || wire.ToolName != "" || wire.IsError {
			return nil, fmt.Errorf("%w: user message contains tool result fields", ErrInvalidMessage)
		}
		return NewUserMessage(wire.ID, wire.Parts...)

	case MessageAssistant:
		// AssistantMessage 同样不能伪装成 ToolResult。
		if wire.ToolCallID != "" || wire.ToolName != "" || wire.IsError {
			return nil, fmt.Errorf("%w: assistant message contains tool result fields", ErrInvalidMessage)
		}
		return NewAssistantMessage(wire.ID, wire.Parts...)

	case MessageToolResult:
		return NewToolResultMessage(
			wire.ID,
			wire.ToolCallID,
			wire.ToolName,
			wire.IsError,
			wire.Parts...,
		)

	default:
		return nil, fmt.Errorf("%w: unsupported kind %q", ErrInvalidMessage, wire.Kind)
	}
}

// ensureDecoderEOF 确认 Decoder已经消费完输入，后面不存在第二个json值
func ensureDecoderEOF(decoder *json.Decoder) error {
	// extra用于接受可能出现的第二个json值
	var extra any
	// 尝试继续解码
	// io.EOF 表示输入已经完整结束
	// 其他错误表示尾部不是合法json
	// err == nil 表示还有第二个json值,拒绝
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing json: %w", err)
	}
	return fmt.Errorf("multiple JSON values are not allowed")
}
