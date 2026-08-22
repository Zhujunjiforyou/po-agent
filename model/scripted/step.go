// Package scripted 提供一个完全确定性的 Model 测试替身。
//
// 它不会访问网络，也不会调用真实 LLM。调用者提前写好一组 Step，
// 每次 Generate 按顺序消费一个 Step，因此测试结果不受模型随机性、额度和网络影响。
package scripted

import (
	"encoding/json"
	"errors"
	"fmt"

	po "github.com/lemonzjj/po-agent-go"
)

// ErrNoRemainingStep 表示 Generate 次数超过了脚本中预设的响应数量。
var ErrNoRemainingStep = errors.New("scripted model has no remaining step")

// ErrConcurrentGenerate 表示同一个线性 Scripted Model 被并发调用。
//
// Scripted Model 的核心语义是“第 1 次调用消费第 1 步”。如果两个 goroutine 同时调用，
// 谁算第 1 次会变得依赖调度器。为避免产生偶现测试，明确拒绝并发调用。
var ErrConcurrentGenerate = errors.New("scripted model does not allow concurrent Generate calls")

// RequestCheck 在某个 Step 返回前检查本次 ModelRequest。
//
// 它适合断言 Agent Loop 是否正确回填历史，例如：
//   - 第二次模型调用必须包含第一次的 Tool Result；
//   - System Prompt 必须存在；
//   - 某个工具是否已经发送给模型。
//
// 返回 error 会让 Generate 立即失败，使测试精确指出“请求上下文不符合预期”。
type RequestCheck func(po.ModelRequest) error

// Step 描述 Scripted Model 的一次预设行为。
//
// 一个 Step 只能二选一：
//   - 返回 response；
//   - 返回 err。
//
// deltas 只允许与成功 response 一起出现，用来模拟流式文本或 Tool Call 增量。
type Step struct {
	name     string
	check    RequestCheck
	deltas   []po.ModelDelta
	response *po.ModelResponse
	err      error
}

// Respond 创建一个成功响应 Step。
func Respond(response po.ModelResponse, deltas ...po.ModelDelta) Step {
	copyOfResponse := response
	return Step{
		response: &copyOfResponse,
		deltas:   append([]po.ModelDelta(nil), deltas...),
	}
}

// Fail 创建一个返回指定错误的 Step。
func Fail(err error) Step {
	return Step{err: err}
}

// Named 给 Step 增加便于排错的名字。
// 当脚本很长时，错误信息会指出具体是哪一步不合法。
func Named(name string, step Step) Step {
	step.name = name
	return step
}

// WithCheck 给 Step 增加请求断言。
func WithCheck(step Step, check RequestCheck) Step {
	step.check = check
	return step
}

// Reply 是最常用的便利函数：构造一个正常结束的文本响应。
func Reply(messageID, text string, usage po.Usage) (Step, error) {
	message, err := po.NewAssistantMessage(messageID, po.TextPart(text))
	if err != nil {
		return Step{}, err
	}
	response, err := po.NewModelResponse(
		message,
		po.ModelStopEndTurn,
		usage,
		"scripted-"+messageID,
	)
	if err != nil {
		return Step{}, err
	}
	return Respond(response), nil
}

// StreamText 构造一个会依次发送文本 Delta、最后返回完整文本的 Step。
func StreamText(messageID string, chunks []string, usage po.Usage) (Step, error) {
	fullText := ""
	deltas := make([]po.ModelDelta, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk == "" {
			return Step{}, fmt.Errorf("stream chunk cannot be empty")
		}
		fullText += chunk
		deltas = append(deltas, po.ModelDelta{
			Kind:         po.ModelDeltaText,
			ContentIndex: 0,
			Text:         chunk,
		})
	}

	step, err := Reply(messageID, fullText, usage)
	if err != nil {
		return Step{}, err
	}
	step.deltas = deltas
	return step, nil
}

// CallTool 构造一个只请求单个工具的响应。
func CallTool(messageID string, callID string, toolName string, arguments any, usage po.Usage) (Step, error) {
	call, err := po.NewToolCall(callID, toolName, arguments)
	if err != nil {
		return Step{}, err
	}
	message, err := po.NewAssistantMessage(messageID, po.ToolCallPart(call))
	if err != nil {
		return Step{}, err
	}
	response, err := po.NewModelResponse(
		message,
		po.ModelStopToolCall,
		usage,
		"scripted-"+messageID,
	)
	if err != nil {
		return Step{}, err
	}
	return Respond(response), nil
}

// Must 把 `(Step, error)` 转成 Step；构造失败时 panic。
//
// 它只适合测试和示例中的静态脚本，因为这些错误属于程序员写错测试数据。
// 真实用户输入、Provider 响应和运行时数据绝不能用 Must 处理。
func Must(step Step, err error) Step {
	if err != nil {
		panic(err)
	}
	return step
}

// Validate 检查 Step 是否有明确、无歧义的行为。
func (s Step) Validate() error {
	hasResponse := s.response != nil
	hasError := s.err != nil

	if hasResponse == hasError {
		return fmt.Errorf("step must contain exactly one response or error")
	}
	if hasError && len(s.deltas) > 0 {
		return fmt.Errorf("error step cannot emit successful deltas")
	}
	if hasResponse {
		if err := s.response.Validate(); err != nil {
			return fmt.Errorf("invalid response: %w", err)
		}
		for index, delta := range s.deltas {
			if err := delta.Validate(); err != nil {
				return fmt.Errorf("delta %d: %w", index, err)
			}
		}
	}
	return nil
}

// ExpectMessageCount 返回一个可复用的请求断言。
func ExpectMessageCount(want int) RequestCheck {
	return func(request po.ModelRequest) error {
		got := len(request.Messages())
		if got != want {
			return fmt.Errorf("message count = %d, want %d", got, want)
		}
		return nil
	}
}

// ExpectLastToolResult 检查请求最后一条消息是否是指定 Tool Call 的结果。
func ExpectLastToolResult(toolCallID string) RequestCheck {
	return func(request po.ModelRequest) error {
		messages := request.Messages()
		if len(messages) == 0 {
			return fmt.Errorf("request has no messages")
		}

		last := messages[len(messages)-1]
		var result po.ToolResultMessage
		switch typed := last.(type) {
		case po.ToolResultMessage:
			result = typed
		case *po.ToolResultMessage:
			if typed == nil {
				return fmt.Errorf("last message is a nil *ToolResultMessage")
			}
			result = *typed
		default:
			return fmt.Errorf("last message kind = %s, want tool_result", last.Kind())
		}

		if result.ToolCallID() != toolCallID {
			return fmt.Errorf(
				"last tool call id = %q, want %q",
				result.ToolCallID(),
				toolCallID,
			)
		}
		return nil
	}
}

// ExpectTool 检查某个 ToolSpec 是否已经提供给模型。
func ExpectTool(name string) RequestCheck {
	return func(request po.ModelRequest) error {
		for _, spec := range request.Tools() {
			if spec.Name() == name {
				return nil
			}
		}
		return fmt.Errorf("tool %q was not included in request", name)
	}
}

// JSONArguments 是测试中构造原始 JSON 参数的便利函数。
// 它会复制输入，避免测试后续修改 Slice 影响已经创建的值。
func JSONArguments(value string) json.RawMessage {
	return json.RawMessage(append([]byte(nil), value...))
}
