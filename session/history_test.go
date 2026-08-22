package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
)

func mustUser(t *testing.T, id string) po.UserMessage {
	t.Helper()
	m, err := po.NewUserTextMessage(id, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustAssistant(t *testing.T, id string) po.AssistantMessage {
	t.Helper()
	m, err := po.NewAssistantMessage(id, po.TextPart(id))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHistoryBranchPreservesAbandonedPath(t *testing.T) {
	now := time.Now().UTC()
	h := NewHistory()
	for _, message := range []po.Message{mustUser(t, "u1"), mustAssistant(t, "a1"), mustUser(t, "u2"), mustAssistant(t, "a2")} {
		entry, err := h.PrepareAppend(message, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.AppendEntry(entry); err != nil {
			t.Fatal(err)
		}
	}

	if err := h.SetLeaf("a1"); err != nil {
		t.Fatal(err)
	}
	branchEntry, err := h.PrepareAppend(mustUser(t, "u3"), now)
	if err != nil {
		t.Fatal(err)
	}
	if branchEntry.ParentID != "a1" {
		t.Fatalf("branch parent = %q, want a1", branchEntry.ParentID)
	}
	if err := h.AppendEntry(branchEntry); err != nil {
		t.Fatal(err)
	}

	messages, err := h.Messages()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(messages))
	for _, message := range messages {
		got = append(got, message.MessageID())
	}
	want := []string{"u1", "a1", "u3"}
	if len(got) != len(want) {
		t.Fatalf("active path = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("active path = %v, want %v", got, want)
		}
	}
	if !h.Has("u2") || !h.Has("a2") {
		t.Fatal("abandoned branch was deleted")
	}
}

func TestNilHistoryPrepareAppendReturnsError(t *testing.T) {
	var history *History
	_, err := history.PrepareAppend(mustUser(t, "u1"), time.Now().UTC())
	if !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("PrepareAppend() error = %v, want ErrInvalidSession", err)
	}
}

type failingRunJournal struct {
	appendCalls int
	pending     *PendingRun
}

func (j *failingRunJournal) AppendEntries(ctx context.Context, entries ...Entry) error {
	j.appendCalls++
	if j.appendCalls == 2 {
		return errors.New("disk full")
	}
	return nil
}

func (j *failingRunJournal) SetHead(context.Context, string) error { return nil }

func (j *failingRunJournal) BeginRun(ctx context.Context, pending PendingRun) error {
	copy := pending
	j.pending = &copy
	return nil
}

func (j *failingRunJournal) EndRun(context.Context, string, string, error) error {
	j.pending = nil
	return nil
}

func (j *failingRunJournal) ResolveRun(context.Context, string, string) error {
	j.pending = nil
	return nil
}

func TestSessionDoesNotAdvanceInMemoryBranchWhenRunMessagesFailToPersist(t *testing.T) {
	journal := &failingRunJournal{}
	sess, err := New("s1", time.Now().UTC(), journal)
	if err != nil {
		t.Fatal(err)
	}

	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.Reply("a1", "done", po.Usage{})))
	agent, err := po.NewAgent(po.AgentConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}

	user := mustUser(t, "u1")
	_, err = sess.Prompt(context.Background(), agent, user)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Prompt error = %v, want persistence failure", err)
	}

	// 用户输入在启动 Agent 前已经 durable，所以 active transcript 应保留它；
	// Assistant 输出的 Journal append 失败后，内存不能领先到磁盘不存在的 a1。
	messages := sess.Transcript().Messages()
	if len(messages) != 1 || messages[0].MessageID() != "u1" {
		t.Fatalf("transcript ids after persistence failure = %v, want [u1]", messageIDs(messages))
	}
	if _, ok := sess.Recovery(); !ok {
		t.Fatal("pending recovery marker should remain after persistence failure")
	}
}

func messageIDs(messages []po.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.MessageID())
	}
	return ids
}
