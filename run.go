package po

import (
	"fmt"
	"time"
)

// RunStopReason 描述“整个 Agent Run 为什么停止”。
//
// 注意它与 ModelStopReason 不在同一层：
//   - ModelStopReason 只解释一次 Model.Generate 为什么结束；
//   - RunStopReason 解释整个 Agent 循环为什么结束。
//
// Core 只定义自身稳定的停止语义。预算、no-progress 等扩展可以直接定义自己的
// RunStopReason 常量，因为底层类型是 string，而无需把具体策略塞回 Agent Core。
type RunStopReason string

const (
	RunStopCompleted      RunStopReason = "completed"
	RunStopToolTerminated RunStopReason = "tool_terminated"
	RunStopModelLength    RunStopReason = "model_length"
	RunStopFailed         RunStopReason = "failed"
	RunStopStopped        RunStopReason = "stopped"
	RunStopAborted        RunStopReason = "aborted"

	// RunStopCancelled 表示父 Context 被主动取消。
	// 最典型场景是 CLI 中用户按 Ctrl+C。
	RunStopCancelled RunStopReason = "cancelled"

	// RunStopRunTimeout 表示整个 Run Deadline 到达。
	RunStopRunTimeout RunStopReason = "run_timeout"

	// RunStopModelTimeout 表示某一次模型调用超过 Model Timeout。
	RunStopModelTimeout RunStopReason = "model_timeout"

	// RunStopToolTimeout 表示某个工具调用超过 Tool Timeout。
	RunStopToolTimeout RunStopReason = "tool_timeout"
)

// RunResult 是一次 Agent.Run 的稳定结果快照。
//
// messages 保存完整 Transcript；finalText 只保存适合直接显示给用户的最终文本。
// 两者分开非常重要：一个 Run 即使最终文本很短，也可能已经产生大量工具事实消息。
type RunResult struct {
	runID    string
	messages []Message
	// Session 在 messages 只含有界上下文投影时设置 transcriptPrefix。
	// Messages 可据此延迟重建完整逻辑 Transcript，既保持原有 API，
	// 也避免每一轮都执行一次 O(history) 复制。
	transcriptPrefix []Message
	finalText        string
	stopReason       RunStopReason
	usage            Usage

	turnAttempts int
	toolCalls    int
	turns        []TurnRecord
	initialCount int

	startedAt  time.Time
	finishedAt time.Time
}

// resultFromState 从内部可变 runState 创建对外快照。
func resultFromState(state *runState, finalText string, reason RunStopReason) RunResult {
	turns := append([]TurnRecord(nil), state.turns...)
	return RunResult{
		runID: state.runID,
		// Run 完成后把 state.messages 的所有权交给不可变结果；此后不会再修改它。
		messages:     state.messages,
		finalText:    finalText,
		stopReason:   reason,
		usage:        state.usage,
		turnAttempts: state.turnAttempts,
		toolCalls:    state.toolCalls,
		turns:        turns,
		initialCount: state.initialCount,
		startedAt:    state.startedAt,
		finishedAt:   state.finishedAt,
	}
}

// RunID 返回这次运行的稳定关联 ID。日志、事件、Session 都会逐步复用它。
func (r RunResult) RunID() string { return r.runID }

// Messages 返回 Transcript 顶层 Slice 的副本，避免调用者覆盖结果内部的消息顺序。
func (r RunResult) Messages() []Message {
	if r.transcriptPrefix != nil && r.initialCount >= 0 && r.initialCount <= len(r.messages) {
		delta := r.messages[r.initialCount:]
		messages := make([]Message, 0, len(r.transcriptPrefix)+len(delta))
		messages = append(messages, r.transcriptPrefix...)
		messages = append(messages, delta...)
		return messages
	}
	return append([]Message(nil), r.messages...)
}

// WithTranscriptPrefix 让 Messages 使用不可变前缀和本次 Run 新产生的事实重建完整逻辑
// Transcript。Session 从有界上下文投影启动 Run 后使用该方法；之后不能修改前缀元素及顺序。
func (r RunResult) WithTranscriptPrefix(prefix []Message) RunResult {
	r.transcriptPrefix = prefix
	return r
}

// FinalText 返回适合直接展示给用户的最终文本。
func (r RunResult) FinalText() string { return r.finalText }

// StopReason 返回 Runtime 最终选择停止的原因，而不是最后一次模型调用的 StopReason。
func (r RunResult) StopReason() RunStopReason { return r.stopReason }

// Usage 返回整个 Run 聚合后的模型用量。
func (r RunResult) Usage() Usage { return r.usage }

// TurnAttempts 返回 Agent Loop 已开始过多少个逻辑 Turn。
// Model decorator 内部的 Provider Retry 不会额外增加 Turn。
func (r RunResult) TurnAttempts() int { return r.turnAttempts }

// ToolCalls 返回 Runtime 已实际处理的 Tool Call 数。
// 扩展在副作用边界之前整批停止的调用不会计入这个值。
func (r RunResult) ToolCalls() int { return r.toolCalls }

// InitialMessageCount 返回构成本次 Run 投影输入的消息数，Session 用它区分临时上下文与新事实。
func (r RunResult) InitialMessageCount() int { return r.initialCount }

// StartedAt / FinishedAt 返回运行时间边界。
func (r RunResult) StartedAt() time.Time  { return r.startedAt }
func (r RunResult) FinishedAt() time.Time { return r.finishedAt }

// Turns 返回已成功获得合法 ModelResponse 的 Turn 记录副本。
func (r RunResult) Turns() []TurnRecord {
	return append([]TurnRecord(nil), r.turns...)
}

// Duration 返回 Run 从开始到结束的墙钟时间。
func (r RunResult) Duration() time.Duration {
	if r.finishedAt.IsZero() || r.startedAt.IsZero() {
		return 0
	}
	return r.finishedAt.Sub(r.startedAt)
}

func (r RunResult) Validate() error {
	if r.runID == "" {
		return fmt.Errorf("run result must contain run id")
	}
	if len(r.messages) == 0 {
		return fmt.Errorf("run result must contain at least one message")
	}
	if r.finishedAt.Before(r.startedAt) {
		return fmt.Errorf("run result finish time cannot be before start time")
	}
	if err := r.usage.Validate(); err != nil {
		return err
	}
	return nil
}

// TurnRecord 是一次产生有效 ModelResponse 的轮次摘要。
type TurnRecord struct {
	ID                 string
	Index              int
	AssistantMessageID string
	StopReason         ModelStopReason
	Usage              Usage
	ToolCalls          int
}

// runState 是一次运行独占的可变状态；Agent 本身只保存可复用依赖和配置。
type runState struct {
	runID      string
	startedAt  time.Time
	finishedAt time.Time

	messages []Message
	usage    Usage

	turnAttempts int
	toolCalls    int
	turns        []TurnRecord
	initialCount int
}

func newRunState(runID string, startedAt time.Time, messages []Message) *runState {
	return &runState{
		runID:     runID,
		startedAt: startedAt,
		// 该切片由 StartMessagesWithOptions 专门为 Run goroutine 创建。
		messages:     messages,
		initialCount: len(messages),
	}
}

// beginTurn 统计智能体的逻辑轮次。模型服务装饰器可以在内部重试传输，而不会增加此计数。
func (s *runState) beginTurn() { s.turnAttempts++ }

func (s *runState) completeTurn(id string, response ModelResponse) {
	s.turns = append(s.turns, TurnRecord{
		ID:                 id,
		Index:              s.turnAttempts,
		AssistantMessageID: response.Message().MessageID(),
		StopReason:         response.StopReason(),
		Usage:              response.Usage(),
		ToolCalls:          len(response.Message().ToolCalls()),
	})
}

func (s *runState) append(messages ...Message) { s.messages = append(s.messages, messages...) }
func (s *runState) addUsage(usage Usage)       { s.usage = s.usage.Add(usage) }
func (s *runState) reserveToolCalls(count int) { s.toolCalls += count }
func (s *runState) finish(at time.Time)        { s.finishedAt = at }

func (s *runState) snapshot(turnID string) RunSnapshot {
	return RunSnapshot{
		RunID:     s.runID,
		TurnID:    turnID,
		Turn:      s.turnAttempts,
		Messages:  append([]Message(nil), s.messages...),
		Usage:     s.usage,
		ToolCalls: s.toolCalls,
	}
}
