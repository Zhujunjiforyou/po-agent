// Package timeout 提供可选的单次模型调用超时装饰器。
package timeout

import (
	"context"
	"fmt"
	"reflect"
	"time"

	po "github.com/lemonzjj/po-agent-go"
)

type Model struct {
	base    po.Model
	timeout time.Duration
}

func New(base po.Model, duration time.Duration) (*Model, error) {
	if isNilModel(base) {
		return nil, fmt.Errorf("timeout model requires a base model")
	}
	if duration < 0 {
		return nil, fmt.Errorf("model timeout cannot be negative")
	}
	return &Model{base: base, timeout: duration}, nil
}

func (m *Model) Info() po.ModelInfo { return m.base.Info() }

func (m *Model) Generate(ctx context.Context, request po.ModelRequest, emit po.DeltaEmitter) (po.ModelResponse, error) {
	if m.timeout <= 0 {
		return m.base.Generate(ctx, request, emit)
	}

	callCtx, cancel := context.WithTimeoutCause(ctx, m.timeout, po.ErrModelTimeout)
	response, err := m.base.Generate(callCtx, request, emit)
	cause := context.Cause(callCtx)
	cancel()

	if cause != nil {
		return po.ModelResponse{}, cause
	}
	return response, err
}

func isNilModel(model po.Model) bool {
	if model == nil {
		return true
	}

	value := reflect.ValueOf(model)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ po.Model = (*Model)(nil)
