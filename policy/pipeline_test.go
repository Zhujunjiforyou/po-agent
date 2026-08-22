package policy

import (
	"context"
	"errors"
	"strings"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

type beforePolicy struct {
	name   string
	order  *[]string
	block  bool
	reason string
	err    error
}

func (p *beforePolicy) Name() string { return p.name }
func (p *beforePolicy) BeforeToolCall(ctx context.Context, input po.BeforeToolCallContext) (po.BeforeToolCallDecision, error) {
	if p.order != nil {
		*p.order = append(*p.order, p.name)
	}
	return po.BeforeToolCallDecision{Block: p.block, Reason: p.reason}, p.err
}

type afterPolicy struct{ name, from, to string }

func (p *afterPolicy) Name() string { return p.name }
func (p *afterPolicy) AfterToolCall(ctx context.Context, input po.AfterToolCallContext) (po.ToolResult, bool, error) {
	parts := input.Result.Content()
	for i := range parts {
		parts[i].Text = strings.ReplaceAll(parts[i].Text, p.from, p.to)
	}
	result, err := po.NewToolResult(parts, input.Result.Details(), input.Result.Terminate())
	return result, input.IsError, err
}

func TestPipelineBeforeOrderAndShortCircuit(t *testing.T) {
	var order []string
	pipeline, err := New(
		&beforePolicy{name: "first", order: &order, block: true, reason: "denied"},
		&beforePolicy{name: "second", order: &order},
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := pipeline.BeforeToolCall(context.Background(), po.BeforeToolCallContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Block || decision.Reason != "denied" {
		t.Fatalf("decision = %+v", decision)
	}
	if len(order) != 1 || order[0] != "first" {
		t.Fatalf("order = %v", order)
	}
}

func TestPipelinePolicyErrorIsNotAllow(t *testing.T) {
	want := errors.New("backend unavailable")
	pipeline, err := New(&beforePolicy{name: "broken", err: want})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pipeline.BeforeToolCall(context.Background(), po.BeforeToolCallContext{})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestPipelineAfterChainsResults(t *testing.T) {
	start, err := po.NewTextToolResult("foo", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := New(
		&afterPolicy{name: "a", from: "foo", to: "bar"},
		&afterPolicy{name: "b", from: "bar", to: "baz"},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := pipeline.AfterToolCall(context.Background(), po.AfterToolCallContext{Result: start})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Content()[0].Text; got != "baz" {
		t.Fatalf("text = %q", got)
	}
}

func TestNewRejectsDuplicateName(t *testing.T) {
	_, err := New(&beforePolicy{name: "same"}, &beforePolicy{name: "same"})
	if err == nil {
		t.Fatal("expected duplicate name error")
	}
}

type nilBeforePolicy struct{}

func (*nilBeforePolicy) Name() string { return "nil-policy" }
func (*nilBeforePolicy) BeforeToolCall(context.Context, po.BeforeToolCallContext) (po.BeforeToolCallDecision, error) {
	return po.BeforeToolCallDecision{}, nil
}

func TestPipelineRejectsTypedNilPolicy(t *testing.T) {
	var typedNil *nilBeforePolicy
	var asPolicy ToolPolicy = typedNil
	if _, err := New(asPolicy); err == nil {
		t.Fatal("expected typed nil policy to be rejected")
	}
}
