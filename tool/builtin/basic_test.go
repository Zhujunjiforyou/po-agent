package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

func TestEcho(t *testing.T) {
	result := executeTool(t, NewEcho(), EchoArgs{Message: "你好"})
	if got := result.Content()[0].Text; got != "你好" {
		t.Fatalf("echo content = %q", got)
	}
	var details map[string]int
	if err := json.Unmarshal(result.Details(), &details); err != nil {
		t.Fatal(err)
	}
	if details["character_count"] != 2 {
		t.Fatalf("details = %#v", details)
	}
}

func TestCalculator(t *testing.T) {
	result := executeTool(t, NewCalculator(), CalculatorArgs{Operation: "multiply", A: 17, B: 19})
	if got := result.Content()[0].Text; got != "计算结果：323" {
		t.Fatalf("calculator content = %q", got)
	}
}

func TestCalculatorRejectsDivisionByZero(t *testing.T) {
	tool := NewCalculator()
	call, err := po.NewToolCall("call-1", tool.Spec().Name(), CalculatorArgs{Operation: "divide", A: 1, B: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), call, nil); err == nil {
		t.Fatal("calculator accepted division by zero")
	}
}

func TestCurrentDirectoryWith(t *testing.T) {
	want := "/workspace"
	result := executeTool(t, NewCurrentDirectoryWith(func() (string, error) { return want, nil }), CurrentDirectoryArgs{})
	if got := result.Content()[0].Text; got != want {
		t.Fatalf("current directory = %q, want %q", got, want)
	}

	wantErr := errors.New("getwd failed")
	tool := NewCurrentDirectoryWith(func() (string, error) { return "", wantErr })
	call, err := po.NewToolCall("call-1", tool.Spec().Name(), CurrentDirectoryArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), call, nil); !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want %v", err, wantErr)
	}
}

func executeTool[T any](t *testing.T, tool po.Tool, arguments T) po.ToolResult {
	t.Helper()
	call, err := po.NewToolCall("call-1", tool.Spec().Name(), arguments)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
