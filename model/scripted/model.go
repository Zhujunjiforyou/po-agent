package scripted

import (
	"context"
	"fmt"
	"sync"

	po "github.com/lemonzjj/po-agent-go"
)

// Model 按创建时提供的顺序消费 Step。
//
// mutex 保护 steps、requests 和 inFlight，使 `go test -race` 能确认没有数据竞争。
// 但它仍明确拒绝并发 Generate，因为线性脚本在并发调用下没有稳定业务语义。
type Model struct {
	info po.ModelInfo

	mu       sync.Mutex
	steps    []Step
	requests []po.ModelRequest
	inFlight bool
}

// DefaultInfo 返回适合大多数测试的能力配置。
func DefaultInfo() po.ModelInfo {
	return po.ModelInfo{
		Provider:    "scripted",
		ID:          "scripted-v1",
		DisplayName: "Scripted Model",
		Capabilities: po.ModelCapabilities{
			Streaming:         true,
			Tools:             true,
			ParallelToolCalls: true,
		},
		Limits: po.ModelLimits{
			ContextWindow:   128_000,
			MaxOutputTokens: 16_384,
		},
	}
}

// New 创建 Scripted Model，并在测试真正开始前验证全部脚本。
//
// 提前验证比运行到第 7 步才发现脚本写错更容易排查。
func New(info po.ModelInfo, steps ...Step) (*Model, error) {
	if err := info.Validate(); err != nil {
		return nil, err
	}
	for index, step := range steps {
		if err := step.Validate(); err != nil {
			name := step.name
			if name == "" {
				name = fmt.Sprintf("step-%d", index+1)
			}
			return nil, fmt.Errorf("invalid scripted %s: %w", name, err)
		}
	}

	return &Model{
		info:  info,
		steps: append([]Step(nil), steps...),
	}, nil
}

// MustNew 是静态测试配置的便利函数；构造失败时 panic。
func MustNew(info po.ModelInfo, steps ...Step) *Model {
	model, err := New(info, steps...)
	if err != nil {
		panic(err)
	}
	return model
}

// Info 返回 Scripted Model 的稳定身份与能力描述。
// ModelInfo 只有值类型字段，因此按值返回不会暴露内部可变状态。
func (m *Model) Info() po.ModelInfo {
	return m.info
}

// Generate 消费一个 Step，并记录本次请求快照。
func (m *Model) Generate(ctx context.Context, request po.ModelRequest, emit po.DeltaEmitter) (po.ModelResponse, error) {
	// 在修改脚本状态前检查取消，避免一次已取消调用仍消耗 Step。
	if err := ctx.Err(); err != nil {
		return po.ModelResponse{}, err
	}
	if err := request.ValidateFor(m.info); err != nil {
		return po.ModelResponse{}, err
	}

	m.mu.Lock()
	if m.inFlight {
		m.mu.Unlock()
		return po.ModelResponse{}, ErrConcurrentGenerate
	}
	if len(m.steps) == 0 {
		m.mu.Unlock()
		return po.ModelResponse{}, ErrNoRemainingStep
	}

	m.inFlight = true
	step := m.steps[0]
	m.steps = m.steps[1:]

	// 保存 Clone，防止后续测试代码复用变量时改变历史断言数据。
	m.requests = append(m.requests, request.Clone())
	m.mu.Unlock()

	// 无论检查、Emitter 还是 Step 返回错误，都要清除 inFlight。
	defer func() {
		m.mu.Lock()
		m.inFlight = false
		m.mu.Unlock()
	}()

	if step.check != nil {
		if err := step.check(request.Clone()); err != nil {
			return po.ModelResponse{}, fmt.Errorf("scripted request check failed: %w", err)
		}
	}

	if step.err != nil {
		return po.ModelResponse{}, step.err
	}

	for _, delta := range step.deltas {
		if err := po.EmitModelDelta(ctx, emit, delta); err != nil {
			return po.ModelResponse{}, fmt.Errorf("emit scripted delta: %w", err)
		}
	}

	// Step.Validate 已经保证成功 Step 一定有 response。
	return *step.response, nil
}

// Requests 返回所有已收到请求的深层安全快照。
func (m *Model) Requests() []po.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()

	requests := make([]po.ModelRequest, len(m.requests))
	for i, request := range m.requests {
		requests[i] = request.Clone()
	}
	return requests
}

// RemainingSteps 返回尚未消费的脚本步数。
func (m *Model) RemainingSteps() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.steps)
}

// CallCount 返回已经进入脚本消费阶段的 Generate 次数。
func (m *Model) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// 编译期确认 scripted.Model 满足 po.Model。
var _ po.Model = (*Model)(nil)
