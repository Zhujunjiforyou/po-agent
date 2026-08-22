package stream

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
)

var (
	ErrUnknownToolCall    = errors.New("unknown tool call")
	ErrDuplicateToolCall  = errors.New("duplicate tool call")
	ErrToolCallTooLarge   = errors.New("tool call arguments too large")
	ErrIncompleteToolCall = errors.New("incomplete tool call")
	ErrTruncatedToolCalls = errors.New("tool calls from length-truncated response are unsafe")
)

// callBuilder 表示尚未成为正式 po.ToolCall 的 Streaming 状态。
type callBuilder struct {
	id        string
	name      string
	arguments strings.Builder
	done      bool
}

// ToolCalls 管理一次 Assistant Response 中
// 所有正在组装的 Tool Call。
type ToolCalls struct {
	// maxBytes 是“单个 Tool Call arguments”的最大大小。
	maxBytes int

	// byIndex 使用 ContentIndex 找到对应 builder。
	//
	// Tool Call Delta 可以交错到达，
	// 所以不能只有一个 current builder。
	byIndex map[int]*callBuilder
}

func NewToolCalls(maxBytes int) *ToolCalls {
	if maxBytes <= 0 {
		maxBytes = 1 << 20 // 1 MiB
	}
	return &ToolCalls{maxBytes: maxBytes, byIndex: make(map[int]*callBuilder)}
}

// Apply 把一条规范化 ModelDelta 合并进当前 Tool Call 状态。
//
// Text / Thinking Delta 与 Tool Call 组装无关，直接忽略。
func (a *ToolCalls) Apply(delta po.ModelDelta) error {
	// Assembler 位于 Runtime 内部边界，
	// 仍然防御 Provider Adapter 传入非法 Delta。
	if err := delta.Validate(); err != nil {
		return fmt.Errorf("invalid model delta: %w", err)
	}
	switch delta.Kind {
	case po.ModelDeltaToolCallStart:
		if _, exists := a.byIndex[delta.ContentIndex]; exists {
			return ErrDuplicateToolCall
		}
		a.byIndex[delta.ContentIndex] = &callBuilder{id: delta.ToolCallID, name: delta.ToolName}
		return nil
	case po.ModelDeltaToolCallArguments:
		builder, exists := a.byIndex[delta.ContentIndex]
		if !exists {
			return ErrUnknownToolCall
		}
		if builder.done {
			return ErrDuplicateToolCall
		}

		// Provider Adapter 会把某些厂商后续 chunk 缺失的 ID
		// 用之前状态补齐成规范化 ModelDelta。
		//
		// 所以来到 Po ModelDelta 后，ID 应始终稳定一致。
		if builder.id != delta.ToolCallID {
			return fmt.Errorf("%w: tool call id changed from %q to %q", ErrDuplicateToolCall, builder.id, delta.ToolCallID)
		}
		if builder.arguments.Len()+len(delta.ArgumentsDelta) > a.maxBytes {
			return ErrToolCallTooLarge
		}
		builder.arguments.WriteString(delta.ArgumentsDelta)
		return nil
	case po.ModelDeltaToolCallEnd:
		builder, exists := a.byIndex[delta.ContentIndex]
		if !exists {
			return ErrUnknownToolCall
		}
		if builder.done {
			return ErrDuplicateToolCall
		}
		if builder.id != delta.ToolCallID {
			return fmt.Errorf("%w: tool call id changed from %q to %q", ErrDuplicateToolCall, builder.id, delta.ToolCallID)
		}
		builder.done = true
		return nil
	default:
		return nil
	}
}

// Build 把所有完整 fragment 组装成正式 po.ToolCall。
//
// stop 是整个 Assistant Response 的最终停止原因。
func (a *ToolCalls) Build(stop po.ModelStopReason) ([]po.ToolCall, error) {
	// 整个模型 Response 因长度限制被截断：
	// 其中 Tool Call 默认全部不具备安全执行资格。
	if stop == po.ModelStopLength && len(a.byIndex) > 0 {
		return nil, ErrTruncatedToolCalls
	}
	indexes := make([]int, 0, len(a.byIndex))
	for index := range a.byIndex {
		indexes = append(indexes, index)
	}

	// Go map 没有稳定迭代顺序。
	// Tool Call 最终逻辑顺序恢复到 ContentIndex。
	sort.Ints(indexes)
	result := make([]po.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		builder := a.byIndex[index]
		if !builder.done {
			return nil, fmt.Errorf("%w: %s", ErrIncompleteToolCall, builder.id)
		}
		call := po.ToolCall{ID: builder.id, Name: builder.name, Arguments: json.RawMessage(builder.arguments.String())}

		// 这里只验证 ToolCall 协议层：
		//   - ID / Name；
		//   - Arguments 是完整 JSON object。
		//
		// 具体 Tool Schema 仍然由 SchemaValidator 负责。
		if err := call.Validate(); err != nil {
			return nil, fmt.Errorf("tool call %s: %w", builder.id, err)
		}
		result = append(result, call)
	}
	return result, nil
}
