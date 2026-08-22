package po_test

import (
	"context"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
)

// TestToolProgressHasAClosedLifecycle 验证 Tool Progress 不是一条“随时都能发”的旁路，
// 而是严格属于一次 Tool Execute 生命周期：Start 之后可以有任意次 Update，End 之后
// 同一个旧 emitter 再被调用也不能重新污染 Event Stream。
func TestToolProgressHasAClosedLifecycle(t *testing.T) {
	var lateEmit po.ToolUpdateEmitter

	tool := mustReadTool(t, "read_file", func(ctx context.Context, args readArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		lateEmit = emit

		if err := po.EmitToolUpdate(ctx, emit, po.NewProgressToolUpdate("opening file", 0.25)); err != nil {
			return po.ToolResult{}, err
		}
		if err := po.EmitToolUpdate(ctx, emit, po.NewProgressToolUpdate("reading file", 0.75)); err != nil {
			return po.ToolResult{}, err
		}
		return po.NewTextToolResult("# Po Agent", nil, false)
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool("assistant-1", "call-1", "read_file", map[string]any{"path": "README.md"}, po.Usage{})),
		scripted.Must(scripted.Reply("assistant-2", "done", po.Usage{})),
	)

	agent := mustAgent(t, model, mustRegistry(t, tool))

	var toolEvents []po.AgentEventType
	unsubscribe := agent.Subscribe(func(_ context.Context, event po.AgentEvent) {
		switch event.Type() {
		case po.EventToolStart, po.EventToolUpdate, po.EventToolEnd:
			toolEvents = append(toolEvents, event.Type())
		}
	})
	defer unsubscribe()

	if _, err := agent.Run(context.Background(), mustUser(t, "read README")); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []po.AgentEventType{
		po.EventToolStart,
		po.EventToolUpdate,
		po.EventToolUpdate,
		po.EventToolEnd,
	}
	if len(toolEvents) != len(want) {
		t.Fatalf("tool events = %v, want %v", toolEvents, want)
	}
	for i := range want {
		if toolEvents[i] != want[i] {
			t.Fatalf("tool event %d = %q, want %q; all=%v", i, toolEvents[i], want[i], toolEvents)
		}
	}

	// 模拟一个写得不够严谨的 Tool：Execute 已经返回，却有后台 goroutine 保存了
	// emitter 并继续发送。Runtime 应把这个更新吞掉，而不是让 ToolEnd 后再出现 Update。
	if lateEmit == nil {
		t.Fatal("tool did not receive an update emitter")
	}
	if err := po.EmitToolUpdate(context.Background(), lateEmit, po.NewToolUpdate("too late")); err != nil {
		t.Fatalf("late EmitToolUpdate() error = %v", err)
	}
	if len(toolEvents) != len(want) {
		t.Fatalf("late update escaped the closed lifecycle: %v", toolEvents)
	}
}
