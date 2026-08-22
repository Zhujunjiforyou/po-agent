// Package policy 提供通过 Po 核心钩子组合的可选工具策略。
package policy

import (
	"context"
	"fmt"
	"reflect"

	po "github.com/lemonzjj/po-agent-go"
)

// ToolPolicy 是 Pipeline 所需的最小公共能力。
type ToolPolicy interface {
	Name() string
}

type BeforeToolPolicy interface {
	ToolPolicy
	BeforeToolCall(context.Context, po.BeforeToolCallContext) (po.BeforeToolCallDecision, error)
}

type AfterToolPolicy interface {
	ToolPolicy
	AfterToolCall(context.Context, po.AfterToolCallContext) (po.ToolResult, bool, error)
}

// Pipeline 在核心的工具执行前后钩子上组合可选工具策略。
type Pipeline struct {
	policies []ToolPolicy
}

func New(policies ...ToolPolicy) (*Pipeline, error) {
	copied := make([]ToolPolicy, 0, len(policies))
	seen := make(map[string]struct{}, len(policies))
	for i, current := range policies {
		if isNilPolicyValue(current) {
			return nil, fmt.Errorf("policy %d is nil", i)
		}
		name := current.Name()
		if name == "" {
			return nil, fmt.Errorf("policy %d has empty name", i)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate policy %q", name)
		}
		seen[name] = struct{}{}
		copied = append(copied, current)
	}
	return &Pipeline{policies: copied}, nil
}

func (p *Pipeline) BeforeToolCall(ctx context.Context, input po.BeforeToolCallContext) (po.BeforeToolCallDecision, error) {
	if p == nil {
		return po.BeforeToolCallDecision{}, nil
	}
	for _, current := range p.policies {
		before, ok := current.(BeforeToolPolicy)
		if !ok {
			continue
		}
		decision, err := before.BeforeToolCall(ctx, input)
		if err != nil {
			return po.BeforeToolCallDecision{}, fmt.Errorf("policy %s before tool call: %w", current.Name(), err)
		}
		if decision.Block {
			if decision.Reason == "" {
				decision.Reason = fmt.Sprintf("blocked by policy %s", current.Name())
			}
			return decision, nil
		}
	}
	return po.BeforeToolCallDecision{}, nil
}

func (p *Pipeline) AfterToolCall(ctx context.Context, input po.AfterToolCallContext) (po.ToolResult, bool, error) {
	if p == nil {
		return input.Result.Clone(), input.IsError, nil
	}

	result := input.Result.Clone()
	isError := input.IsError
	for _, current := range p.policies {
		after, ok := current.(AfterToolPolicy)
		if !ok {
			continue
		}
		next, nextIsError, err := after.AfterToolCall(ctx, po.AfterToolCallContext{
			Run:        input.Run.Clone(),
			BatchIndex: input.BatchIndex,
			BatchSize:  input.BatchSize,
			Call:       input.Call.Clone(),
			Spec:       input.Spec.Clone(),
			Result:     result.Clone(),
			IsError:    isError,
		})
		if err != nil {
			return po.ToolResult{}, false, fmt.Errorf("policy %s after tool call: %w", current.Name(), err)
		}
		if err := next.Validate(); err != nil {
			return po.ToolResult{}, false, fmt.Errorf("policy %s returned invalid tool result: %w", current.Name(), err)
		}
		result, isError = next.Clone(), nextIsError
	}
	return result, isError, nil
}

// isNilPolicyValue 在扩展边界拒绝包含有类型 nil 的接口值。
func isNilPolicyValue(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
