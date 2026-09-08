package policy

import (
	"context"
	"errors"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

type fakeApprover struct {
	allowed bool
	err     error
	calls   int
	last    ApprovalRequest
}

func (a *fakeApprover) Approve(ctx context.Context, request ApprovalRequest) (bool, error) {
	a.calls++
	a.last = request
	if a.err != nil {
		return false, a.err
	}
	return a.allowed, nil
}

var _ Approver = (*fakeApprover)(nil)

func TestApprovalAllowsNonRiskyToolWithoutPrompt(t *testing.T) {
	approver := &fakeApprover{allowed: true}
	policy := NewApproval("approval", map[string]string{"delete_file": "destructive operation"}, approver)
	decision, err := policy.BeforeToolCall(context.Background(), po.BeforeToolCallContext{
		Call: po.ToolCall{Name: "read_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Block {
		t.Fatal("read_file should not be blocked")
	}
	if approver.calls != 0 {
		t.Fatalf("approver calls = %d, want 0", approver.calls)
	}
}

func TestApprovalBlocksWhenNoApproverExists(t *testing.T) {
	policy := NewApproval("approval", map[string]string{"delete_file": "destructive operation"}, nil)
	decision, err := policy.BeforeToolCall(context.Background(), po.BeforeToolCallContext{
		Call: po.ToolCall{Name: "delete_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Block {
		t.Fatal("expected operation to be blocked")
	}
}

func TestApprovalReturnsUserDenialAsNormalBlock(t *testing.T) {
	approver := &fakeApprover{allowed: false}
	policy := NewApproval("approval", map[string]string{"git_push": "remote write"}, approver)
	decision, err := policy.BeforeToolCall(context.Background(), po.BeforeToolCallContext{
		Call: po.ToolCall{Name: "git_push"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Block {
		t.Fatal("expected user denial to block")
	}
	if approver.calls != 1 {
		t.Fatalf("approver calls = %d, want 1", approver.calls)
	}
}

func TestApprovalBackendFailureIsErrorNotNormalBlock(t *testing.T) {
	want := errors.New("approval backend unavailable")
	approver := &fakeApprover{err: want}
	policy := NewApproval("approval", map[string]string{"git_push": "remote write"}, approver)
	decision, err := policy.BeforeToolCall(context.Background(), po.BeforeToolCallContext{
		Call: po.ToolCall{Name: "git_push"},
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if decision.Block {
		t.Fatal("backend failure must not be disguised as user denial")
	}
}
