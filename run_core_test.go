package po_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
)

func TestRunAggregatesUsageAndRecordsTurns(t *testing.T) {
	readTool := mustReadTool(t, "read_file", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		return po.NewTextToolResult("contents", nil, false)
	})
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool(
			"assistant-1", "call-1", "read_file",
			map[string]any{"path": "README.md"},
			po.Usage{InputTokens: 10, OutputTokens: 2, CostUSD: 0.01},
		)),
		scripted.Must(scripted.Reply(
			"assistant-2", "done",
			po.Usage{InputTokens: 20, OutputTokens: 3, CostUSD: 0.02},
		)),
	)

	result, err := mustAgent(t, model, mustRegistry(t, readTool)).Run(context.Background(), mustUser(t, "run"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.StopReason() != po.RunStopCompleted {
		t.Fatalf("StopReason() = %q", result.StopReason())
	}
	if result.TurnAttempts() != 2 || len(result.Turns()) != 2 {
		t.Fatalf("turns = attempts:%d records:%d, want 2/2", result.TurnAttempts(), len(result.Turns()))
	}
	if result.ToolCalls() != 1 {
		t.Fatalf("ToolCalls() = %d, want 1", result.ToolCalls())
	}
	usage := result.Usage()
	if usage.InputTokens != 30 || usage.OutputTokens != 5 || usage.CostUSD != 0.03 {
		t.Fatalf("Usage() = %+v", usage)
	}
	if !strings.HasPrefix(result.RunID(), "run-") {
		t.Fatalf("RunID() = %q", result.RunID())
	}
}

func TestModelFailureReturnsPartialRunState(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Fail(wantErr))

	result, err := mustAgent(t, model, po.NewToolRegistry()).Run(context.Background(), mustUser(t, "run"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want wrapped provider error", err)
	}
	if result.StopReason() != po.RunStopFailed {
		t.Fatalf("StopReason() = %q", result.StopReason())
	}
	if result.TurnAttempts() != 1 {
		t.Fatalf("TurnAttempts() = %d, want 1", result.TurnAttempts())
	}
	if result.RunID() == "" || result.FinishedAt().IsZero() {
		t.Fatalf("partial result lost run metadata: %+v", result)
	}
	if len(result.Messages()) != 1 {
		t.Fatalf("Messages() = %d, want only the user message", len(result.Messages()))
	}
}

func TestSameAgentGetsDifferentRunIDs(t *testing.T) {
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.Reply("a1", "one", po.Usage{})),
		scripted.Must(scripted.Reply("a2", "two", po.Usage{})),
	)
	agent := mustAgent(t, model, po.NewToolRegistry())

	first, err := agent.Run(context.Background(), mustUser(t, "first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := agent.Run(context.Background(), mustUser(t, "second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID() == second.RunID() {
		t.Fatalf("run IDs are equal: %q", first.RunID())
	}
}
