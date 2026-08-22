package po_test

import (
	"context"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
)

func TestContextBuilderChangesModelViewButNotRunTranscript(t *testing.T) {
	history1, _ := po.NewUserTextMessage("u-old", "old history")
	history2, _ := po.NewAssistantMessage("a-old", po.TextPart("old reply"))
	current, _ := po.NewUserTextMessage("u-new", "current task")
	final, _ := po.NewAssistantMessage("a-new", po.TextPart("done"))
	response, _ := po.NewModelResponse(final, po.ModelStopEndTurn, po.Usage{}, "resp-1")

	model, err := scripted.New(scripted.DefaultInfo(), scripted.Respond(response))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := po.NewAgent(po.AgentConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	builder := po.ContextBuilderFunc(func(ctx context.Context, input po.ContextBuildInput) (po.ContextBuildResult, error) {
		return po.ContextBuildResult{Messages: []po.Message{input.Messages[len(input.Messages)-1]}}, nil
	})
	result, err := agent.RunMessagesWithOptions(
		context.Background(),
		[]po.Message{history1, history2, current},
		po.RunOptions{ContextBuilder: builder},
	)
	if err != nil {
		t.Fatal(err)
	}

	requests := model.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if got := len(requests[0].Messages()); got != 1 {
		t.Fatalf("model context messages = %d, want 1", got)
	}
	if got := len(result.Messages()); got != 4 {
		t.Fatalf("run transcript messages = %d, want 4", got)
	}
}
