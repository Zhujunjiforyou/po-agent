package jsonl_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
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

func TestOpenTruncatesIncompleteCrashTailBeforeAppending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")

	file, err := jsonl.Create(path, "session-1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := file.AppendMessages(context.Background(), user(t, "user-1", "first")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	// 模拟进程在写下一条 JSON record 中途崩溃：尾部没有 newline，也不是完整 JSON。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","sequence":2`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, state, err := jsonl.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(state.Messages) != 1 {
		t.Fatalf("loaded messages = %d, want 1", len(state.Messages))
	}

	if err := reopened.AppendMessages(context.Background(), user(t, "user-2", "second")); err != nil {
		t.Fatalf("AppendMessages() after recovery error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	finalFile, finalState, err := jsonl.Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer finalFile.Close()

	if len(finalState.Messages) != 2 {
		t.Fatalf("final messages = %d, want 2", len(finalState.Messages))
	}
}

func TestOpenRejectsCorruptCompleteMiddleRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")

	file, err := jsonl.Create(path, "session-1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := file.AppendMessages(context.Background(), user(t, "user-1", "first")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not-json}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if reopened, _, err := jsonl.Open(path); err == nil {
		reopened.Close()
		t.Fatal("expected corrupt complete record to be rejected")
	}
}
