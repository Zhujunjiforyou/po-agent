package contextwindow

import (
	"context"
	"fmt"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

type fixedCounter struct{}

func (fixedCounter) CountSystemPrompt(string) int  { return 0 }
func (fixedCounter) CountToolSpec(po.ToolSpec) int { return 0 }
func (fixedCounter) CountMessage(po.Message) int   { return 10 }

func user(t *testing.T, id string) po.UserMessage {
	t.Helper()
	m, err := po.NewUserTextMessage(id, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func assistant(t *testing.T, id string) po.AssistantMessage {
	t.Helper()
	m, err := po.NewAssistantMessage(id, po.TextPart(id))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBuilderCompactsOldTurnsAndReusesCheckpoint(t *testing.T) {
	calls := 0
	builder, err := New(Config{ReserveTokens: 10, KeepRecentTokens: 20, MaxSummaryTokens: 10}, fixedCounter{}, SummarizerFunc(
		func(ctx context.Context, request SummaryRequest) (Summary, error) {
			calls++
			return Summary{Text: fmt.Sprintf("summary-%d", calls)}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}

	messages := []po.Message{
		user(t, "u1"), assistant(t, "a1"),
		user(t, "u2"), assistant(t, "a2"),
		user(t, "u3"), assistant(t, "a3"),
	}
	input := po.ContextBuildInput{
		Messages:        messages,
		Model:           po.ModelInfo{Limits: po.ModelLimits{ContextWindow: 60, MaxOutputTokens: 10}},
		MaxOutputTokens: 10,
	}

	first, err := builder.Build(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", calls)
	}
	if len(first.Messages) != 3 {
		t.Fatalf("context messages = %d, want summary + recent turn", len(first.Messages))
	}
	if first.Messages[1].MessageID() != "u3" {
		t.Fatalf("first kept id = %q, want u3", first.Messages[1].MessageID())
	}

	second, err := builder.Build(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("checkpoint should avoid resummarizing unchanged prefix; calls=%d", calls)
	}
	if len(second.Messages) != len(first.Messages) {
		t.Fatalf("second context len = %d, want %d", len(second.Messages), len(first.Messages))
	}
}

func TestBuilderNeverStartsTailAtToolResult(t *testing.T) {
	call, err := po.NewToolCall("call-1", "read_file", map[string]any{"path": "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	assistantCall, err := po.NewAssistantMessage("a-call", po.ToolCallPart(call))
	if err != nil {
		t.Fatal(err)
	}
	result, err := po.NewToolResultMessage("r1", call.ID, call.Name, false, po.TextPart("ok"))
	if err != nil {
		t.Fatal(err)
	}

	builder, err := New(Config{ReserveTokens: 10, KeepRecentTokens: 15, MaxSummaryTokens: 10}, fixedCounter{}, SummarizerFunc(
		func(ctx context.Context, request SummaryRequest) (Summary, error) {
			return Summary{Text: "summary"}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}

	messages := []po.Message{user(t, "u1"), assistant(t, "a1"), assistantCall, result}
	view, err := builder.Build(context.Background(), po.ContextBuildInput{
		Messages:        messages,
		Model:           po.ModelInfo{Limits: po.ModelLimits{ContextWindow: 50, MaxOutputTokens: 10}},
		MaxOutputTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) < 2 {
		t.Fatalf("unexpected view length %d", len(view.Messages))
	}
	if view.Messages[1].Kind() == po.MessageToolResult {
		t.Fatal("compacted tail must not start with tool result")
	}
}

func TestApproxCounterDoesNotUndercountCJKAsBytesDividedByFour(t *testing.T) {
	counter := ApproxCounter{}
	message, err := po.NewUserTextMessage("u-cjk", "你好世界")
	if err != nil {
		t.Fatal(err)
	}

	// 四个汉字在 UTF-8 中有 12 bytes。纯 bytes/4 只会估成 3 token；
	// ApproxCounter 对非 ASCII rune 使用 1 token/rune，至少不会出现这个明显低估。
	if got := counter.CountMessage(message); got < 8 { // 4 个框架标记加 4 个中日韩字符
		t.Fatalf("CJK estimate = %d, want at least 8", got)
	}
}
