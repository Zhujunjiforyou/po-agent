// Package provider 包含不应进入 Agent 核心的模型服务集成类型。
package provider

import (
	"fmt"
	"time"
)

// Error 是 Provider Adapter 向模型运行层暴露的统一调用错误。
// OpenAI-compatible 和其他模型服务都应该在自己的 Adapter 中把厂商错误转换成这一稳定结构。
type Error struct {
	// Op 表示失败发生在哪个 Provider 操作。
	//
	// 它主要服务日志和定位，不建议让 Agent Core
	// 根据 Op 字符串做业务分支。
	Op string

	// StatusCode 保存 HTTP 状态码。
	//
	// DNS、TLS、连接建立等尚未得到 HTTP Response 的错误，
	// 可以保持为 0。
	StatusCode int

	// Code 保存 Provider 提供的稳定机器错误码。
	//
	// 如果 Provider 没有这样的字段，可以为空。
	Code string

	// RetryAfter 表示 Provider 明确建议客户端至少等待多久。
	//
	// 0 表示 Provider 没有给出明确等待时间。
	RetryAfter time.Duration

	// Retryable 是 Provider Adapter 基于厂商错误语义
	// 做出的判断。
	//
	// Retry / routing 等可选模型装饰器不应该再次解析某家厂商的错误字符串，
	// 而只消费这个稳定语义。
	Retryable bool

	// Err 保存底层根因。
	//
	// Unwrap() 会把它暴露给 errors.Is / errors.As。
	Err error
}

// Error 实现 Go 内置 error 接口。
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	if e.StatusCode != 0 {
		return fmt.Sprintf("provider %s failed with HTTP %d: %v", e.Op, e.StatusCode, e.Err)
	}

	return fmt.Sprintf("provider %s failed: %v", e.Op, e.Err)
}

// Unwrap 保留底层错误链。
//
// 于是外层即使再通过 fmt.Errorf("%w") 包装，
// errors.Is / errors.As 仍然可以继续向下查找。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}
