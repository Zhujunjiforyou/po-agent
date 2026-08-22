package timeout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	tooltimeout "github.com/lemonzjj/po-agent-go/tool/timeout"
)

type blockingTool struct{ spec po.ToolSpec }

func (t blockingTool) Spec() po.ToolSpec { return t.spec }
func (t blockingTool) Execute(ctx context.Context, call po.ToolCall, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
	<-ctx.Done()
	return po.ToolResult{}, ctx.Err()
}

func TestToolTimeoutReturnsStableCause(t *testing.T) {
	spec, err := po.NewToolSpec("block", "block", []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	tool, err := tooltimeout.New(blockingTool{spec: spec}, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	call, _ := po.NewToolCall("c1", "block", map[string]any{})
	_, err = tool.Execute(context.Background(), call, nil)
	if !errors.Is(err, po.ErrToolTimeout) {
		t.Fatalf("error = %v", err)
	}
}

type successArgs struct {
	Path string `json:"path"`
}

func TestSuccessfulToolCallIsNotTurnedIntoCancellationByCleanup(t *testing.T) {
	spec, err := po.NewToolSpec(
		"read_file",
		"read file",
		[]byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	base, err := po.NewTypedTool(spec, nil, func(context.Context, successArgs, po.ToolUpdateEmitter) (po.ToolResult, error) {
		return po.NewTextToolResult("ok", nil, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := tooltimeout.New(base, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	call, err := po.NewToolCall("c1", "read_file", map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := wrapped.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content()[0].Text != "ok" {
		t.Fatalf("result = %#v", result.Content())
	}
}
