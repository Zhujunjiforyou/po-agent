package po

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Run 同步入口：创建一个只包含当前用户消息的初始Transcript
func (a *Agent) Run(ctx context.Context, user UserMessage) (RunResult, error) {
	return a.RunMessages(ctx, []Message{user})
}

// RunMessages 从一份已经存在的 Transcript 快照继续运行。
//
// Session 层使用这个入口把“历史消息 + 新用户消息”交给 Agent Core。
// Agent 不持有Session，也不把历史保存到自己身上，因此同一个 Agent 可以安全服务多个独立 Session。
func (a *Agent) RunMessages(ctx context.Context, messages []Message) (RunResult, error) {
	return a.RunMessagesWithOptions(ctx, messages, RunOptions{})
}

func (a *Agent) RunMessagesWithOptions(ctx context.Context, messages []Message, options RunOptions) (RunResult, error) {
	handle, err := a.StartMessagesWithOptions(ctx, messages, options)
	if err != nil {
		return RunResult{}, err
	}
	return handle.Wait()
}

// Start 是RunHandle版本的便利入口
func (a *Agent) Start(ctx context.Context, user UserMessage) (*RunHandle, error) {
	return a.StartMessages(ctx, []Message{user})
}

func (a *Agent) StartMessages(ctx context.Context, messages []Message) (*RunHandle, error) {
	return a.StartMessagesWithOptions(ctx, messages, RunOptions{})
}

// StartMessages 从已有Transcript启动一个可控制Run，并立即返回RunHandle
// 具体 Message 是 package po 的不可变值对象，因此不需要再做 JSON 往返。
func (a *Agent) StartMessagesWithOptions(ctx context.Context, messages []Message, options RunOptions) (*RunHandle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("run context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRunMessages(messages); err != nil {
		return nil, err
	}
	initialMessages := append([]Message(nil), messages...)
	runCtx, cancelRun := context.WithCancelCause(ctx)
	runID := a.ids.NewID("run")
	control := newRunControl(a.steeringMode, a.followUpMode)
	handle := newRunHandle(runID, runCtx, cancelRun, control)

	stopCloseOnCancel := context.AfterFunc(runCtx, control.close)

	go func() {
		result, err := a.runControlled(runCtx, runID, initialMessages, control, options)
		stopCloseOnCancel()
		control.close()
		handle.complete(result, err)
	}()

	return handle, nil
}

func validateRunMessages(messages []Message) error {
	if len(messages) == 0 {
		return fmt.Errorf("run transcript must contain at least one message")
	}

	for index, message := range messages {
		if message == nil || isNilMessage(message) {
			return fmt.Errorf("run transcript message %d is nil", index)
		}
		if err := message.Validate(); err != nil {
			return fmt.Errorf("run transcript message %d: %w", index, err)
		}
	}

	// 模型调用前最后一个逻辑事实必须来自 User 或 ToolResult。
	// 如果最后是 Assistant，说明缺少“新的用户输入”或“对应 Tool Observation”；
	// 直接 continue 会让很多 Provider 拒绝请求，也会让会话语义变得含糊。
	lastKind := messages[len(messages)-1].Kind()
	if lastKind != MessageUser && lastKind != MessageToolResult {
		return fmt.Errorf("run transcript must end with user or tool_result message, got %q", lastKind)
	}

	return nil
}

// runControlled 是单个Run的唯一状态所有者
func (a *Agent) runControlled(runCtx context.Context, runID string, initialMessages []Message,
	control *runControl, options RunOptions) (RunResult, error) {
	state := newRunState(runID, time.Now(), initialMessages)
	tools := a.tools.Snapshot()
	a.emit(runCtx, RunStartEvent{RunID: runID})

	finish := func(finalText string, reason RunStopReason, runErr error) (RunResult, error) {
		// 先停止接受新的控制消息，再发布 terminal event。
		// 一旦外部观察到 RunEnd，就不会存在一条“已经接受、却永远不会处理”的 steer。
		control.close()

		state.finish(time.Now())
		result := resultFromState(state, finalText, reason)

		// Run Context 可能已经因为 Abort/Timeout 结束，但 terminal event 仍然必须可见。
		eventCtx := context.WithoutCancel(runCtx)
		a.emit(eventCtx, RunEndEvent{RunID: runID, StopReason: reason, Err: runErr})
		return result, runErr
	}
	for {
		state.beginTurn()
		turnID := a.ids.NewID("turn")
		a.emit(runCtx, TurnStartEvent{RunID: runID, TurnID: turnID, Turn: state.turnAttempts})

		turnEnded := false
		endTurn := func() {
			if turnEnded {
				return
			}
			turnEnded = true
			a.emit(runCtx, TurnEndEvent{RunID: runID, TurnID: turnID, Turn: state.turnAttempts})
		}
		modelMessages := append([]Message(nil), state.messages...)
		if options.ContextBuilder != nil {
			view, err := options.ContextBuilder.Build(runCtx, ContextBuildInput{
				SystemPrompt:    a.systemPrompt,
				Messages:        modelMessages,
				Tools:           tools.Specs(),
				Model:           a.model.Info(),
				MaxOutputTokens: a.maxOutputTokens,
			})
			if err != nil {
				endTurn()
				return finish("", RunStopFailed, fmt.Errorf("build model context: %w", err))
			}
			modelMessages = view.Messages
		}

		request, err := NewModelRequest(a.systemPrompt, modelMessages, tools.Specs(), a.maxOutputTokens)
		if err != nil {
			endTurn()
			return finish("", RunStopFailed, fmt.Errorf("build model request: %w", err))
		}

		response, err := a.generateModel(runCtx, runID, turnID, request)
		if err != nil {
			endTurn()
			reason := stopReasonFromContextError(err)
			return finish("", reason, fmt.Errorf("generate model response: %w", err))
		}

		state.addUsage(response.Usage())
		state.completeTurn(turnID, response)

		assistant := response.Message()
		state.append(assistant)
		toolCalls := assistant.ToolCalls()
		if err := validateUniqueToolCallIDs(toolCalls); err != nil {
			endTurn()
			return finish(assistant.Text(), RunStopFailed, err)
		}

		switch response.StopReason() {
		case ModelStopEndTurn:
			endTurn()

			// 模型本来准备自然结束。此时 Steering 优先，其次 Follow-up。
			// takeNextAtNaturalStop 把“检查空队列”和“关闭 Run 控制面”放在同一把锁内，
			// 因此不会丢掉一个并发提交且已经返回 nil 的控制消息。
			queued, closed := control.takeNextAtNaturalStop()
			if closed {
				return finish(assistant.Text(), RunStopCompleted, nil)
			}

			// 原本自然结束的 Run 只有因为 queued message 才需要开启下一 Turn。
			// 此时再询问 ShouldStopAfterTurn，避免 Budget/Guard 被 Steering 绕过。
			if reason, stop, err := a.stopAfterTurn(runCtx, state, turnID, assistant, nil); err != nil {
				return finish(assistant.Text(), stopReasonFromContextError(err), err)
			} else if stop {
				return finish(assistant.Text(), reason, nil)
			}

			appendQueuedUserMessages(state, queued)
			continue

		case ModelStopLength:
			if len(toolCalls) == 0 {
				endTurn()

				queued, closed := control.takeNextAtNaturalStop()
				if closed {
					return finish(assistant.Text(), RunStopModelLength, nil)
				}

				if reason, stop, err := a.stopAfterTurn(runCtx, state, turnID, assistant, nil); err != nil {
					return finish(assistant.Text(), stopReasonFromContextError(err), err)
				} else if stop {
					return finish(assistant.Text(), reason, nil)
				}

				appendQueuedUserMessages(state, queued)
				continue
			}

			outcomes, err := a.failToolCalls(
				toolCalls,
				"tool call was not executed because the model output was truncated; re-issue the call with complete arguments",
			)
			if err != nil {
				endTurn()
				return finish(assistant.Text(), RunStopFailed, err)
			}
			state.append(messagesFromOutcomes(outcomes)...)
			endTurn()

			if reason, stop, err := a.stopAfterTurn(runCtx, state, turnID, assistant, outcomes); err != nil {
				return finish(assistant.Text(), stopReasonFromContextError(err), err)
			} else if stop {
				return finish(assistant.Text(), reason, nil)
			}

			// 这一分支本来就会因为“截断 ToolCall 的错误 Observation”继续请求模型。
			// Steering 可以在下一轮前插队；Follow-up 仍然等待 Agent 真正准备结束。
			appendQueuedUserMessages(state, control.takeSteering())
			continue

		case ModelStopToolCall:
			batchSnapshot := state.snapshot(turnID)
			outcomes, requestedStop, err := a.executeToolCalls(runCtx, batchSnapshot, tools, toolCalls)
			if err != nil {
				endTurn()
				return finish("", stopReasonFromContextError(err), err)
			}

			state.append(messagesFromOutcomes(outcomes)...)
			for _, outcome := range outcomes {
				if outcome.counted {
					state.reserveToolCalls(1)
				}
			}
			endTurn()

			// StopReason 来自执行前 Guard，是明确的 Run-level stop；它比 queued user
			// control 优先，不允许 Steering 绕过一个已经做出的 Runtime 停止决策。
			if requestedStop != "" {
				return finish(joinToolOutcomeText(outcomes), requestedStop, nil)
			}

			if reason, stop, err := a.stopAfterTurn(runCtx, state, turnID, assistant, outcomes); err != nil {
				return finish("", stopReasonFromContextError(err), err)
			} else if stop {
				return finish(joinToolOutcomeText(outcomes), reason, nil)
			}

			steering := control.takeSteering()
			if len(steering) > 0 {
				appendQueuedUserMessages(state, steering)
				continue
			}

			if !allOutcomesTerminate(outcomes) {
				// 普通 Tool batch 会自动进入下一次模型调用，因此 Follow-up 暂时不消费。
				continue
			}

			// terminate=true 只跳过自动 Tool follow-up，不应该吞掉用户已经排队的显式
			// Steering / Follow-up。这里把它视为“本来准备自然结束”的边界。
			queued, closed := control.takeNextAtNaturalStop()
			if closed {
				return finish(joinToolOutcomeText(outcomes), RunStopToolTerminated, nil)
			}

			appendQueuedUserMessages(state, queued)
			continue

		default:
			endTurn()
			return finish("", RunStopFailed, fmt.Errorf("unsupported model stop reason %q", response.StopReason()))
		}
	}
}

func appendQueuedUserMessages(state *runState, messages []UserMessage) {
	for _, message := range messages {
		state.append(message)
	}
}

// runControlled 是单个run的唯一状态所有者
// 外部goroutine只能往runControl里排队消息或者取消Context

func (a *Agent) generateModel(runCtx context.Context, runID, turnID string, request ModelRequest) (ModelResponse, error) {
	a.emit(runCtx, MessageStartEvent{RunID: runID, TurnID: turnID})

	response, err := a.model.Generate(runCtx, request, a.modelDeltaEmitter(runID, turnID))
	if err != nil {
		if cause := context.Cause(runCtx); cause != nil {
			return ModelResponse{}, cause
		}
		return ModelResponse{}, err
	}
	if cause := context.Cause(runCtx); cause != nil {
		return ModelResponse{}, cause
	}

	a.emit(runCtx, MessageEndEvent{RunID: runID, TurnID: turnID, Message: response.Message()})
	return response, nil
}

func (a *Agent) modelDeltaEmitter(runID, turnID string) DeltaEmitter {
	return func(ctx context.Context, delta ModelDelta) error {
		if err := EmitModelDelta(ctx, a.emitModelDelta, delta); err != nil {
			return err
		}
		a.emit(ctx, MessageUpdateEvent{RunID: runID, TurnID: turnID, Delta: delta})
		return nil
	}
}

func (a *Agent) stopAfterTurn(
	ctx context.Context,
	state *runState,
	turnID string,
	assistant AssistantMessage,
	outcomes []toolCallOutcome,
) (RunStopReason, bool, error) {
	if a.shouldStopAfterTurn == nil {
		return "", false, nil
	}

	toolResults := make([]ToolResultMessage, 0, len(outcomes))
	for _, outcome := range outcomes {
		toolResults = append(toolResults, outcome.message)
	}

	reason, stop, err := a.shouldStopAfterTurn(ctx, TurnCompletedContext{
		Run:         state.snapshot(turnID),
		Assistant:   assistant,
		ToolResults: toolResults,
	})
	if err != nil {
		return "", false, fmt.Errorf("should stop after turn: %w", err)
	}
	if stop && reason == "" {
		reason = RunStopStopped
	}
	return reason, stop, nil
}

type toolCallOutcome struct {
	message    ToolResultMessage
	terminate  bool
	stopReason RunStopReason
	counted    bool
}

type preparedToolCall struct {
	batchIndex int
	batchSize  int
	call       ToolCall
	tool       Tool
	spec       ToolSpec
}

// completedToolCall 是worker goroutine发回协调器的完成信号
type completedToolCall struct {
	index   int
	outcome toolCallOutcome
	err     error
}

// executeToolCalls 是tool batch唯一的入口
func (a *Agent) executeToolCalls(ctx context.Context, run RunSnapshot, tools ToolSnapshot,
	calls []ToolCall) ([]toolCallOutcome, RunStopReason, error) {
	if a.toolExecution == ToolExecutionSequential {
		return a.executeToolCallsSequential(ctx, run, tools, calls)
	}
	return a.executeToolCallsParallel(ctx, run, tools, calls)
}

func (a *Agent) executeToolCallsSequential(ctx context.Context, run RunSnapshot, tools ToolSnapshot,
	calls []ToolCall) ([]toolCallOutcome, RunStopReason, error) {
	outcomes := make([]toolCallOutcome, 0, len(calls))
	for index, call := range calls {
		prepared, immediate, err := a.prepareToolCall(ctx, run, index, len(calls), tools, call)
		if err != nil {
			return nil, "", err
		}
		if immediate != nil {
			outcomes = append(outcomes, *immediate)
			if immediate.stopReason == "" {
				continue
			}
			// Run-stopping Hook 明确要求从这个副作用边界停止
			remaining, err := a.failToolCalls(
				calls[index+1:],
				fmt.Sprintf("toolcall was not executed because a tool hook requested run stop (%s)", immediate.stopReason),
			)
			if err != nil {
				return nil, "", err
			}
			outcomes = append(outcomes, remaining...)
			return outcomes, immediate.stopReason, nil
		}

		// ToolStart 只表示真正准备进入execute，unknown tool、schema failure和policy block都不会产生虚假的started lifecycle
		a.emitToolStart(ctx, run, *prepared)
		outcome, err := a.executeStartedToolCall(ctx, run, *prepared)
		if err != nil {
			return nil, "", err
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, "", nil
}

// executeToolCallsParallel 实现默认的批次语义：
//
//  1. 所有 ToolCall 先按 source order 顺序 preflight；
//  2. 如果没有 Run-stopping Hook，允许执行的 Tool 全部进入 runnable 集合；
//  3. ToolStartEvent 仍按 source order 发出；
//  4. 真正 Execute 并发发生；ToolEndEvent 因而按真实完成顺序出现；
//  5. 最终 outcomes 按 batchIndex 回填，Transcript 仍保持 source order。
//
// 这和“给旧 for 循环简单加 go”有本质区别：副作用前的 Policy/Guard 仍然具有
// 可推理的确定顺序，而 UI 又能看到真实完成时间。
func (a *Agent) executeToolCallsParallel(
	ctx context.Context,
	run RunSnapshot,
	tools ToolSnapshot,
	calls []ToolCall,
) ([]toolCallOutcome, RunStopReason, error) {
	outcomes := make([]toolCallOutcome, len(calls))
	prepared := make([]preparedToolCall, 0, len(calls))

	// Phase 1: deterministic preflight. BeforeToolCall 可能连接 Approval、Budget Guard
	// 或审计逻辑，因此这里故意不并发调用它们。
	for index, call := range calls {
		item, immediate, err := a.prepareToolCall(ctx, run, index, len(calls), tools, call)
		if err != nil {
			return nil, "", err
		}
		if immediate == nil {
			prepared = append(prepared, *item)
			continue
		}

		outcomes[index] = *immediate
		if immediate.stopReason == "" {
			// 普通 Block / Unknown Tool / Schema Error 只是当前 Call 的 Observation；
			// 它不应该阻止同批其他独立 Tool 获取有价值的 Observation。
			continue
		}

		// Po 的 BeforeToolCall 比 Pi 的普通 block 多一个可选 StopReason，用于 guard。
		// Parallel 模式下 preflight 尚未产生任何真实 Tool 副作用，因此一旦出现
		// Run-stop 时不启动本批任何已准备 Tool，避免执行一半后再停止。
		message := fmt.Sprintf("tool call was not executed because batch preflight requested run stop (%s)", immediate.stopReason)
		for _, ready := range prepared {
			blocked, err := a.errorToolOutcome(ready.call, message, "", false)
			if err != nil {
				return nil, "", err
			}
			outcomes[ready.batchIndex] = blocked
		}
		for remaining := index + 1; remaining < len(calls); remaining++ {
			blocked, err := a.errorToolOutcome(calls[remaining], message, "", false)
			if err != nil {
				return nil, "", err
			}
			outcomes[remaining] = blocked
		}
		return outcomes, immediate.stopReason, nil
	}

	if len(prepared) == 0 {
		return outcomes, "", nil
	}

	// ToolStart 由协调器按 source order 发出，而不是让 worker goroutine 自己 emit。
	// 否则仅仅因为 goroutine 调度不同，CLI 看到的“开始顺序”也会变成不确定。
	for _, item := range prepared {
		a.emitToolStart(ctx, run, item)
	}

	// 所有 worker 共享一个 batch Context。第一个 Runtime 级 fatal error 出现后，
	// coordinator 会 cancelBatch(err)，让仍在执行的兄弟 Tool 有机会尽快退出。
	// 普通 Tool 业务 error 已经在 executeStartedToolCall 中转成 ToolResult，不会走这里。
	batchCtx, cancelBatch := context.WithCancelCause(ctx)
	defer cancelBatch(nil)

	completions := make(chan completedToolCall, len(prepared))
	for _, item := range prepared {
		go func(item preparedToolCall) {
			outcome, err := a.executeStartedToolCall(batchCtx, run, item)
			completions <- completedToolCall{index: item.batchIndex, outcome: outcome, err: err}
		}(item)
	}

	var firstErr error
	for range len(prepared) {
		completed := <-completions
		if completed.err != nil {
			// 只让第一个 fatal error 成为 Run 的主错误。后续兄弟 Tool 很可能只是因为
			// batch Context 已被取消而返回同一个 Cause；覆盖 firstErr 会丢掉根因。
			if firstErr == nil {
				firstErr = completed.err
				cancelBatch(completed.err)
			}
			continue
		}
		outcomes[completed.index] = completed.outcome
	}

	if firstErr != nil {
		return nil, "", firstErr
	}
	return outcomes, "", nil
}

// prepareToolCall 只做“副作用前”的工作，不发 ToolStart，也绝不调用 Tool.Execute。
// 返回 immediate != nil 表示当前 Call 已经有模型可见结果，无需真实执行。
func (a *Agent) prepareToolCall(
	ctx context.Context,
	run RunSnapshot,
	batchIndex, batchSize int,
	tools ToolSnapshot,
	call ToolCall,
) (*preparedToolCall, *toolCallOutcome, error) {
	if cause := context.Cause(ctx); cause != nil {
		return nil, nil, cause
	}

	tool, ok := tools.Lookup(call.Name)
	if !ok {
		outcome, err := a.errorToolOutcome(call, fmt.Sprintf("tool %q is not available", call.Name), "", true)
		return nil, &outcome, err
	}

	spec := tool.Spec()
	if err := ValidateToolCallArguments(a.validator, spec, call); err != nil {
		outcome, buildErr := a.errorToolOutcome(call, err.Error(), "", true)
		return nil, &outcome, buildErr
	}

	if a.beforeToolCall != nil {
		decision, err := a.beforeToolCall(ctx, BeforeToolCallContext{
			Run:        run.Clone(),
			BatchIndex: batchIndex,
			BatchSize:  batchSize,
			Call:       call.Clone(),
			Spec:       spec.Clone(),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("before tool call: %w", err)
		}
		if decision.StopReason != "" && !decision.Block {
			return nil, nil, fmt.Errorf("before tool call returned stop reason without blocking the tool")
		}
		if decision.Block {
			reason := decision.Reason
			if reason == "" {
				reason = "tool execution was blocked"
			}

			// 普通 Policy Block 已经消费了一次模型 ToolCall，因此计入 ToolCalls；
			// Run-stopping Guard 是在副作用前拒绝整个运行继续，不把这个 Call 记作已处理。
			counted := decision.StopReason == ""
			outcome, buildErr := a.errorToolOutcome(call, reason, decision.StopReason, counted)
			return nil, &outcome, buildErr
		}
	}

	return &preparedToolCall{
		batchIndex: batchIndex,
		batchSize:  batchSize,
		call:       call.Clone(),
		tool:       tool,
		spec:       spec.Clone(),
	}, nil, nil
}

func (a *Agent) emitToolStart(ctx context.Context, run RunSnapshot, prepared preparedToolCall) {
	a.emit(ctx, ToolStartEvent{
		RunID:      run.RunID,
		TurnID:     run.TurnID,
		ToolCallID: prepared.call.ID,
		ToolName:   prepared.call.Name,
	})
}

// executeStartedToolCall 从“ToolStart 已经可见”开始，负责闭合整个 Tool 生命周期。
// Parallel 和 Sequential 都复用这一个实现，因此错误归一化、ToolUpdate、AfterHook、
// ToolEnd 的语义不会因为执行模式不同而分叉成两套代码。
func (a *Agent) executeStartedToolCall(ctx context.Context, run RunSnapshot, prepared preparedToolCall) (toolCallOutcome, error) {
	call := prepared.call

	// 从 ToolStartEvent 可见开始，下面任何返回路径都必须产生一次 ToolEndEvent。
	// Result 可能是零值，例如 Run Cancel / Tool Timeout 发生时并没有模型可见结果。
	emitEnd := func(result ToolResult, isError bool, eventErr error) {
		a.emit(ctx, ToolEndEvent{
			RunID:      run.RunID,
			TurnID:     run.TurnID,
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Result:     result.Clone(),
			IsError:    isError,
			Err:        eventErr,
		})
	}

	// 每个并发 Tool 都有自己独立的 update gate，因此 A Tool 的 Progress 背压不会
	// 持有 B Tool 的 gate 锁；它们只会在共享 EventHub.emitMu 处按事件序列化展示。
	updateGate := newToolUpdateGate(func(updateCtx context.Context, update ToolUpdate) error {
		a.emit(updateCtx, ToolUpdateEvent{
			RunID:      run.RunID,
			TurnID:     run.TurnID,
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Update:     update.Clone(),
		})
		return nil
	})

	result, executeErr := executePreparedTool(ctx, prepared.tool, call, updateGate.Emit)
	updateGate.Close()

	isError := false
	if executeErr != nil {
		if cause := context.Cause(ctx); cause != nil {
			emitEnd(ToolResult{}, false, cause)
			return toolCallOutcome{}, cause
		}
		if errors.Is(executeErr, ErrToolTimeout) {
			emitEnd(ToolResult{}, false, executeErr)
			return toolCallOutcome{}, executeErr
		}

		// 普通 Tool 业务失败仍然是模型应该看到的 Observation，而不是整个 Batch 的
		// fatal error。因此把 error 转成 IsError=true 的 ToolResult，兄弟 Tool 继续执行。
		errorResult, err := NewTextToolResult(executeErr.Error(), nil, false)
		if err != nil {
			wrapped := fmt.Errorf("build tool error result: %w", err)
			emitEnd(ToolResult{}, true, wrapped)
			return toolCallOutcome{}, wrapped
		}
		result = errorResult
		isError = true
	}

	if err := result.Validate(); err != nil {
		wrapped := fmt.Errorf("tool returned invalid result: %w", err)
		emitEnd(result, isError, wrapped)
		return toolCallOutcome{}, wrapped
	}

	if a.afterToolCall != nil {
		nextResult, nextIsError, err := a.afterToolCall(ctx, AfterToolCallContext{
			Run:        run.Clone(),
			BatchIndex: prepared.batchIndex,
			BatchSize:  prepared.batchSize,
			Call:       call.Clone(),
			Spec:       prepared.spec.Clone(),
			Result:     result.Clone(),
			IsError:    isError,
		})
		if err != nil {
			// Tool 此时已经产生过真实副作用，因此 AfterHook 失败只能让 Run 失败，
			// 绝不能把它解释成“Tool 没执行，再重试一次”。
			wrapped := fmt.Errorf("after tool call: %w", err)
			emitEnd(result, isError, wrapped)
			return toolCallOutcome{}, wrapped
		}
		if err := nextResult.Validate(); err != nil {
			wrapped := fmt.Errorf("after tool call returned invalid result: %w", err)
			emitEnd(result, isError, wrapped)
			return toolCallOutcome{}, wrapped
		}
		result, isError = nextResult, nextIsError
	}

	message, err := NewToolResultMessage(
		a.ids.NewID("tool-result"), call.ID, call.Name, isError, result.Content()...,
	)
	if err != nil {
		wrapped := fmt.Errorf("build tool result message: %w", err)
		emitEnd(result, isError, wrapped)
		return toolCallOutcome{}, wrapped
	}

	// ToolEnd 在 worker 内部、完成信号发送给 coordinator 之前 emit。因此 Parallel
	// 模式的 ToolEndEvent 反映实际完成顺序；但 outcome 之后会按 batchIndex 回填。
	emitEnd(result, isError, nil)
	return toolCallOutcome{message: message, terminate: result.Terminate(), counted: true}, nil
}

func executePreparedTool(ctx context.Context, tool Tool, call ToolCall, emit ToolUpdateEmitter) (result ToolResult, err error) {
	// Tool 是扩展边界，第三方 Tool 不应该因为 panic 把整个 Agent 进程打崩。
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: tool %q panicked: %v", ErrToolExecution, call.Name, recovered)
			result = ToolResult{}
		}
	}()
	return tool.Execute(ctx, call, emit)
}

// toolUpdateGate 保证 ToolEnd 以后不会再出现同一 ToolCall 的迟到 Progress。
// Parallel 模式下每个 ToolCall 都拥有自己的 gate。
type toolUpdateGate struct {
	mu     sync.Mutex
	closed bool
	emit   ToolUpdateEmitter
}

func newToolUpdateGate(emit ToolUpdateEmitter) *toolUpdateGate {
	return &toolUpdateGate{emit: emit}
}

// Emit 校验 Context 和 ToolUpdate，并在 gate 仍开放时同步转发。
//
// Late update（Execute 已返回后才来的更新）直接忽略并返回 nil。它通常来自
// Tool 自己遗留的后台 goroutine；Runtime 的职责是维持事件状态机正确，而不是
// 让这个迟到更新反过来把已经完成的 Tool 标记成失败。开发期如果希望发现这类
// Tool bug，可以在以后 Observability 层增加 debug log。
func (g *toolUpdateGate) Emit(ctx context.Context, update ToolUpdate) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.closed {
		return nil
	}
	return EmitToolUpdate(ctx, g.emit, update)
}

func (g *toolUpdateGate) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

func (a *Agent) errorToolOutcome(call ToolCall, message string, stopReason RunStopReason, counted bool) (toolCallOutcome, error) {
	result, err := NewTextToolResult(message, nil, false)
	if err != nil {
		return toolCallOutcome{}, fmt.Errorf("build tool error result: %w", err)
	}
	resultMessage, err := NewToolResultMessage(
		a.ids.NewID("tool-result"), call.ID, call.Name, true, result.Content()...,
	)
	if err != nil {
		return toolCallOutcome{}, fmt.Errorf("build tool error message: %w", err)
	}
	return toolCallOutcome{
		message:    resultMessage,
		stopReason: stopReason,
		counted:    counted,
	}, nil
}

func (a *Agent) failToolCalls(calls []ToolCall, reason string) ([]toolCallOutcome, error) {
	outcomes := make([]toolCallOutcome, 0, len(calls))
	for _, call := range calls {
		outcome, err := a.errorToolOutcome(call, reason, "", false)
		if err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func messagesFromOutcomes(outcomes []toolCallOutcome) []Message {
	messages := make([]Message, 0, len(outcomes))
	for _, outcome := range outcomes {
		messages = append(messages, outcome.message)
	}
	return messages
}

func validateUniqueToolCallIDs(calls []ToolCall) error {
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if _, exists := seen[call.ID]; exists {
			return fmt.Errorf("duplicate tool call id %q", call.ID)
		}
		seen[call.ID] = struct{}{}
	}
	return nil
}

func allOutcomesTerminate(outcomes []toolCallOutcome) bool {
	if len(outcomes) == 0 {
		return false
	}
	for _, outcome := range outcomes {
		if !outcome.terminate {
			return false
		}
	}
	return true
}

func joinToolOutcomeText(outcomes []toolCallOutcome) string {
	var builder strings.Builder
	for index, outcome := range outcomes {
		if index > 0 {
			builder.WriteByte('\n')
		}
		for _, part := range outcome.message.Parts() {
			if part.Type == ContentText {
				builder.WriteString(part.Text)
			}
		}
	}
	return builder.String()
}

func stopReasonFromContextError(err error) RunStopReason {
	switch {
	case errors.Is(err, ErrRunAborted):
		return RunStopAborted
	case errors.Is(err, ErrRunTimeout):
		return RunStopRunTimeout
	case errors.Is(err, ErrModelTimeout):
		return RunStopModelTimeout
	case errors.Is(err, ErrToolTimeout):
		return RunStopToolTimeout
	case errors.Is(err, context.Canceled):
		return RunStopCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return RunStopRunTimeout
	default:
		return RunStopFailed
	}
}
