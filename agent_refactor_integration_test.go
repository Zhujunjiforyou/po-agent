package po_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	modeltimeout "github.com/lemonzjj/po-agent-go/model/timeout"
	"github.com/lemonzjj/po-agent-go/schema/basic"
	tooltimeout "github.com/lemonzjj/po-agent-go/tool/timeout"
)

func collectEventTypes(agent *po.Agent) (*[]po.AgentEventType, func()) {
	events := make([]po.AgentEventType, 0, 16)
	unsubscribe := agent.Subscribe(func(_ context.Context, event po.AgentEvent) {
		events = append(events, event.Type())
	})
	return &events, unsubscribe
}

func TestRunEmitsBalancedLifecycleForFinalAnswer(t *testing.T) {
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.Reply("a1", "done", po.Usage{})))
	agent := mustAgent(t, model, po.NewToolRegistry())
	events, unsubscribe := collectEventTypes(agent)
	defer unsubscribe()

	result, err := agent.Run(context.Background(), mustUser(t, "finish"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.StopReason() != po.RunStopCompleted {
		t.Fatalf("StopReason() = %q, want %q", result.StopReason(), po.RunStopCompleted)
	}

	want := []po.AgentEventType{
		po.EventRunStart,
		po.EventTurnStart,
		po.EventMessageStart,
		po.EventMessageEnd,
		po.EventTurnEnd,
		po.EventRunEnd,
	}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("events = %v, want %v", *events, want)
	}
}

type invalidResultTool struct {
	spec po.ToolSpec
}

func (t *invalidResultTool) Spec() po.ToolSpec { return t.spec.Clone() }

func (t *invalidResultTool) Execute(context.Context, po.ToolCall, po.ToolUpdateEmitter) (po.ToolResult, error) {
	return po.ToolResult{}, nil
}

func TestStartedToolAlwaysEmitsToolEndOnRuntimeFailure(t *testing.T) {
	tool := &invalidResultTool{spec: mustSpec(t, "read_file")}
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.CallTool(
		"a1", "c1", "read_file", map[string]any{"path": "README.md"}, po.Usage{},
	)))
	agent := mustAgent(t, model, mustRegistry(t, tool))

	var toolEnd po.ToolEndEvent
	var sawToolEnd bool
	unsubscribe := agent.Subscribe(func(_ context.Context, event po.AgentEvent) {
		if typed, ok := event.(po.ToolEndEvent); ok {
			toolEnd = typed
			sawToolEnd = true
		}
	})
	defer unsubscribe()

	result, err := agent.Run(context.Background(), mustUser(t, "read"))
	if err == nil {
		t.Fatal("expected invalid tool result to fail the run")
	}
	if result.StopReason() != po.RunStopFailed {
		t.Fatalf("StopReason() = %q, want %q", result.StopReason(), po.RunStopFailed)
	}
	if !sawToolEnd {
		t.Fatal("ToolStartEvent was not closed by ToolEndEvent")
	}
	if toolEnd.Err == nil {
		t.Fatal("ToolEndEvent.Err must describe the runtime failure")
	}
}

type cancellationTool struct {
	spec  po.ToolSpec
	cause error
}

func (t *cancellationTool) Spec() po.ToolSpec { return t.spec.Clone() }

func (t *cancellationTool) Execute(ctx context.Context, _ po.ToolCall, _ po.ToolUpdateEmitter) (po.ToolResult, error) {
	t.cause = context.Cause(ctx)
	if t.cause == nil {
		return po.ToolResult{}, errors.New("tool did not receive canceled run context")
	}
	return po.ToolResult{}, t.cause
}

func TestRunCancellationPropagatesToToolAndStillEndsLifecycle(t *testing.T) {
	tool := &cancellationTool{spec: mustSpec(t, "read_file")}
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.CallTool(
		"a1", "c1", "read_file", map[string]any{"path": "README.md"}, po.Usage{},
	)))
	agent := mustAgent(t, model, mustRegistry(t, tool))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sawTurnEnd, sawRunEnd bool
	unsubscribe := agent.Subscribe(func(_ context.Context, event po.AgentEvent) {
		switch event.(type) {
		case po.ToolStartEvent:
			cancel()
		case po.TurnEndEvent:
			sawTurnEnd = true
		case po.RunEndEvent:
			sawRunEnd = true
		}
	})
	defer unsubscribe()

	result, err := agent.Run(ctx, mustUser(t, "read"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if result.StopReason() != po.RunStopCancelled {
		t.Fatalf("StopReason() = %q, want %q", result.StopReason(), po.RunStopCancelled)
	}
	if !errors.Is(tool.cause, context.Canceled) {
		t.Fatalf("tool cause = %v, want context.Canceled", tool.cause)
	}
	if !sawTurnEnd || !sawRunEnd {
		t.Fatalf("terminal events: turn_end=%v run_end=%v", sawTurnEnd, sawRunEnd)
	}
}

func TestShouldStopAfterTurnCanStopCoreWithoutBudgetKnowledge(t *testing.T) {
	readTool := mustReadTool(t, "read_file", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		return po.NewTextToolResult("ok", nil, false)
	})
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool("a1", "c1", "read_file", map[string]any{"path": "README.md"}, po.Usage{})),
		scripted.Must(scripted.Reply("a2", "must not run", po.Usage{})),
	)

	const stop po.RunStopReason = "test_extension_stop"
	agent, err := po.NewAgent(po.AgentConfig{
		Model:     model,
		Tools:     mustRegistry(t, readTool),
		Validator: basic.New(),
		ShouldStopAfterTurn: func(context.Context, po.TurnCompletedContext) (po.RunStopReason, bool, error) {
			return stop, true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := agent.Run(context.Background(), mustUser(t, "read"))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason() != stop {
		t.Fatalf("StopReason() = %q, want %q", result.StopReason(), stop)
	}
	if model.CallCount() != 1 {
		t.Fatalf("model calls = %d, want 1", model.CallCount())
	}
}

func TestModelTimeoutDecoratorMapsToCoreStopReason(t *testing.T) {
	base := blockingModel{info: scripted.DefaultInfo()}
	model, err := modeltimeout.New(base, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	agent := mustAgent(t, model, po.NewToolRegistry())

	result, err := agent.Run(context.Background(), mustUser(t, "wait"))
	if !errors.Is(err, po.ErrModelTimeout) {
		t.Fatalf("Run() error = %v, want ErrModelTimeout", err)
	}
	if result.StopReason() != po.RunStopModelTimeout {
		t.Fatalf("StopReason() = %q, want %q", result.StopReason(), po.RunStopModelTimeout)
	}
}

type blockingModel struct {
	info po.ModelInfo
}

func (m blockingModel) Info() po.ModelInfo { return m.info }

func (blockingModel) Generate(ctx context.Context, _ po.ModelRequest, _ po.DeltaEmitter) (po.ModelResponse, error) {
	<-ctx.Done()
	return po.ModelResponse{}, context.Cause(ctx)
}

func TestToolTimeoutDecoratorMapsToCoreStopReason(t *testing.T) {
	base := mustReadTool(t, "read_file", func(ctx context.Context, _ readArgs, _ po.ToolUpdateEmitter) (po.ToolResult, error) {
		<-ctx.Done()
		return po.ToolResult{}, context.Cause(ctx)
	})
	wrapped, err := tooltimeout.New(base, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.CallTool(
		"a1", "c1", "read_file", map[string]any{"path": "README.md"}, po.Usage{},
	)))
	agent := mustAgent(t, model, mustRegistry(t, wrapped))

	result, err := agent.Run(context.Background(), mustUser(t, "read"))
	if !errors.Is(err, po.ErrToolTimeout) {
		t.Fatalf("Run() error = %v, want ErrToolTimeout", err)
	}
	if result.StopReason() != po.RunStopToolTimeout {
		t.Fatalf("StopReason() = %q, want %q", result.StopReason(), po.RunStopToolTimeout)
	}
}
