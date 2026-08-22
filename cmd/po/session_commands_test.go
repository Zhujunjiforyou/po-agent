package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/session"
	sessionjsonl "github.com/lemonzjj/po-agent-go/session/jsonl"
)

func TestSessionInspectAndRecover(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	journal, err := sessionjsonl.Create(path, "session-test", now)
	if err != nil {
		t.Fatal(err)
	}
	user, err := po.NewUserTextMessage("user-1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendMessages(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := journal.BeginRun(ctx, session.PendingRun{
		AttemptID:     "attempt-user-1",
		UserMessageID: user.MessageID(),
		StartedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runSession(ctx, []string{"inspect", "--file", path}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("inspect code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "recovery=required") || !strings.Contains(output, "attempt-user-1") {
		t.Fatalf("inspect output = %q", output)
	}

	stdout.Reset()
	stderr.Reset()
	code = runSession(ctx, []string{"recover", "--file", path, "--note", "verified workspace state"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("recover code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "resolved attempt attempt-user-1") {
		t.Fatalf("recover output = %q", stdout.String())
	}

	stdout.Reset()
	code = runSession(ctx, []string{"inspect", "--file", path}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "recovery=none") {
		t.Fatalf("second inspect code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestSessionRecoverRequiresNote(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runSession(context.Background(), []string{"recover", "--file", "session.jsonl"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--note NOTE") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
