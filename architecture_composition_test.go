package po_test

import (
	"context"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/guard"
	"github.com/lemonzjj/po-agent-go/model/retry"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	modeltimeout "github.com/lemonzjj/po-agent-go/model/timeout"
	"github.com/lemonzjj/po-agent-go/policy"
	"github.com/lemonzjj/po-agent-go/schema/basic"
	tooltimeout "github.com/lemonzjj/po-agent-go/tool/timeout"
)

func TestOptionalExtensionsComposeAroundCore(t *testing.T) {
	baseModel := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool("a1", "c1", "read_file", map[string]any{"path": "README.md"}, po.Usage{})),
		scripted.Must(scripted.Reply("a2", "done", po.Usage{})),
	)

	timedModel, err := modeltimeout.New(baseModel, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	retryingModel, err := retry.New(timedModel, retry.Policy{MaxAttempts: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}

	baseTool := mustReadTool(t, "read_file", func(context.Context, readArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		return po.NewTextToolResult("ok", nil, false)
	})
	timedTool, err := tooltimeout.New(baseTool, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	controller := guard.MustNew(guard.Budget{MaxTurns: 8, MaxToolCalls: 8})
	pipeline := policy.New(controller)

	agent, err := po.NewAgent(po.AgentConfig{
		Model:               retryingModel,
		Tools:               mustRegistry(t, timedTool),
		Validator:           basic.New(),
		BeforeToolCall:      pipeline.BeforeToolCall,
		AfterToolCall:       pipeline.AfterToolCall,
		ShouldStopAfterTurn: controller.ShouldStopAfterTurn,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := controller.Context(context.Background())
	defer cancel()
	result, err := agent.Run(ctx, mustUser(t, "read"))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason() != po.RunStopCompleted {
		t.Fatalf("StopReason() = %q, want %q", result.StopReason(), po.RunStopCompleted)
	}
}
