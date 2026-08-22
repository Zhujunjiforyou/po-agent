package po

import (
	"context"
	"fmt"
	"reflect"
)

// AgentConfig 只包含可复用的核心机制。预算、重试、审批和超时等工作流策略应通过这些
// 钩子和接口进行组合。
type AgentConfig struct {
	Model Model
	Tools *ToolRegistry

	// Validator 在调用工具钩子或执行工具前校验模型生成的参数。
	Validator SchemaValidator

	SystemPrompt    string
	MaxOutputTokens int

	// IDs 可以注入以便编写确定性测试；为 nil 时使用 AtomicIDGenerator。
	IDs IDGenerator

	// EmitModelDelta 是可选的底层流式增量观察器。
	EmitModelDelta DeltaEmitter

	// ToolExecution 决定一条 AssistantMessage 中多个 ToolCall 的执行方式，默认并行。
	ToolExecution ToolExecutionMode

	// SteeringMode 和 FollowUpMode 控制一次 Turn 边界最多消费多少条排队用户消息。
	SteeringMode ControlQueueMode
	FollowUpMode ControlQueueMode

	BeforeToolCall      BeforeToolCallHook
	AfterToolCall       AfterToolCallHook
	ShouldStopAfterTurn ShouldStopAfterTurnHook
}

// Agent 保存可复用依赖；每次运行的对话记录和计数器保存在 runState 中。
type Agent struct {
	model           Model
	tools           *ToolRegistry
	validator       SchemaValidator
	systemPrompt    string
	maxOutputTokens int
	ids             IDGenerator
	emitModelDelta  DeltaEmitter

	toolExecution ToolExecutionMode

	steeringMode ControlQueueMode
	followUpMode ControlQueueMode

	beforeToolCall      BeforeToolCallHook
	afterToolCall       AfterToolCallHook
	shouldStopAfterTurn ShouldStopAfterTurnHook

	events eventHub
}

func NewAgent(config AgentConfig) (*Agent, error) {
	if isNilInterface(config.Model) {
		return nil, fmt.Errorf("invalid agent: model is required")
	}
	if err := config.Model.Info().Validate(); err != nil {
		return nil, fmt.Errorf("invalid agent: model info: %w", err)
	}
	if config.MaxOutputTokens < 0 {
		return nil, fmt.Errorf("invalid agent: max output tokens cannot be negative")
	}

	// ToolExecution 的零值故意映射到 Parallel：这是默认 Agent Harness
	// 语义，同时不会迫使已有调用方增加一项配置。任何非空未知值
	// 都应该尽早在 NewAgent 失败，而不是运行到 Tool 批次时才默默回退。
	toolExecution := config.ToolExecution
	if toolExecution == "" {
		toolExecution = ToolExecutionParallel
	}
	if err := toolExecution.Validate(); err != nil {
		return nil, fmt.Errorf("invalid agent: %w", err)
	}

	steeringMode := config.SteeringMode
	if steeringMode == "" {
		steeringMode = ControlQueueOneAtATime
	}
	if err := steeringMode.Validate(); err != nil {
		return nil, fmt.Errorf("invalid agent steering mode: %w", err)
	}

	followUpMode := config.FollowUpMode
	if followUpMode == "" {
		followUpMode = ControlQueueOneAtATime
	}
	if err := followUpMode.Validate(); err != nil {
		return nil, fmt.Errorf("invalid agent follow-up mode: %w", err)
	}

	ids := config.IDs
	if isNilInterface(ids) {
		ids = NewAtomicIDGenerator()
	}

	tools := config.Tools
	if tools == nil {
		tools = NewToolRegistry()
	}
	if tools.Snapshot().Len() > 0 && isNilInterface(config.Validator) {
		return nil, fmt.Errorf("invalid agent: validator is required when tools are registered")
	}

	return &Agent{
		model:               config.Model,
		tools:               tools,
		validator:           config.Validator,
		systemPrompt:        config.SystemPrompt,
		maxOutputTokens:     config.MaxOutputTokens,
		ids:                 ids,
		emitModelDelta:      config.EmitModelDelta,
		toolExecution:       toolExecution,
		steeringMode:        steeringMode,
		followUpMode:        followUpMode,
		beforeToolCall:      config.BeforeToolCall,
		afterToolCall:       config.AfterToolCall,
		shouldStopAfterTurn: config.ShouldStopAfterTurn,
	}, nil
}

// isNilInterface 还会识别内部包含有类型 nil 指针、映射、切片、函数或通道的接口值。
// 公共接口边界在调用方法前使用它，避免有类型 nil 延迟触发 panic。
func isNilInterface(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Subscribe 注册一个同步的运行时事件观察器。
func (a *Agent) Subscribe(handler EventHandler) func() { return a.events.subscribe(handler) }

func (a *Agent) emit(ctx context.Context, event AgentEvent) { a.events.emit(ctx, event) }
