package po_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	"github.com/lemonzjj/po-agent-go/schema/basic"
)

func mustToolBatchStep(t *testing.T, messageID string, calls ...po.ToolCall) scripted.Step {
	t.Helper()

	parts := make([]po.ContentPart, 0, len(calls))
	for _, call := range calls {
		parts = append(parts, po.ToolCallPart(call))
	}
	message, err := po.NewAssistantMessage(messageID, parts...)
	if err != nil {
		t.Fatalf("NewAssistantMessage() error = %v", err)
	}
	response, err := po.NewModelResponse(message, po.ModelStopToolCall, po.Usage{}, "scripted-"+messageID)
	if err != nil {
		t.Fatalf("NewModelResponse() error = %v", err)
	}
	return scripted.Respond(response)
}

func mustCall(t *testing.T, id, name, path string) po.ToolCall {
	t.Helper()
	call, err := po.NewToolCall(id, name, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("NewToolCall() error = %v", err)
	}
	return call
}

func expectLastToolResultOrder(ids ...string) scripted.RequestCheck {
	return func(request po.ModelRequest) error {
		messages := request.Messages()
		if len(messages) < len(ids) {
			return fmt.Errorf("message count = %d, need at least %d", len(messages), len(ids))
		}

		start := len(messages) - len(ids)
		for index, want := range ids {
			result, ok := messages[start+index].(po.ToolResultMessage)
			if !ok {
				return fmt.Errorf("message %d is %T, want ToolResultMessage", start+index, messages[start+index])
			}
			if result.ToolCallID() != want {
				return fmt.Errorf("tool result %d id = %q, want %q", index, result.ToolCallID(), want)
			}
		}
		return nil
	}
}

// TestParallelToolExecutionShowsCompletionOrderButPersistsSourceOrder 验证最重要的并行不变量：
// 运行时事件反映真实完成顺序，模型对话记录则保持助手消息中的原始调用顺序。
func TestParallelToolExecutionShowsCompletionOrderButPersistsSourceOrder(t *testing.T) {
	started := make(chan string, 2)
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})

	toolA := mustReadTool(t, "tool_a", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		started <- "a"
		<-releaseA
		return po.NewTextToolResult("A", nil, false)
	})
	toolB := mustReadTool(t, "tool_b", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		started <- "b"
		<-releaseB
		return po.NewTextToolResult("B", nil, false)
	})

	callA := mustCall(t, "call-a", "tool_a", "a.go")
	callB := mustCall(t, "call-b", "tool_b", "b.go")
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		mustToolBatchStep(t, "assistant-1", callA, callB),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "done", po.Usage{})),
			expectLastToolResultOrder("call-a", "call-b"),
		),
	)

	agent := mustAgent(t, model, mustRegistry(t, toolA, toolB))
	var mu sync.Mutex
	var endOrder []string
	toolEnded := make(chan string, 2)
	unsubscribe := agent.Subscribe(func(_ context.Context, event po.AgentEvent) {
		if end, ok := event.(po.ToolEndEvent); ok {
			mu.Lock()
			endOrder = append(endOrder, end.ToolCallID)
			mu.Unlock()
			toolEnded <- end.ToolCallID
		}
	})
	defer unsubscribe()

	runDone := make(chan error, 1)
	go func() {
		_, err := agent.Run(context.Background(), mustUser(t, "run two tools"))
		runDone <- err
	}()

	// 如果 Runtime 仍然是串行，第二个 Tool 在第一个 release 前永远不会 started。
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatalf("tools did not start concurrently; seen=%v", seen)
		}
	}

	// 故意让 B 先结束，并且真正等到 B 的 ToolEnd 已经可见以后才释放 A。
	// 这样测试不依赖“sleep 20ms 应该够了”这种脆弱时序假设。
	close(releaseB)
	select {
	case got := <-toolEnded:
		if got != "call-b" {
			t.Fatalf("first ToolEnd = %q, want call-b", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for call-b ToolEnd")
	}
	close(releaseA)

	if err := <-runDone; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(endOrder) != 2 || endOrder[0] != "call-b" || endOrder[1] != "call-a" {
		t.Fatalf("ToolEnd order = %v, want [call-b call-a]", endOrder)
	}
}

func TestResourceSchedulerRunsIndependentFilesWhileSameFileWaits(t *testing.T) {
	started := make(chan string, 3)
	releaseReadA := make(chan struct{})
	releaseReadB := make(chan struct{})
	releaseEditA := make(chan struct{})

	makeBlockingTool := func(name string, release <-chan struct{}) po.Tool {
		return mustReadTool(t, name, func(ctx context.Context, _ readArgs, _ po.ToolUpdateEmitter) (po.ToolResult, error) {
			started <- name
			select {
			case <-release:
				return po.NewTextToolResult(name, nil, false)
			case <-ctx.Done():
				return po.ToolResult{}, context.Cause(ctx)
			}
		})
	}

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		mustToolBatchStep(t, "assistant-1",
			mustCall(t, "call-read-a", "read_a", "a.go"),
			mustCall(t, "call-read-b", "read_b", "b.go"),
			mustCall(t, "call-edit-a", "edit_a", "a.go"),
		),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "done", po.Usage{})),
			expectLastToolResultOrder("call-read-a", "call-read-b", "call-edit-a"),
		),
	)
	agent, err := po.NewAgent(po.AgentConfig{
		Model: model,
		Tools: mustRegistry(t,
			makeBlockingTool("read_a", releaseReadA),
			makeBlockingTool("read_b", releaseReadB),
			makeBlockingTool("edit_a", releaseEditA),
		),
		Validator: basic.New(),
		ResourceResolver: func(call po.ToolCall) ([]po.ResourceClaim, error) {
			mode := po.ResourceAccessShared
			path := "file:b.go"
			if call.Name == "read_a" || call.Name == "edit_a" {
				path = "file:a.go"
			}
			if call.Name == "edit_a" {
				mode = po.ResourceAccessExclusive
			}
			return []po.ResourceClaim{
				{Key: "workspace", Mode: po.ResourceAccessShared},
				{Key: path, Mode: mode},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		_, err := agent.Run(ctx, mustUser(t, "run scheduled tools"))
		runDone <- err
	}()

	initial := map[string]bool{}
	for len(initial) < 2 {
		select {
		case name := <-started:
			initial[name] = true
		case <-ctx.Done():
			t.Fatalf("initial tools did not start: %v", initial)
		}
	}
	if !initial["read_a"] || !initial["read_b"] {
		t.Fatalf("initial tools = %v, want read_a and read_b", initial)
	}
	select {
	case name := <-started:
		t.Fatalf("same-file edit started before read completed: %s", name)
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseReadA)
	select {
	case name := <-started:
		if name != "edit_a" {
			t.Fatalf("next started tool = %q, want edit_a", name)
		}
	case <-ctx.Done():
		t.Fatal("edit_a did not start after read_a completed")
	}
	close(releaseEditA)
	close(releaseReadB)
	if err := <-runDone; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestResourceSchedulerDoesNotStartWaiterAfterFatalError(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFailure := make(chan struct{})
	waiterStarted := make(chan struct{}, 1)

	failing := mustReadTool(t, "failing", func(ctx context.Context, _ readArgs, _ po.ToolUpdateEmitter) (po.ToolResult, error) {
		close(firstStarted)
		select {
		case <-releaseFailure:
			return po.ToolResult{}, po.ErrToolTimeout
		case <-ctx.Done():
			return po.ToolResult{}, context.Cause(ctx)
		}
	})
	waiting := mustReadTool(t, "waiting", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		waiterStarted <- struct{}{}
		return po.NewTextToolResult("unexpected", nil, false)
	})
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		mustToolBatchStep(t, "assistant-1",
			mustCall(t, "call-failing", "failing", "a.go"),
			mustCall(t, "call-waiting", "waiting", "a.go"),
		),
	)
	agent, err := po.NewAgent(po.AgentConfig{
		Model:     model,
		Tools:     mustRegistry(t, failing, waiting),
		Validator: basic.New(),
		ResourceResolver: func(call po.ToolCall) ([]po.ResourceClaim, error) {
			mode := po.ResourceAccessShared
			if call.Name == "waiting" {
				mode = po.ResourceAccessExclusive
			}
			return []po.ResourceClaim{{Key: "file:a.go", Mode: mode}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		_, err := agent.Run(ctx, mustUser(t, "run"))
		runDone <- err
	}()
	select {
	case <-firstStarted:
	case <-ctx.Done():
		t.Fatal("failing tool did not start")
	}
	close(releaseFailure)
	select {
	case err := <-runDone:
		if !errors.Is(err, po.ErrToolTimeout) {
			t.Fatalf("Run() error = %v, want ErrToolTimeout", err)
		}
	case <-ctx.Done():
		t.Fatal("run did not stop after the active tool failed")
	}
	select {
	case <-waiterStarted:
		t.Fatal("waiting tool started after the batch had already failed")
	default:
	}
}

func TestResourceResolverArgumentErrorBecomesToolObservation(t *testing.T) {
	var executed atomic.Bool
	tool := mustReadTool(t, "read", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		executed.Store(true)
		return po.NewTextToolResult("unexpected", nil, false)
	})
	checkErrorResult := func(request po.ModelRequest) error {
		messages := request.Messages()
		result, ok := messages[len(messages)-1].(po.ToolResultMessage)
		if !ok {
			return fmt.Errorf("last message is %T, want ToolResultMessage", messages[len(messages)-1])
		}
		if !result.IsError() {
			return errors.New("resource resolution failure is not marked as a tool error")
		}
		return nil
	}
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		mustToolBatchStep(t, "assistant-1", mustCall(t, "call-read", "read", "../outside.go")),
		scripted.WithCheck(scripted.Must(scripted.Reply("assistant-2", "done", po.Usage{})), checkErrorResult),
	)
	agent, err := po.NewAgent(po.AgentConfig{
		Model:     model,
		Tools:     mustRegistry(t, tool),
		Validator: basic.New(),
		ResourceResolver: func(po.ToolCall) ([]po.ResourceClaim, error) {
			return nil, fmt.Errorf("%w: path escapes workspace", po.ErrInvalidToolArguments)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := agent.Run(context.Background(), mustUser(t, "run"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalText() != "done" {
		t.Fatalf("FinalText() = %q, want done", result.FinalText())
	}
	if executed.Load() {
		t.Fatal("tool executed after resource resolution rejected its arguments")
	}
}

func TestParallelPreflightCompletesBeforeAnyToolExecutes(t *testing.T) {
	var preflightCount atomic.Int32
	var badExecution atomic.Bool

	makeTool := func(name string) po.Tool {
		return mustReadTool(t, name, func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
			if preflightCount.Load() != 2 {
				badExecution.Store(true)
			}
			return po.NewTextToolResult(name, nil, false)
		})
	}

	callA := mustCall(t, "call-a", "tool_a", "a.go")
	callB := mustCall(t, "call-b", "tool_b", "b.go")
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		mustToolBatchStep(t, "assistant-1", callA, callB),
		scripted.Must(scripted.Reply("assistant-2", "done", po.Usage{})),
	)

	agent, err := po.NewAgent(po.AgentConfig{
		Model:     model,
		Tools:     mustRegistry(t, makeTool("tool_a"), makeTool("tool_b")),
		Validator: basic.New(),
		BeforeToolCall: func(context.Context, po.BeforeToolCallContext) (po.BeforeToolCallDecision, error) {
			preflightCount.Add(1)
			return po.BeforeToolCallDecision{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := agent.Run(context.Background(), mustUser(t, "run")); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if badExecution.Load() {
		t.Fatal("a tool Execute started before all BeforeToolCall preflights finished")
	}
}

func TestSequentialToolExecutionPreservesHistoricalBehavior(t *testing.T) {
	firstRelease := make(chan struct{})
	started := make(chan string, 2)
	var resolverCalled atomic.Bool

	first := mustReadTool(t, "first", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		started <- "first"
		<-firstRelease
		return po.NewTextToolResult("first", nil, false)
	})
	second := mustReadTool(t, "second", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		started <- "second"
		return po.NewTextToolResult("second", nil, false)
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		mustToolBatchStep(t, "assistant-1",
			mustCall(t, "call-1", "first", "a.go"),
			mustCall(t, "call-2", "second", "b.go"),
		),
		scripted.Must(scripted.Reply("assistant-2", "done", po.Usage{})),
	)

	agent, err := po.NewAgent(po.AgentConfig{
		Model:         model,
		Tools:         mustRegistry(t, first, second),
		Validator:     basic.New(),
		ToolExecution: po.ToolExecutionSequential,
		ResourceResolver: func(po.ToolCall) ([]po.ResourceClaim, error) {
			resolverCalled.Store(true)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	runDone := make(chan error, 1)
	go func() {
		_, err := agent.Run(context.Background(), mustUser(t, "run"))
		runDone <- err
	}()

	if got := <-started; got != "first" {
		t.Fatalf("first started tool = %q", got)
	}
	select {
	case got := <-started:
		t.Fatalf("second tool started before first completed: %q", got)
	case <-time.After(30 * time.Millisecond):
	}

	close(firstRelease)
	if got := <-started; got != "second" {
		t.Fatalf("second started tool = %q", got)
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resolverCalled.Load() {
		t.Fatal("sequential execution unexpectedly resolved scheduler resources")
	}
}

func TestParallelFatalErrorCancelsSiblingTool(t *testing.T) {
	siblingStarted := make(chan struct{})
	siblingCanceled := make(chan struct{})

	failing := mustReadTool(t, "failing", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		<-siblingStarted
		return po.ToolResult{}, po.ErrToolTimeout
	})
	sibling := mustReadTool(t, "sibling", func(ctx context.Context, _ readArgs, _ po.ToolUpdateEmitter) (po.ToolResult, error) {
		close(siblingStarted)
		<-ctx.Done()
		close(siblingCanceled)
		return po.ToolResult{}, context.Cause(ctx)
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		mustToolBatchStep(t, "assistant-1",
			mustCall(t, "call-1", "failing", "a.go"),
			mustCall(t, "call-2", "sibling", "b.go"),
		),
	)

	result, err := mustAgent(t, model, mustRegistry(t, failing, sibling)).Run(context.Background(), mustUser(t, "run"))
	if !errors.Is(err, po.ErrToolTimeout) {
		t.Fatalf("Run() error = %v, want ErrToolTimeout", err)
	}
	if result.StopReason() != po.RunStopToolTimeout {
		t.Fatalf("StopReason() = %q, want %q", result.StopReason(), po.RunStopToolTimeout)
	}
	select {
	case <-siblingCanceled:
	case <-time.After(time.Second):
		t.Fatal("sibling tool did not observe batch cancellation")
	}
}
