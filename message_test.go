package po

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestMessageRoundTrip 验证三种消息都能完成：
// Domain Message -> JSON -> Domain Message。
//
// 测试目标不是标准库 json，而是验证类型分派、Wire Kind、版本字段和构造校验
// 能否形成闭环。Session 从 JSONL 恢复历史时会依赖同一能力。
func TestMessageRoundTrip(t *testing.T) {
	call, err := NewToolCall(
		"call-1",
		"read_file",
		map[string]any{"path": "README.md"},
	)
	if err != nil {
		t.Fatal(err)
	}

	// 表驱动测试让不同消息类型共享同一套测试流程。
	tests := []struct {
		name    string
		message Message
	}{
		{
			name:    "user",
			message: mustUserMessage(t, "msg-1", TextPart("read README.md")),
		},
		{
			name: "assistant tool call",
			message: mustAssistantMessage(
				t,
				"msg-2",
				ThinkingPart("I need the file"),
				ToolCallPart(call),
			),
		},
		{
			name: "tool result",
			message: mustToolResultMessage(
				t,
				"msg-3",
				"call-1",
				"read_file",
				false,
				TextPart("hello"),
			),
		},
	}

	for _, test := range tests {
		// t.Run 会为每个案例生成独立子测试，失败时名称更清晰。
		t.Run(test.name, func(t *testing.T) {
			data, err := MarshalMessage(test.message)
			if err != nil {
				t.Fatalf("MarshalMessage() error = %v", err)
			}

			decoded, err := UnmarshalMessage(data)
			if err != nil {
				t.Fatalf("UnmarshalMessage() error = %v", err)
			}

			// 确认协议判别字段和消息 ID 没有丢失。
			if decoded.Kind() != test.message.Kind() {
				t.Fatalf(
					"kind = %q, want %q",
					decoded.Kind(),
					test.message.Kind(),
				)
			}
			if decoded.MessageID() != test.message.MessageID() {
				t.Fatalf(
					"id = %q, want %q",
					decoded.MessageID(),
					test.message.MessageID(),
				)
			}
		})
	}
}

// TestUnmarshalMessageRejectsInvalidInput 验证协议边界会拒绝损坏或语义矛盾的数据。
func TestUnmarshalMessageRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		data string

		// err 可选；设置后除了要求失败，还要求 errors.Is 匹配指定错误类别。
		err error
	}{
		{
			name: "corrupt JSON",
			data: `{"version":1`,
		},
		{
			name: "unknown field",
			data: `{"version":1,"kind":"user","id":"m1","parts":[{"type":"text"}],"surprise":true}`,
		},
		{
			name: "unsupported kind",
			data: `{"version":1,"kind":"system","id":"m1","parts":[{"type":"text"}]}`,
			err:  ErrInvalidMessage,
		},
		{
			name: "missing tool call id",
			data: `{"version":1,"kind":"tool_result","id":"m1","tool_name":"read_file","parts":[{"type":"text"}]}`,
			err:  ErrInvalidMessage,
		},
		{
			name: "user with tool result fields",
			data: `{"version":1,"kind":"user","id":"m1","tool_call_id":"call-1","parts":[{"type":"text"}]}`,
			err:  ErrInvalidMessage,
		},
		{
			name: "unsupported version",
			data: `{"version":99,"kind":"user","id":"m1","parts":[{"type":"text"}]}`,
			err:  ErrUnsupportedWireVersion,
		},
		{
			name: "trailing value",
			data: `{"version":1,"kind":"user","id":"m1","parts":[{"type":"text"}]} {}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := UnmarshalMessage([]byte(test.data))
			if err == nil {
				t.Fatal("UnmarshalMessage() error = nil, want non-nil")
			}

			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf(
					"error = %v, want errors.Is(..., %v)",
					err,
					test.err,
				)
			}
		})
	}
}

// TestMessagesOwnTheirContent 验证消息会深拷贝 Slice 和 RawMessage，
// 从而不会被外部代码事后修改。
func TestMessagesOwnTheirContent(t *testing.T) {
	arguments := json.RawMessage(`{"path":"README.md"}`)
	call := ToolCall{
		ID:        "call-1",
		Name:      "read_file",
		Arguments: arguments,
	}
	message := mustAssistantMessage(t, "msg-1", ToolCallPart(call))

	// 修改最初传入的 RawMessage。
	arguments[2] = 'X'

	// 修改 getter 返回副本中的 RawMessage。
	parts := message.Parts()
	parts[0].ToolCall.Arguments[2] = 'Y'

	// 两次外部修改都不应该改变消息内部保存的事实。
	got := string(message.Parts()[0].ToolCall.Arguments)
	want := `{"path":"README.md"}`
	if got != want {
		t.Fatalf("stored arguments = %q, want %q", got, want)
	}
}

// TestUserMessageRejectsToolCallPart 验证“Part 自身合法”不等于
// “Part 可以放进任何消息”。Tool Call 只能由 AssistantMessage 产生。
func TestUserMessageRejectsToolCallPart(t *testing.T) {
	call, err := NewToolCall(
		"call-1",
		"read_file",
		map[string]any{"path": "README.md"},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewUserMessage("msg-1", ToolCallPart(call))
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("error = %v, want ErrInvalidMessage", err)
	}
}

// 以下 mustXxx 辅助函数用于测试准备阶段。
// 如果构造测试数据都失败，说明测试本身无法继续，直接 t.Fatal 最清晰。
func mustUserMessage(t *testing.T, id string, parts ...ContentPart) UserMessage {
	t.Helper() // 失败时把行号指向调用者，而不是这个辅助函数内部。

	message, err := NewUserMessage(id, parts...)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func mustAssistantMessage(
	t *testing.T,
	id string,
	parts ...ContentPart,
) AssistantMessage {
	t.Helper()

	message, err := NewAssistantMessage(id, parts...)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func mustToolResultMessage(
	t *testing.T,
	id string,
	callID string,
	toolName string,
	isError bool,
	parts ...ContentPart,
) ToolResultMessage {
	t.Helper()

	message, err := NewToolResultMessage(
		id,
		callID,
		toolName,
		isError,
		parts...,
	)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

// TestToolCallRejectsTrailingJSON 验证 ToolCall 参数必须只有一个 JSON object。
func TestToolCallRejectsTrailingJSON(t *testing.T) {
	call := ToolCall{
		ID:        "call-1",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"path":"README.md"} {}`),
	}

	if err := call.Validate(); !errors.Is(err, ErrInvalidToolCall) {
		t.Fatalf("error = %v, want ErrInvalidToolCall", err)
	}
}

// TestMarshalMessageRejectsTypedNil 验证 Codec 不会因接口 typed nil 而 panic。
func TestMarshalMessageRejectsTypedNil(t *testing.T) {
	var user *UserMessage

	_, err := MarshalMessage(user)
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("error = %v, want ErrInvalidMessage", err)
	}
}
