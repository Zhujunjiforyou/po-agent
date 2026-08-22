package jsonl_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	"github.com/lemonzjj/po-agent-go/session"
	"github.com/lemonzjj/po-agent-go/session/jsonl"
)

func msgUser(t *testing.T, id string) po.UserMessage {
	t.Helper()
	m, err := po.NewUserTextMessage(id, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func msgAssistant(t *testing.T, id string) po.AssistantMessage {
	t.Helper()
	m, err := po.NewAssistantMessage(id, po.TextPart(id))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestJSONLReplaysActiveBranchWithoutDeletingOldBranch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	created := time.Now().UTC()
	file, err := jsonl.Create(path, "s1", created)
	if err != nil {
		t.Fatal(err)
	}

	entries := []session.Entry{
		{ID: "u1", ParentID: "", Timestamp: created, Message: msgUser(t, "u1")},
		{ID: "a1", ParentID: "u1", Timestamp: created, Message: msgAssistant(t, "a1")},
		{ID: "u2", ParentID: "a1", Timestamp: created, Message: msgUser(t, "u2")},
		{ID: "a2", ParentID: "u2", Timestamp: created, Message: msgAssistant(t, "a2")},
	}
	if err := file.AppendEntries(context.Background(), entries...); err != nil {
		t.Fatal(err)
	}
	if err := file.SetHead(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	entry := session.Entry{ID: "u3", ParentID: "a1", Timestamp: created, Message: msgUser(t, "u3")}
	if err := file.AppendEntries(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, state, err := jsonl.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(state.Entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(state.Entries))
	}
	ids := make([]string, 0, len(state.Messages))
	for _, message := range state.Messages {
		ids = append(ids, message.MessageID())
	}
	if strings.Join(ids, ",") != "u1,a1,u3" {
		t.Fatalf("active replay path = %v", ids)
	}
}

func TestUnclosedRunRequiresExplicitRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	created := time.Now().UTC()
	file, err := jsonl.Create(path, "s1", created)
	if err != nil {
		t.Fatal(err)
	}
	user := msgUser(t, "u1")
	entry := session.Entry{ID: user.MessageID(), Timestamp: created, Message: user}
	if err := file.AppendEntries(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	pending := session.PendingRun{AttemptID: "attempt-u1", UserMessageID: "u1", StartedAt: created}
	if err := file.BeginRun(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, state, err := jsonl.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if state.Pending == nil {
		t.Fatal("expected unresolved run marker")
	}

	sess, err := session.Resume(state, reopened)
	if err != nil {
		t.Fatal(err)
	}
	step := scripted.Must(scripted.Reply("a2", "ok", po.Usage{}))
	model := scripted.MustNew(scripted.DefaultInfo(), step)
	agent, err := po.NewAgent(po.AgentConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	newUser := msgUser(t, "u2")
	if _, err := sess.Start(context.Background(), agent, newUser); !errors.Is(err, session.ErrRecoveryRequired) {
		t.Fatalf("Start error = %v, want ErrRecoveryRequired", err)
	}
	if err := sess.ResolveRecovery(context.Background(), "workspace inspected manually"); err != nil {
		t.Fatal(err)
	}
	if _, ok := sess.Recovery(); ok {
		t.Fatal("recovery marker should be cleared")
	}
}

func TestV1SessionUpgradesByAppendingFormatRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	created := time.Now().UTC()
	user := msgUser(t, "u1")
	payload, err := po.MarshalMessage(user)
	if err != nil {
		t.Fatal(err)
	}

	header := map[string]any{"type": "session", "version": 1, "session_id": "legacy", "created_at": created}
	message := map[string]any{"type": "message", "sequence": 1, "timestamp": created, "message": json.RawMessage(payload)}
	h, _ := json.Marshal(header)
	m, _ := json.Marshal(message)
	if err := os.WriteFile(path, append(append(h, '\n'), append(m, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}

	file, state, err := jsonl.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 1 {
		t.Fatalf("legacy messages = %d, want 1", len(state.Messages))
	}
	if err := file.AppendMessages(context.Background(), msgUser(t, "u2")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"format_upgrade"`) {
		t.Fatal("v1 file did not receive append-only format upgrade record")
	}

	reopened, state, err := jsonl.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(state.Messages) != 2 {
		t.Fatalf("replayed messages = %d, want 2", len(state.Messages))
	}
}
