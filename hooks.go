package po

import "context"

// RunSnapshot 是提供给钩子的当前运行状态只读快照。它只包含事实而不包含策略，因此扩展
// 可以读取它，Agent 无需理解读取原因。
type RunSnapshot struct {
	RunID     string
	TurnID    string
	Turn      int
	Messages  []Message
	Usage     Usage
	ToolCalls int
}

// BeforeToolCallContext 描述调用 Tool.Execute 前已经校验的工具调用。
type BeforeToolCallContext struct {
	Run        RunSnapshot
	BatchIndex int
	BatchSize  int
	Call       ToolCall
	Spec       ToolSpec
}

// BeforeToolCallDecision 是工具执行前钩子返回的常规决策。StopReason 是可选字段，只有
// Block 为 true 时才有意义。扩展可以在拒绝当前批次后终止运行，而无需让 Agent 核心
// 依赖预算、审批或其他策略特有的停止原因。
type BeforeToolCallDecision struct {
	Block      bool
	Reason     string
	StopReason RunStopReason
}

type BeforeToolCallHook func(context.Context, BeforeToolCallContext) (BeforeToolCallDecision, error)

// AfterToolCallContext 描述工具执行后、转换成模型可见的 ToolResultMessage 前的结果。
type AfterToolCallContext struct {
	Run        RunSnapshot
	BatchIndex int
	BatchSize  int
	Call       ToolCall
	Spec       ToolSpec
	Result     ToolResult
	IsError    bool
}

type AfterToolCallHook func(context.Context, AfterToolCallContext) (ToolResult, bool, error)

// TurnCompletedContext 会在一轮正常完成后、循环准备开始下一次模型调用前传给
// ShouldStopAfterTurn。
type TurnCompletedContext struct {
	Run         RunSnapshot
	Assistant   AssistantMessage
	ToolResults []ToolResultMessage
}

// ShouldStopAfterTurnHook 是一种循环扩展点：核心持有循环机制，可选防护组件决定是否开始
// 下一轮。
type ShouldStopAfterTurnHook func(context.Context, TurnCompletedContext) (RunStopReason, bool, error)
