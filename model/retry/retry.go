// Package retry 提供可选的模型重试装饰器。
// Agent 核心不需要知道模型是否在内部进行重试。
package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	provider "github.com/lemonzjj/po-agent-go/provider"
)

type Policy struct {
	MaxAttempts    int
	BaseDelay      time.Duration
	MaxDelay       time.Duration
	JitterFraction float64
}

func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 3, BaseDelay: 250 * time.Millisecond, MaxDelay: 4 * time.Second, JitterFraction: 0.2}
}

func (p Policy) Validate() error {
	if p.MaxAttempts < 1 {
		return fmt.Errorf("retry max attempts must be at least 1")
	}
	if p.BaseDelay < 0 || p.MaxDelay < 0 {
		return fmt.Errorf("retry delays cannot be negative")
	}
	if p.MaxDelay > 0 && p.BaseDelay > p.MaxDelay {
		return fmt.Errorf("retry base delay cannot exceed max delay")
	}
	if math.IsNaN(p.JitterFraction) || math.IsInf(p.JitterFraction, 0) || p.JitterFraction < 0 || p.JitterFraction > 1 {
		return fmt.Errorf("retry jitter fraction must be between 0 and 1")
	}
	return nil
}

func (p Policy) Delay(retryNumber int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	if retryNumber < 1 {
		retryNumber = 1
	}

	delay := p.BaseDelay
	for i := 1; i < retryNumber; i++ {
		if p.MaxDelay > 0 && delay >= p.MaxDelay/2 {
			delay = p.MaxDelay
			break
		}
		delay *= 2
	}
	if p.MaxDelay > 0 && delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	if delay <= 0 || p.JitterFraction == 0 {
		return delay
	}

	jitter := ((rand.Float64() * 2) - 1) * p.JitterFraction
	return time.Duration(float64(delay) * (1 + jitter))
}

type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

type TimerSleeper struct{}

func (TimerSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return ctx.Err()
	}
}

// Model 为另一个 po.Model 增加传输或模型服务重试。只要已经发出任何增量，它就会拒绝
// 重试：部分输出对外可见后，除非界面具有明确的重置协议，否则静默开始第二次生成会破坏
// 输出流。
type Model struct {
	base    po.Model
	policy  Policy
	sleeper Sleeper
}

func New(base po.Model, policy Policy, sleeper Sleeper) (*Model, error) {
	if isNilModel(base) {
		return nil, fmt.Errorf("retry model requires a base model")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if isNilValue(sleeper) {
		sleeper = TimerSleeper{}
	}
	return &Model{base: base, policy: policy, sleeper: sleeper}, nil
}

func MustNew(base po.Model, policy Policy, sleeper Sleeper) *Model {
	model, err := New(base, policy, sleeper)
	if err != nil {
		panic(err)
	}
	return model
}

func (m *Model) Info() po.ModelInfo { return m.base.Info() }

func (m *Model) Generate(ctx context.Context, request po.ModelRequest, emit po.DeltaEmitter) (po.ModelResponse, error) {
	var lastErr error
	for attempt := 1; attempt <= m.policy.MaxAttempts; attempt++ {
		streamed := false
		attemptEmit := po.DeltaEmitter(func(ctx context.Context, delta po.ModelDelta) error {
			streamed = true
			if emit == nil {
				return nil
			}
			return emit(ctx, delta)
		})

		response, err := m.base.Generate(ctx, request, attemptEmit)
		if err == nil {
			return response, nil
		}
		lastErr = err

		if cause := context.Cause(ctx); cause != nil {
			return po.ModelResponse{}, cause
		}
		if streamed {
			return po.ModelResponse{}, err
		}

		retryable, retryAfter := metadata(err)
		if !retryable || attempt == m.policy.MaxAttempts {
			return po.ModelResponse{}, err
		}
		if err := m.sleeper.Sleep(ctx, m.policy.Delay(attempt, retryAfter)); err != nil {
			return po.ModelResponse{}, err
		}
	}
	return po.ModelResponse{}, lastErr
}

func metadata(err error) (bool, time.Duration) {
	var providerErr *provider.Error
	if errors.As(err, &providerErr) && providerErr != nil {
		return providerErr.Retryable, providerErr.RetryAfter
	}
	return false, 0
}

func isNilModel(model po.Model) bool { return isNilValue(model) }

func isNilValue(value any) bool {
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

var _ po.Model = (*Model)(nil)
