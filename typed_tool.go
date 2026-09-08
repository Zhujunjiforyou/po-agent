package po

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// ToolHandler 是强类型工具的业务函数。
// T 是该工具自己的参数类型
type ToolHandler[T any] func(ctx context.Context, args T, emit ToolUpdateEmitter) (ToolResult, error)

// ToolArgumentsCheck 是可选的业务规则校验函数
// JSON Schema 负责通用结构约束，例如字段类型，required和数组范围
type ToolArgumentsCheck[T any] func(args T) error

// TypedTool 把 Provider-neutral 的原始 JSON ToolCall 转换成强类型 Go 参数。
// 字段不导出，使工具在构造完成后保持逻辑不可变。运行期间多个 Run 可以安全共享
// 同一个 TypedTool，前提是 handler 自身不修改未受保护的外部共享状态。
type TypedTool[T any] struct {
	spec    ToolSpec
	check   ToolArgumentsCheck[T]
	handler ToolHandler[T]
}

// NewTypedTool 创建一个类型安全的工具
func NewTypedTool[T any](spec ToolSpec, check ToolArgumentsCheck[T], handler ToolHandler[T]) (*TypedTool[T], error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, fmt.Errorf("%w: handler is required", ErrInvalidTool)
	}
	return &TypedTool[T]{
		spec:    spec,
		check:   check,
		handler: handler,
	}, nil
}

// MustNewTypedTool 适合静态内置工具和测试夹具。
// 动态配置或用户输入路径应使用 NewTypedTool 并正常处理 error。
func MustNewTypedTool[T any](spec ToolSpec, check ToolArgumentsCheck[T], handler ToolHandler[T]) *TypedTool[T] {
	tool, err := NewTypedTool(spec, check, handler)
	if err != nil {
		panic(err)
	}
	return tool
}

func (t *TypedTool[T]) Spec() ToolSpec {
	return t.spec
}

// Execute 实现Tool接口，并集中完成名称校验，JSON解码，业务校验和结果校验
func (t *TypedTool[T]) Execute(ctx context.Context, call ToolCall, emit ToolUpdateEmitter) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	if err := call.Validate(); err != nil {
		return ToolResult{}, err
	}
	if call.Name != t.spec.Name() {
		return ToolResult{}, fmt.Errorf(
			"%w: call targets %q, tool implements %q",
			ErrToolNameMismatch,
			call.Name,
			t.spec.Name(),
		)
	}
	args, err := decodeToolArguments[T](call.Arguments)
	if err != nil {
		return ToolResult{}, err
	}
	if t.check != nil {
		if err := t.check(args); err != nil {
			return ToolResult{}, fmt.Errorf("%w: %v", ErrInvalidToolArguments, err)
		}
	}
	result, err := t.handler(ctx, args, emit)
	if err != nil {
		// 保留原始error作为错误链一部分，后续可通过errors.Is / errors.As判断
		return ToolResult{}, fmt.Errorf("%w: %s: %w", ErrToolExecution, t.spec.Name(), err)
	}
	if err := result.Validate(); err != nil {
		return ToolResult{}, fmt.Errorf("%w: tool %s returned invalid result: %v", ErrToolExecution, t.spec.Name(), err)
	}
	return result, nil
}

// decodeToolArguments 把原始 JSON 对象解码成工具自己的 Go 参数类型。
func decodeToolArguments[T any](arguments json.RawMessage) (T, error) {
	var zero T

	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()

	// 这里不调用 DisallowUnknownFields。
	// 原因是 unknown field 是否允许属于 JSON Schema 的 additionalProperties 语义
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("%w: decode JSON arguments: %v", ErrInvalidToolArguments, err)
	}
	if err := ensureDecoderEOF(decoder); err != nil {
		return zero, fmt.Errorf("%w: arguments must contain exactly one JSON value: %v", ErrInvalidToolArguments, err)
	}
	return value, nil
}

// 编译期断言：任意具体 T 的 *TypedTool[T] 都应满足 Tool。
// Go 目前不能对“所有 T”写一条泛型接口断言，因此用一个示例类型触发方法集检查。
var _ Tool = (*TypedTool[struct{}])(nil)
