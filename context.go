package po

import "context"

// ContextBuildInput 是每次模型调用前，Agent Core 交给 ContextBuilder 的完整输入。
// Messages 是 Run 内已经发生的 Transcript 快照；Builder 可以裁剪、压缩或临时注入上下文。
type ContextBuildInput struct {
	SystemPrompt    string
	Messages        []Message
	Tools           []ToolSpec
	Model           ModelInfo
	MaxOutputTokens int
}

// ContextBuildResult 是当前这一次 Model Request 真正要看到的消息视图。
// 它不是新的 Transcript。这里返回的临时 Summary / Projection 不会自动进入 Session。
type ContextBuildResult struct {
	Messages []Message
}

// ContextBuilder 是 Agent Core 暴露的最小 Context Engineering 扩展点。
// Core 只负责在每次 Model Request 前调用它，不知道实现到底使用最近窗口、LLM Summary、
// RAG，还是完全不做压缩。这样 Context policy 可以属于 Session / Product 层。
type ContextBuilder interface {
	Build(ctx context.Context, input ContextBuildInput) (ContextBuildResult, error)
}

// ContextBuilderFunc 让小型策略可以直接使用函数，而不用额外定义 struct。
type ContextBuilderFunc func(context.Context, ContextBuildInput) (ContextBuildResult, error)

func (f ContextBuilderFunc) Build(ctx context.Context, input ContextBuildInput) (ContextBuildResult, error) {
	return f(ctx, input)
}

// RunOptions 保存只属于“这一份 Run”的可选机制。
//
// ContextBuilder 放在 RunOptions 而不是 AgentConfig，是因为同一个可复用 Agent 可以同时
// 服务多个 Session，而每个 Session 的 Compaction checkpoint / Context policy 都可能不同。
type RunOptions struct {
	ContextBuilder ContextBuilder

	// ProjectInitialContext lets a Session keep the complete durable transcript
	// separately while the Run state owns only the ContextBuilder projection.
	// Direct Agent callers leave this false and retain the historical behavior in
	// which RunResult.Messages contains their complete input transcript.
	ProjectInitialContext bool
}
