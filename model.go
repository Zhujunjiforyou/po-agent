package po

import (
	"context"
	"fmt"
	"strings"
)

// Model 是Po Agent与任意大模型实现之间的最小边界
// Agent Core只依赖这个接口，不应该直接依赖LLM厂商或者具体SDK的请求/响应

type Model interface {
	// Info 返回模型的稳定身份，能力和限制
	Info() ModelInfo

	// Generate根据当前请求生成一条完整的AssistantMessage

	// emit可选流式增量
	// - 非流式Model可以完全不调用emit
	// - 流失provider可以在最终响应完成前持续调用emit
	// - emit为nil时表示调用者不关心增量
	Generate(ctx context.Context, request ModelRequest, emit DeltaEmitter) (ModelResponse, error)
}

// ModelCapabilities 描述“这个模型能做什么”。
//
// 同一 Provider 的不同模型能力可能不同，
// OpenAI-compatible 服务也常常只实现协议的一部分。
type ModelCapabilities struct {
	// Streaming 表示实现能够在最终响应前发送 ModelDelta。
	Streaming bool

	// Tools 表示模型能够接收 ToolSpec，并返回结构化 Tool Call。
	Tools bool

	// ParallelToolCalls 表示模型可能在同一条 AssistantMessage 中请求多个工具。
	// 这里只描述“模型可能这样输出”，真正是否并发执行由后续 Scheduler 决定。
	ParallelToolCalls bool

	// StructuredOutput 表示 Provider 支持 JSON Schema、Grammar 等约束输出能力。
	// Po 的最终结果工具不依赖这个能力，但 Provider 适配器可以利用它增强可靠性。
	StructuredOutput bool

	// Reasoning 表示模型能够返回或接受独立的思考/推理配置。
	Reasoning bool

	// Vision 表示模型可以接收图片输入。
	// 协议 v1 当前只实现文本用户消息，但提前保留能力位，避免用 Provider 名称猜测。
	Vision bool
}

// 检查能力组合自身是否矛盾
func (c ModelCapabilities) Validate() error {
	// 并行工具调用时工具调用子能力
	if c.ParallelToolCalls && !c.Tools {
		return fmt.Errorf("%w: parallel tool calls require tool support", ErrInvalidModelInfo)
	}
	return nil
}

// 描述模型的上下文与单次输出上限
type ModelLimits struct {
	// ContextWindow 是一次请求中“输入 + 最大输出”可使用的总 Token 窗口。
	ContextWindow int
	// MaxOutputTokens 是 Provider 允许单次生成的最大输出 Token 数。
	MaxOutputTokens int
}

// Validate 检查限制值是否合理。
func (l ModelLimits) Validate() error {
	if l.ContextWindow <= 0 {
		return fmt.Errorf(
			"%w: context window must be positive",
			ErrInvalidModelInfo,
		)
	}
	if l.MaxOutputTokens <= 0 {
		return fmt.Errorf(
			"%w: max output tokens must be positive",
			ErrInvalidModelInfo,
		)
	}
	if l.MaxOutputTokens > l.ContextWindow {
		return fmt.Errorf(
			"%w: max output tokens %d exceed context window %d",
			ErrInvalidModelInfo,
			l.MaxOutputTokens,
			l.ContextWindow,
		)
	}
	return nil
}

// ModelInfo 是 Provider-neutral 的模型说明。
// 模型是谁，支持什么，上下文多大
type ModelInfo struct {
	Provider     string
	ID           string
	DisplayName  string
	Capabilities ModelCapabilities
	Limits       ModelLimits
}

// Validate 检查模型说明是否完整。
func (i ModelInfo) Validate() error {
	if strings.TrimSpace(i.Provider) == "" {
		return fmt.Errorf("%w: provider is required", ErrInvalidModelInfo)
	}
	if strings.TrimSpace(i.ID) == "" {
		return fmt.Errorf("%w: model id is required", ErrInvalidModelInfo)
	}
	if err := i.Capabilities.Validate(); err != nil {
		return err
	}
	if err := i.Limits.Validate(); err != nil {
		return err
	}
	return nil
}

// Name 返回适合界面展示的名称。
func (i ModelInfo) Name() string {
	if strings.TrimSpace(i.DisplayName) != "" {
		return i.DisplayName
	}
	return i.ID
}

// QualifiedID 返回不会轻易与其他 Provider 冲突的模型标识。
func (i ModelInfo) QualifiedID() string {
	return i.Provider + "/" + i.ID
}
