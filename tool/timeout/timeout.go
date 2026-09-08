// Package timeout 提供可选的单次工具调用超时装饰器。
package timeout

import (
	"context"
	"fmt"
	"time"

	po "github.com/lemonzjj/po-agent-go"
)

type Tool struct {
	base    po.Tool
	timeout time.Duration
}

func New(base po.Tool, duration time.Duration) (*Tool, error) {
	if base == nil {
		return nil, fmt.Errorf("timeout tool requires a base tool")
	}
	if duration < 0 {
		return nil, fmt.Errorf("tool timeout cannot be negative")
	}
	return &Tool{base: base, timeout: duration}, nil
}

func (t *Tool) Spec() po.ToolSpec { return t.base.Spec() }

func (t *Tool) Execute(ctx context.Context, call po.ToolCall, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
	if t.timeout <= 0 {
		return t.base.Execute(ctx, call, emit)
	}

	callCtx, cancel := context.WithTimeoutCause(ctx, t.timeout, po.ErrToolTimeout)
	result, err := t.base.Execute(callCtx, call, emit)
	cause := context.Cause(callCtx)
	cancel()

	if cause != nil {
		return po.ToolResult{}, cause
	}
	return result, err
}

var _ po.Tool = (*Tool)(nil)
