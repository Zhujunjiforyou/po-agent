package jsonl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
)

func TestPoisonedJournalRejectsFurtherWritesUntilReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	file, err := Create(path, "s1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	file.mu.Lock()
	file.poisoned = true
	file.mu.Unlock()

	message, err := po.NewUserTextMessage("u1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.AppendMessages(context.Background(), message); !errors.Is(err, ErrUncertainJournal) {
		t.Fatalf("AppendMessages error = %v, want ErrUncertainJournal", err)
	}

	// Close + Open 会重新 replay 当前 durable 文件，并得到一个新的、未 poisoned handle。
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.poisoned {
		t.Fatal("reopened journal must not inherit in-memory poison state")
	}

	// 确认测试没有依赖特殊文件权限或不存在的路径。
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
