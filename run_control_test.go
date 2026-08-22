package po_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	"github.com/lemonzjj/po-agent-go/schema/basic"
)

func expectLastUserText(want string) scripted.RequestCheck {
	return func(request po.ModelRequest) error {
		messages := request.Messages()
		if len(messages) == 0 {
			return errors.New("request has no messages")
		}

		user, ok := messages[len(messages)-1].(po.UserMessage)
		if !ok {
			return fmt.Errorf("last message is %T, want UserMessage", messages[len(messages)-1])
		}

		parts := user.Parts()
		if len(parts) != 1 || parts[0].Type != po.ContentText {
			return fmt.Errorf("unexpected user parts: %#v", parts)
		}
		if parts[0].Text != want {
			return fmt.Errorf("last user text = %q, want %q", parts[0].Text, want)
		}
		return nil
	}
}

func TestRunHandleSteeringIsInjectedAtNextTurnBoundary(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	tool := mustReadTool(t, "read_file", func(
		ctx context.Context,
		args readArgs,
		emit po.ToolUpdateEmitter,
	) (po.ToolResult, error) {
		close(entered)
		select {
		case <-release:
			return po.NewTextToolResult("tool-result", nil, false)
		case <-ctx.Done():
			return po.ToolResult{}, ctx.Err()
		}
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool(
			"assistant-1",
			"call-1",
			"read_file",
			map[string]any{"path": "README.md"},
			po.Usage{},
		)),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "steering received", po.Usage{})),
			expectLastUserText("change direction"),
		),
	)

	agent := mustAgent(t, model, mustRegistry(t, tool))
	handle, err := agent.Start(context.Background(), mustUser(t, "start"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}

	steering, err := po.NewUserTextMessage("steer-1", "change direction")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Steer(steering); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}

	close(release)

	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.FinalText() != "steering received" {
		t.Fatalf("FinalText() = %q", result.FinalText())
	}
}

func TestFollowUpWaitsUntilAgentWouldOtherwiseStop(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	tool := mustReadTool(t, "read_file", func(
		ctx context.Context,
		args readArgs,
		emit po.ToolUpdateEmitter,
	) (po.ToolResult, error) {
		close(entered)
		select {
		case <-release:
			return po.NewTextToolResult("tool-result", nil, false)
		case <-ctx.Done():
			return po.ToolResult{}, ctx.Err()
		}
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool(
			"assistant-1",
			"call-1",
			"read_file",
			map[string]any{"path": "README.md"},
			po.Usage{},
		)),
		// 当前 ToolResult 会自动触发这一轮模型调用。Follow-up 不能抢到它前面。
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "first work finished", po.Usage{})),
			scripted.ExpectLastToolResult("call-1"),
		),
		// assistant-2 已经自然结束，Runtime 这时才消费 Follow-up。
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-3", "follow-up finished", po.Usage{})),
			expectLastUserText("also summarize"),
		),
	)

	agent := mustAgent(t, model, mustRegistry(t, tool))
	handle, err := agent.Start(context.Background(), mustUser(t, "start"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}

	followUp, err := po.NewUserTextMessage("followup-1", "also summarize")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.FollowUp(followUp); err != nil {
		t.Fatalf("FollowUp() error = %v", err)
	}

	close(release)

	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.FinalText() != "follow-up finished" {
		t.Fatalf("FinalText() = %q", result.FinalText())
	}
	if result.TurnAttempts() != 3 {
		t.Fatalf("TurnAttempts() = %d, want 3", result.TurnAttempts())
	}
}

func TestRunHandleAbortCancelsCurrentToolAndClosesQueues(t *testing.T) {
	entered := make(chan struct{})

	tool := mustReadTool(t, "read_file", func(
		ctx context.Context,
		args readArgs,
		emit po.ToolUpdateEmitter,
	) (po.ToolResult, error) {
		close(entered)
		<-ctx.Done()
		return po.ToolResult{}, ctx.Err()
	})

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool(
			"assistant-1",
			"call-1",
			"read_file",
			map[string]any{"path": "README.md"},
			po.Usage{},
		)),
	)

	agent, err := po.NewAgent(po.AgentConfig{
		Model:     model,
		Tools:     mustRegistry(t, tool),
		Validator: basic.New(),
	})
	if err != nil {
		t.Fatal(err)
	}

	handle, err := agent.Start(context.Background(), mustUser(t, "start"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}

	handle.Abort()

	late, err := po.NewUserTextMessage("late", "too late")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Steer(late); !errors.Is(err, po.ErrRunClosed) {
		t.Fatalf("Steer() after Abort error = %v, want ErrRunClosed", err)
	}

	result, err := handle.Wait()
	if !errors.Is(err, po.ErrRunAborted) {
		t.Fatalf("Wait() error = %v, want ErrRunAborted", err)
	}
	if result.StopReason() != po.RunStopAborted {
		t.Fatalf("StopReason() = %q, want %q", result.StopReason(), po.RunStopAborted)
	}
}
