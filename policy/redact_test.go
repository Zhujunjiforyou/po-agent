package policy

import (
	"context"
	"strings"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

func TestRedactTextRemovesKnownSecret(t *testing.T) {
	result, err := po.NewTextToolResult("token=secret-123", []byte(`{"debug":"secret-123"}`), false)
	if err != nil {
		t.Fatal(err)
	}

	policy := NewRedactText("redact", []string{"secret-123"})
	got, isError, err := policy.AfterToolCall(context.Background(), po.AfterToolCallContext{
		Result:  result,
		IsError: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Redaction 不应该偷偷改变 Tool Error 语义。
	if !isError {
		t.Fatal("expected IsError to remain true")
	}

	parts := got.Content()
	if len(parts) != 1 {
		t.Fatalf("parts = %d", len(parts))
	}
	if strings.Contains(parts[0].Text, "secret-123") {
		t.Fatalf("secret remained in content: %q", parts[0].Text)
	}
	if strings.Contains(string(got.Details()), "secret-123") {
		t.Fatalf("secret remained in details: %s", got.Details())
	}
}
