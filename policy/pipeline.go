// Package policy 提供通过 Po 核心钩子组合的可选工具策略。
package policy

import (
	"context"
	"fmt"

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

func New(policies ...ToolPolicy) *Pipeline {
	return &Pipeline{policies: append([]ToolPolicy(nil), policies...)}
}

func (p *Pipeline) BeforeToolCall(ctx context.Context, input po.BeforeToolCallContext) (po.BeforeToolCallDecision, error) {
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
	result := input.Result
	isError := input.IsError
	for _, current := range p.policies {
		after, ok := current.(AfterToolPolicy)
		if !ok {
			continue
		}
		next, nextIsError, err := after.AfterToolCall(ctx, po.AfterToolCallContext{
			Run:        input.Run,
			BatchIndex: input.BatchIndex,
			BatchSize:  input.BatchSize,
			Call:       input.Call,
			Spec:       input.Spec,
			Result:     result,
			IsError:    isError,
		})
		if err != nil {
			return po.ToolResult{}, false, fmt.Errorf("policy %s after tool call: %w", current.Name(), err)
		}
		result, isError = next, nextIsError
	}
	return result, isError, nil
}
