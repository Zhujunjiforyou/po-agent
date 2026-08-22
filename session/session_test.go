package session_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	"github.com/lemonzjj/po-agent-go/session"
	"github.com/lemonzjj/po-agent-go/session/jsonl"
)

func user(t *testing.T, id, text string) po.UserMessage {
	t.Helper()
	message, err := po.NewUserTextMessage(id, text)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestSessionPersistsAndResumesTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	journal, err := jsonl.Create(path, "session-1", createdAt)
	if err != nil {
		t.Fatal(err)
	}

	current, err := session.New("session-1", createdAt, journal)
	if err != nil {
		t.Fatal(err)
	}

	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.Reply("assistant-1", "first answer", po.Usage{})),
	)
	agent, err := po.NewAgent(po.AgentConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := current.Prompt(context.Background(), agent, user(t, "user-1", "first")); err != nil {
		t.Fatalf("first Prompt() error = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	resumedJournal, state, err := jsonl.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer resumedJournal.Close()

	if len(state.Messages) != 2 {
		t.Fatalf("loaded messages = %d, want 2", len(state.Messages))
	}

	resumed, err := session.Resume(state, resumedJournal)
	if err != nil {
		t.Fatal(err)
	}

	secondModel := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "second answer", po.Usage{})),
			scripted.ExpectMessageCount(3),
		),
	)
	secondAgent, err := po.NewAgent(po.AgentConfig{Model: secondModel})
	if err != nil {
		t.Fatal(err)
	}

	result, err := resumed.Prompt(context.Background(), secondAgent, user(t, "user-2", "second"))
	if err != nil {
		t.Fatalf("second Prompt() error = %v", err)
	}
	if len(result.Messages()) != 4 {
		t.Fatalf("result messages = %d, want 4", len(result.Messages()))
	}
}

type blockingModel struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *blockingModel) Info() po.ModelInfo {
	return scripted.DefaultInfo()
}

func (m *blockingModel) Generate(
	ctx context.Context,
	request po.ModelRequest,
	emit po.DeltaEmitter,
) (po.ModelResponse, error) {
	m.once.Do(func() { close(m.entered) })

	select {
	case <-m.release:
	case <-ctx.Done():
		return po.ModelResponse{}, ctx.Err()
	}

	message, err := po.NewAssistantMessage("assistant-blocking", po.TextPart("done"))
	if err != nil {
		return po.ModelResponse{}, err
	}
	return po.NewModelResponse(message, po.ModelStopEndTurn, po.Usage{}, "response-blocking")
}

func TestSessionRejectsConcurrentPrompt(t *testing.T) {
	model := &blockingModel{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	agent, err := po.NewAgent(po.AgentConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	current, err := session.New("session-1", time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := current.Prompt(context.Background(), agent, user(t, "user-1", "first"))
		firstDone <- err
	}()

	select {
	case <-model.entered:
	case <-time.After(time.Second):
		t.Fatal("first prompt did not reach model")
	}

	if _, err := current.Prompt(context.Background(), agent, user(t, "user-2", "second")); !errors.Is(err, session.ErrSessionBusy) {
		t.Fatalf("second Prompt() error = %v, want ErrSessionBusy", err)
	}

	close(model.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Prompt() error = %v", err)
	}
}
