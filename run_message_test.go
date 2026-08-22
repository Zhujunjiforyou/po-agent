package po_test

import (
	"context"
	"strings"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
)

func TestRunMessagesContinuesExistingTranscript(t *testing.T) {
	firstUser, err := po.NewUserTextMessage("user-1", "first")
	if err != nil {
		t.Fatal(err)
	}
	historicalAssistant, err := po.NewAssistantMessage("assistant-old", po.TextPart("old answer"))
	if err != nil {
		t.Fatal(err)
	}
	secondUser, err := po.NewUserTextMessage("user-2", "second")
	if err != nil {
		t.Fatal(err)
	}

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-new", "continued", po.Usage{})),
			scripted.ExpectMessageCount(3),
		),
	)

	agent, err := po.NewAgent(po.AgentConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	result, err := agent.RunMessages(
		context.Background(),
		[]po.Message{firstUser, historicalAssistant, secondUser},
	)
	if err != nil {
		t.Fatalf("RunMessages() error = %v", err)
	}
	if len(result.Messages()) != 4 {
		t.Fatalf("messages = %d, want 4", len(result.Messages()))
	}
}

func TestRunMessagesRejectsAssistantAsLastMessage(t *testing.T) {
	user, err := po.NewUserTextMessage("user-1", "first")
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := po.NewAssistantMessage("assistant-1", po.TextPart("done"))
	if err != nil {
		t.Fatal(err)
	}

	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.Reply("unused", "unused", po.Usage{})))
	agent, err := po.NewAgent(po.AgentConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	_, err = agent.RunMessages(context.Background(), []po.Message{user, assistant})
	if err == nil || !strings.Contains(err.Error(), "must end with user or tool_result") {
		t.Fatalf("RunMessages() error = %v", err)
	}
}
