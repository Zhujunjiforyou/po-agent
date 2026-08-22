package po_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/tool/builtin"
)

func TestTypedToolDecodesArgumentsAndReturnsSeparatedDetails(t *testing.T) {
	t.Parallel()

	tool := builtin.NewCalculator()
	call, err := po.NewToolCall(
		"call-1",
		"calculator",
		map[string]any{
			"operation": "multiply",
			"a":         6,
			"b":         7,
		},
	)
	if err != nil {
		t.Fatalf("NewToolCall() error = %v", err)
	}

	result, err := tool.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	content := result.Content()
	if len(content) != 1 || content[0].Text != "计算结果：42" {
		t.Fatalf("content = %#v, want one text part containing 42", content)
	}

	var details struct {
		Result float64 `json:"result"`
	}
	if err := json.Unmarshal(result.Details(), &details); err != nil {
		t.Fatalf("decode details: %v", err)
	}
	if details.Result != 42 {
		t.Fatalf("details result = %v, want 42", details.Result)
	}
}

func TestTypedToolRejectsNameMismatchBeforeHandler(t *testing.T) {
	t.Parallel()

	tool := builtin.NewEcho()
	call, err := po.NewToolCall("call-1", "another_tool", map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("NewToolCall() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), call, nil)
	if !errors.Is(err, po.ErrToolNameMismatch) {
		t.Fatalf("Execute() error = %v, want ErrToolNameMismatch", err)
	}
}

func TestTypedToolBusinessValidationRunsAfterJSONDecode(t *testing.T) {
	t.Parallel()

	tool := builtin.NewCalculator()
	call, err := po.NewToolCall(
		"call-1",
		"calculator",
		map[string]any{"operation": "divide", "a": 10, "b": 0},
	)
	if err != nil {
		t.Fatalf("NewToolCall() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), call, nil)
	if !errors.Is(err, po.ErrInvalidToolArguments) {
		t.Fatalf("Execute() error = %v, want ErrInvalidToolArguments", err)
	}
}

func TestToolResultOwnsContentAndDetails(t *testing.T) {
	t.Parallel()

	details := json.RawMessage(`{"path":"README.md"}`)
	result, err := po.NewTextToolResult("content", details, false)
	if err != nil {
		t.Fatalf("NewTextToolResult() error = %v", err)
	}

	// 修改构造函数的输入，结果内部不应变化。
	details[2] = 'X'
	if got := string(result.Details()); got != `{"path":"README.md"}` {
		t.Fatalf("details = %s, want original JSON", got)
	}

	// 修改 getter 返回值，结果内部也不应变化。
	returned := result.Details()
	returned[2] = 'Y'
	if got := string(result.Details()); got != `{"path":"README.md"}` {
		t.Fatalf("details after getter mutation = %s", got)
	}
}

func TestEmitToolUpdateValidatesProgressAndPropagatesEmitterError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("consumer stopped")
	err := po.EmitToolUpdate(
		context.Background(),
		func(context.Context, po.ToolUpdate) error { return wantErr },
		po.NewProgressToolUpdate("half", 0.5),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("EmitToolUpdate() error = %v, want %v", err, wantErr)
	}

	err = po.EmitToolUpdate(
		context.Background(),
		nil,
		po.NewProgressToolUpdate("invalid", 1.1),
	)
	if !errors.Is(err, po.ErrInvalidToolUpdate) {
		t.Fatalf("invalid progress error = %v, want ErrInvalidToolUpdate", err)
	}
}

func TestToolExecutionPreservesHandlerErrorInChain(t *testing.T) {
	t.Parallel()

	rootErr := errors.New("disk unavailable")
	spec, err := po.NewToolSpec(
		"failure",
		"always fails",
		json.RawMessage(`{"type":"object"}`),
	)
	if err != nil {
		t.Fatalf("NewToolSpec() error = %v", err)
	}

	tool, err := po.NewTypedTool(
		spec,
		nil,
		func(context.Context, struct{}, po.ToolUpdateEmitter) (po.ToolResult, error) {
			return po.ToolResult{}, rootErr
		},
	)
	if err != nil {
		t.Fatalf("NewTypedTool() error = %v", err)
	}
	call, err := po.NewToolCall("call-1", "failure", struct{}{})
	if err != nil {
		t.Fatalf("NewToolCall() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), call, nil)
	if !errors.Is(err, po.ErrToolExecution) || !errors.Is(err, rootErr) {
		t.Fatalf("Execute() error chain = %v, want both sentinels", err)
	}
}
