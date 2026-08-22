package guard_test

import (
	"context"
	"errors"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/guard"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	"github.com/lemonzjj/po-agent-go/schema/basic"
)

type args struct {
	Path string `json:"path"`
}

func spec(t *testing.T) po.ToolSpec {
	t.Helper()
	schema := []byte(`{
		"type":"object",
		"properties":{"path":{"type":"string"}},
		"required":["path"],
		"additionalProperties":false
	}`)
	s, err := po.NewToolSpec("read_file", "read file", schema)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func tool(t *testing.T, executed *int, text string) po.Tool {
	t.Helper()
	value, err := po.NewTypedTool(spec(t), nil, func(ctx context.Context, a args, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		*executed++
		return po.NewTextToolResult(text, nil, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func registry(t *testing.T, tools ...po.Tool) *po.ToolRegistry {
	t.Helper()
	r := po.NewToolRegistry()
	for _, current := range tools {
		if err := r.Register(current); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func user(t *testing.T) po.UserMessage {
	t.Helper()
	m, err := po.NewUserTextMessage("u1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func agent(t *testing.T, model po.Model, tools *po.ToolRegistry, budget guard.Budget) *po.Agent {
	t.Helper()
	controller := guard.MustNew(budget)
	a, err := po.NewAgent(po.AgentConfig{
		Model: model, Tools: tools, Validator: basic.New(),
		BeforeToolCall:      controller.BeforeToolCall,
		ShouldStopAfterTurn: controller.ShouldStopAfterTurn,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMaxTurnsStopsBeforeExtraModelCall(t *testing.T) {
	executed := 0
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool("a1", "c1", "read_file", map[string]any{"path": "x"}, po.Usage{})),
		scripted.Must(scripted.CallTool("a2", "c2", "read_file", map[string]any{"path": "x"}, po.Usage{})),
		scripted.Must(scripted.Reply("a3", "must not run", po.Usage{})),
	)
	result, err := agent(t, model, registry(t, tool(t, &executed, "ok")), guard.Budget{MaxTurns: 2}).Run(context.Background(), user(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason() != guard.StopMaxTurns {
		t.Fatalf("reason = %q", result.StopReason())
	}
	if model.CallCount() != 2 {
		t.Fatalf("calls = %d", model.CallCount())
	}
}

func TestMaxToolCallsRejectsWholeBatchBeforeSideEffects(t *testing.T) {
	executed := 0
	call1, _ := po.NewToolCall("c1", "read_file", map[string]any{"path": "a"})
	call2, _ := po.NewToolCall("c2", "read_file", map[string]any{"path": "b"})
	message, _ := po.NewAssistantMessage("a1", po.ToolCallPart(call1), po.ToolCallPart(call2))
	response, _ := po.NewModelResponse(message, po.ModelStopToolCall, po.Usage{}, "r1")
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Respond(response))

	result, err := agent(t, model, registry(t, tool(t, &executed, "ok")), guard.Budget{MaxToolCalls: 1}).Run(context.Background(), user(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason() != guard.StopMaxToolCalls {
		t.Fatalf("reason = %q", result.StopReason())
	}
	if executed != 0 {
		t.Fatalf("executed = %d", executed)
	}
	if result.ToolCalls() != 0 {
		t.Fatalf("tool calls = %d", result.ToolCalls())
	}
	if len(result.Messages()) != 4 {
		t.Fatalf("messages = %d", len(result.Messages()))
	}
}

func TestInputTokenBudgetStopsBeforeToolExecution(t *testing.T) {
	executed := 0
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.CallTool(
		"a1", "c1", "read_file", map[string]any{"path": "x"}, po.Usage{InputTokens: 11},
	)))
	result, err := agent(t, model, registry(t, tool(t, &executed, "ok")), guard.Budget{MaxInputTokens: 10}).Run(context.Background(), user(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason() != guard.StopInputTokens {
		t.Fatalf("reason = %q", result.StopReason())
	}
	if executed != 0 {
		t.Fatalf("executed = %d", executed)
	}
}

func TestExactlyAtTokenBudgetAllowsFinalAnswer(t *testing.T) {
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.Reply("a1", "done", po.Usage{InputTokens: 10, OutputTokens: 5})))
	budget := guard.Budget{MaxInputTokens: 10, MaxOutputTokens: 5}
	result, err := agent(t, model, po.NewToolRegistry(), budget).Run(context.Background(), user(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason() != po.RunStopCompleted {
		t.Fatalf("reason = %q", result.StopReason())
	}
}

func TestNoProgressIgnoresChangingToolCallIDs(t *testing.T) {
	executed := 0
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool("a1", "c1", "read_file", map[string]any{"path": "missing"}, po.Usage{})),
		scripted.Must(scripted.CallTool("a2", "c2", "read_file", map[string]any{"path": "missing"}, po.Usage{})),
		scripted.Must(scripted.CallTool("a3", "c3", "read_file", map[string]any{"path": "missing"}, po.Usage{})),
	)
	registry := registry(t, tool(t, &executed, "file does not exist"))
	budget := guard.Budget{MaxTurns: 10, NoProgressLimit: 3}
	result, err := agent(t, model, registry, budget).Run(context.Background(), user(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason() != guard.StopNoProgress {
		t.Fatalf("reason = %q", result.StopReason())
	}
	if model.CallCount() != 3 {
		t.Fatalf("calls = %d", model.CallCount())
	}
}

func TestContextAppliesMaxDuration(t *testing.T) {
	controller := guard.MustNew(guard.Budget{MaxDuration: 5 * time.Millisecond})
	ctx, cancel := controller.Context(context.Background())
	defer cancel()
	<-ctx.Done()
	if !errors.Is(context.Cause(ctx), po.ErrRunTimeout) {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
}
