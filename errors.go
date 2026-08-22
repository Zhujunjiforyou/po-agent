package po

import "errors"

// 协议公开错误类别

var (
	// ErrInvalidMessage 表示一条消息违反了消息协议。
	// 例如：消息 ID 为空、UserMessage 包含 Tool Call、ToolResult 缺少关联 ID。
	ErrInvalidMessage = errors.New("invalid message")

	// ErrInvalidContentPart 表示消息中的某一个内容片段不合法。
	// 例如：tool_call 类型的片段没有 ToolCall 数据，或者同时包含 Text。
	ErrInvalidContentPart = errors.New("invalid content part")

	// ErrInvalidToolCall 表示模型生成的 Tool Call 在协议层不完整。
	// 这里只检查 ID、工具名和 JSON 结构；具体参数是否合法由后续 Schema 层负责。
	ErrInvalidToolCall = errors.New("invalid tool call")

	// ErrUnsupportedWireVersion 表示磁盘或网络中的消息版本不是当前程序支持的版本。
	// 单独定义这个错误，后续 Session 迁移时可以精确识别“版本不兼容”。
	ErrUnsupportedWireVersion = errors.New("unsupported message wire version")
)

var (
	// ErrInvalidModelInfo，表示模型身份、能力或限制配置自相矛盾
	ErrInvalidModelInfo = errors.New("invalid model info")

	// ErrInvalidModelRequest 表示发送给模型的请求在本地协议层不合法。
	ErrInvalidModelRequest = errors.New("invalid model request")

	// ErrInvalidModelResponse 表示 Provider 映射出的最终响应不满足 Po 协议。
	ErrInvalidModelResponse = errors.New("invalid model response")

	// ErrInvalidToolSpec 表示模型可见的工具说明缺少名称、描述或合法 Schema。
	ErrInvalidToolSpec = errors.New("invalid tool spec")

	// ErrInvalidUsage 表示 Provider 返回了负 Token 或负费用等不可能的计量数据。
	ErrInvalidUsage = errors.New("invalid model usage")

	// ErrInvalidModelDelta 表示流式模型增量的类型与载荷字段不匹配。
	ErrInvalidModelDelta = errors.New("invalid model delta")
)

var (
	// ErrInvalidTool 表示本地可执行工具定义不完整。
	ErrInvalidTool = errors.New("invalid tool")

	// ErrInvalidToolArguments 表示 ToolCall 参数无法解码或不满足工具业务约束。
	ErrInvalidToolArguments = errors.New("invalid tool arguments")

	// ErrToolNameMismatch 表示 ToolCall 指向的名称与当前执行工具不一致。
	ErrToolNameMismatch = errors.New("tool name mismatch")

	// ErrInvalidToolResult 表示工具声称成功，却返回了不符合协议的结果。
	ErrInvalidToolResult = errors.New("invalid tool result")

	// ErrInvalidToolUpdate 表示工具进度更新中的消息或进度值不合法。
	ErrInvalidToolUpdate = errors.New("invalid tool update")

	// ErrToolExecution 表示工具业务函数执行失败或返回了无效结果。
	ErrToolExecution = errors.New("tool execution failed")
)

var (
	// ErrInvalidToolRegistry 表示 Registry 接收到 nil Tool、非法 Spec 等配置错误。
	ErrInvalidToolRegistry = errors.New("invalid tool registry")

	// ErrDuplicateTool 表示相同稳定名称被注册了两次。
	// 这里选择立即失败，而不是“后注册覆盖前注册”，因为静默覆盖会让行为很难追踪。
	ErrDuplicateTool = errors.New("duplicate tool")

	// ErrToolNotFound 将在 Agent Loop 查找模型请求的工具失败时使用。
	ErrToolNotFound = errors.New("tool not found")

	// ErrInvalidSchema 表示 Schema 自身非法，不应该把这类错误归咎于模型参数。
	ErrInvalidSchema = errors.New("invalid schema")

	// ErrToolArgumentValidation 表示 ToolCall.Arguments 没有满足 ToolSpec.InputSchema。
	ErrToolArgumentValidation = errors.New("tool argument validation failed")
)

var (
	// ErrRunTimeout 表示整个 Agent Run 到达最长运行时间。
	ErrRunTimeout = errors.New("agent run timed out")

	// ErrModelTimeout 表示某一次 Model.Generate 超过单次模型 Timeout。
	ErrModelTimeout = errors.New("model call timed out")

	// ErrToolTimeout 表示某一次 Tool.Execute 超过单次工具 Timeout。
	ErrToolTimeout = errors.New("tool call timed out")
)

var (
	// ErrRunClosed 表示一次 Run 已经结束、正在结束，或已经被 Abort，因此不能再接受 Steering / Follow-up。
	ErrRunClosed = errors.New("agent run is closed")

	// ErrRunAborted 表示调用方通过 RunHandle.Abort 主动终止当前 Run。
	ErrRunAborted = errors.New("agent run aborted")
)
