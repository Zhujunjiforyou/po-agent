package po_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	"github.com/lemonzjj/po-agent-go/schema/basic"
)

type readArgs struct {
	Path string `json:"path"`
}

func mustSpec(t *testing.T, name string) po.ToolSpec {
	t.Helper()
	schema := []byte(`{
		"type":"object",
		"properties":{"path":{"type":"string","minLength":1}},
		"required":["path"],
		"additionalProperties":false
	}`)
	spec, err := po.NewToolSpec(name, "read one text file", schema)
	if err != nil {
		t.Fatalf("NewToolSpec() error = %v", err)
	}
	return spec
}

func mustReadTool(t *testing.T, name string, handler po.ToolHandler[readArgs]) po.Tool {
	t.Helper()
	tool, err := po.NewTypedTool(mustSpec(t, name), nil, handler)
	if err != nil {
		t.Fatalf("NewTypedTool() error = %v", err)
	}
	return tool
}

func mustRegistry(t *testing.T, tools ...po.Tool) *po.ToolRegistry {
	t.Helper()
	registry := po.NewToolRegistry()
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}
	return registry
}

func mustUser(t *testing.T, text string) po.UserMessage {
	t.Helper()
	message, err := po.NewUserTextMessage("user-1", text)
	if err != nil {
		t.Fatalf("NewUserTextMessage() error = %v", err)
	}
	return message
}

func mustAgent(t *testing.T, model po.Model, registry *po.ToolRegistry) *po.Agent {
	t.Helper()
	agent, err := po.NewAgent(po.AgentConfig{
		Model:     model,
		Tools:     registry,
		Validator: basic.New(),
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	return agent
}

func lastToolResult(request po.ModelRequest) (po.ToolResultMessage, error) {
	messages := request.Messages()
	if len(messages) == 0 {
		return po.ToolResultMessage{}, errors.New("no messages")
	}
	result, ok := messages[len(messages)-1].(po.ToolResultMessage)
	if !ok {
		return po.ToolResultMessage{}, fmt.Errorf("last message is %T", messages[len(messages)-1])
	}
	return result, nil
}

// TestAgentRunExecutesToolAndFeedsResultBackToModel 验证最核心的闭环：
// 模型请求工具 → Runtime 执行 → Tool Result 写回 → 模型在下一 Turn 看到结果。
func TestAgentRunExecutesToolAndFeedsResultBackToModel(t *testing.T) {
	readTool := mustReadTool(t, "read_file", func(
		ctx context.Context,
		args readArgs,
		emit po.ToolUpdateEmitter,
	) (po.ToolResult, error) {
		if args.Path != "README.md" {
			t.Fatalf("path = %q, want README.md", args.Path)
		}
		return po.NewTextToolResult("# Po Agent", nil, false)
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.WithCheck(
			scripted.Must(scripted.CallTool(
				"assistant-1", "call-1", "read_file",
				map[string]any{"path": "README.md"}, po.Usage{InputTokens: 10, OutputTokens: 3},
			)),
			scripted.ExpectTool("read_file"),
		),
		scripted.WithCheck(
			scripted.Must(scripted.Reply(
				"assistant-2", "项目名称是 Po Agent。", po.Usage{InputTokens: 15, OutputTokens: 7},
			)),
			scripted.ExpectLastToolResult("call-1"),
		),
	)

	result, err := mustAgent(t, model, mustRegistry(t, readTool)).Run(
		context.Background(),
		mustUser(t, "读取 README.md 并告诉我项目名"),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.StopReason(); got != po.RunStopCompleted {
		t.Fatalf("StopReason() = %q, want %q", got, po.RunStopCompleted)
	}
	if got := result.FinalText(); got != "项目名称是 Po Agent。" {
		t.Fatalf("FinalText() = %q", got)
	}
	if got := len(result.Messages()); got != 4 {
		t.Fatalf("len(Messages()) = %d, want 4", got)
	}
	if got := result.Usage().TotalTokens(); got != 35 {
		t.Fatalf("TotalTokens() = %d, want 35", got)
	}
}

// TestUnknownToolBecomesRecoverableToolResult 证明未知工具不是进程级错误。
// Runtime 应把它编码成 is_error=true 的 ToolResult，让模型自己调整策略。
func TestUnknownToolBecomesRecoverableToolResult(t *testing.T) {
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool(
			"assistant-1", "call-1", "missing_tool", map[string]any{}, po.Usage{},
		)),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "这个工具不可用。", po.Usage{})),
			func(request po.ModelRequest) error {
				result, err := lastToolResult(request)
				if err != nil {
					return err
				}
				if !result.IsError() {
					return errors.New("tool result should be an error")
				}
				if !strings.Contains(result.Parts()[0].Text, "not available") {
					return fmt.Errorf("unexpected error text %q", result.Parts()[0].Text)
				}
				return nil
			},
		),
	)

	result, err := mustAgent(t, model, po.NewToolRegistry()).Run(
		context.Background(),
		mustUser(t, "调用不存在的工具"),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalText() != "这个工具不可用。" {
		t.Fatalf("FinalText() = %q", result.FinalText())
	}
}

// TestSchemaFailureDoesNotExecuteTool 验证“校验发生在副作用之前”。
// 参数不合法时 handler 的调用次数必须保持为 0。
func TestSchemaFailureDoesNotExecuteTool(t *testing.T) {
	calls := 0
	readTool := mustReadTool(t, "read_file", func(
		context.Context,
		readArgs,
		po.ToolUpdateEmitter,
	) (po.ToolResult, error) {
		calls++
		return po.NewTextToolResult("should not run", nil, false)
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool(
			"assistant-1", "call-1", "read_file",
			map[string]any{"path": ""}, po.Usage{},
		)),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "参数有问题。", po.Usage{})),
			func(request po.ModelRequest) error {
				result, err := lastToolResult(request)
				if err != nil {
					return err
				}
				if !result.IsError() {
					return errors.New("expected validation error result")
				}
				return nil
			},
		),
	)

	_, err := mustAgent(t, model, mustRegistry(t, readTool)).Run(
		context.Background(), mustUser(t, "read"),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("tool calls = %d, want 0", calls)
	}
}

// TestToolPanicBecomesRecoverableToolResult 验证自定义工具 panic 不会直接炸掉 Agent 进程。
// panic 会被 Runtime 边界捕获并转换成模型可见的错误结果。
func TestToolPanicBecomesRecoverableToolResult(t *testing.T) {
	panicTool := mustReadTool(t, "read_file", func(
		context.Context,
		readArgs,
		po.ToolUpdateEmitter,
	) (po.ToolResult, error) {
		panic("boom")
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool(
			"assistant-1", "call-1", "read_file",
			map[string]any{"path": "README.md"}, po.Usage{},
		)),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "工具崩溃了。", po.Usage{})),
			func(request po.ModelRequest) error {
				result, err := lastToolResult(request)
				if err != nil {
					return err
				}
				if !result.IsError() || !strings.Contains(result.Parts()[0].Text, "panicked") {
					return errors.New("panic was not converted to tool error")
				}
				return nil
			},
		),
	)

	_, err := mustAgent(t, model, mustRegistry(t, panicTool)).Run(
		context.Background(), mustUser(t, "read"),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

// TestLengthToolCallIsNeverExecuted 验证长度截断的 Tool Call 不会被执行。
// 即使被截断的参数目前“碰巧能解析”，只要 stop reason 是 length，就绝不能执行。
func TestLengthToolCallIsNeverExecuted(t *testing.T) {
	executed := 0
	readTool := mustReadTool(t, "read_file", func(
		context.Context,
		readArgs,
		po.ToolUpdateEmitter,
	) (po.ToolResult, error) {
		executed++
		return po.NewTextToolResult("unsafe", nil, false)
	})

	call, err := po.NewToolCall("call-1", "read_file", map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	message, err := po.NewAssistantMessage("assistant-1", po.ToolCallPart(call))
	if err != nil {
		t.Fatal(err)
	}
	response, err := po.NewModelResponse(message, po.ModelStopLength, po.Usage{}, "response-1")
	if err != nil {
		t.Fatal(err)
	}

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Respond(response),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "我重新考虑，不需要执行。", po.Usage{})),
			scripted.ExpectLastToolResult("call-1"),
		),
	)

	_, err = mustAgent(t, model, mustRegistry(t, readTool)).Run(
		context.Background(), mustUser(t, "read"),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if executed != 0 {
		t.Fatalf("tool executed %d times, want 0", executed)
	}
}

// TestDuplicateToolCallIDFailsBeforeExecution 验证 Tool Call 的关联键必须唯一。
// 如果 ID 重复，Runtime 在任何工具执行前失败，避免生成无法可靠配对的结果。
func TestDuplicateToolCallIDFailsBeforeExecution(t *testing.T) {
	call1, _ := po.NewToolCall("same", "read_file", map[string]any{"path": "a"})
	call2, _ := po.NewToolCall("same", "read_file", map[string]any{"path": "b"})
	message, err := po.NewAssistantMessage(
		"assistant-1",
		po.ToolCallPart(call1),
		po.ToolCallPart(call2),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := po.NewModelResponse(message, po.ModelStopToolCall, po.Usage{}, "response-1")
	if err != nil {
		t.Fatal(err)
	}

	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Respond(response))
	readTool := mustReadTool(t, "read_file", func(
		context.Context, readArgs, po.ToolUpdateEmitter,
	) (po.ToolResult, error) {
		t.Fatal("tool must not execute for duplicate call IDs")
		return po.ToolResult{}, nil
	})

	result, err := mustAgent(t, model, mustRegistry(t, readTool)).Run(
		context.Background(), mustUser(t, "read"),
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate tool call id") {
		t.Fatalf("Run() error = %v, want duplicate id error", err)
	}
	if result.StopReason() != po.RunStopFailed {
		t.Fatalf("StopReason() = %q, want failed", result.StopReason())
	}
}

// TestTerminateToolEndsRunWithoutAnotherModelCall 验证终止工具可以省掉额外一次模型总结。
// 这是结构化最终输出、分类器、审批器等 Agent 很常用的优化。
func TestTerminateToolEndsRunWithoutAnotherModelCall(t *testing.T) {
	finishTool := mustReadTool(t, "finish", func(
		context.Context,
		readArgs,
		po.ToolUpdateEmitter,
	) (po.ToolResult, error) {
		return po.NewToolResult(
			[]po.ContentPart{po.TextPart("最终结构化结果已提交")},
			nil,
			true,
		)
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool(
			"assistant-1", "call-1", "finish",
			map[string]any{"path": "done"}, po.Usage{},
		)),
	)

	result, err := mustAgent(t, model, mustRegistry(t, finishTool)).Run(
		context.Background(), mustUser(t, "finish"),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.StopReason() != po.RunStopToolTerminated {
		t.Fatalf("StopReason() = %q", result.StopReason())
	}
	if model.CallCount() != 1 {
		t.Fatalf("model CallCount() = %d, want 1", model.CallCount())
	}
}
