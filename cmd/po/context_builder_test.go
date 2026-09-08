package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/contextwindow"
	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/session"
)

const protocolContextWindow = 8192

type protocolRequest struct {
	Messages []struct {
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens int `json:"max_tokens"`
}

type protocolProbe struct {
	requests, main, summaries int
	maxMain, maxSummary       int
	failure                   string
}

func (p *protocolProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request protocolRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		p.reject(w, err.Error())
		return
	}
	p.requests++
	inputTokens := 0
	var all strings.Builder
	for _, message := range request.Messages {
		inputTokens += 4 + approximateTextTokens(message.Content)
		all.WriteString(message.Content)
		all.WriteByte('\n')
	}
	if inputTokens+request.MaxTokens > protocolContextWindow {
		p.reject(w, fmt.Sprintf("input=%d output=%d exceeds %d", inputTokens, request.MaxTokens, protocolContextWindow))
		return
	}

	joined := all.String()
	isSummary := len(request.Messages) > 0 && request.Messages[0].Content == summarySystemPrompt
	response := ""
	if isSummary {
		p.summaries++
		p.maxSummary = max(p.maxSummary, inputTokens)
		response = fmt.Sprintf(
			"PROJECT_CODE=%s OWNER=%s PORT=%s BACKUP_POLICY=%s",
			latestFact(joined, "PROJECT_CODE", "ORCHID-731"),
			latestFact(joined, "OWNER", "Lin", "Mei"),
			latestFact(joined, "PORT", "43127", "43128"),
			latestFact(joined, "BACKUP_POLICY", "NEVER_DELETE"),
		)
	} else {
		p.main++
		p.maxMain = max(p.maxMain, inputTokens)
		last := request.Messages[len(request.Messages)-1].Content
		response = "ACK turn=" + fieldValue(last, "TURN_INDEX")
		if strings.Contains(last, "CHECKPOINT_QUERY=TRUE") {
			response = fmt.Sprintf(
				"CHECK code=%s owner=%s port=%s backup=%s",
				latestFact(joined, "PROJECT_CODE", "ORCHID-731"),
				latestFact(joined, "OWNER", "Lin", "Mei"),
				latestFact(joined, "PORT", "43127", "43128"),
				latestFact(joined, "BACKUP_POLICY", "NEVER_DELETE"),
			)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": fmt.Sprintf("context-%d", p.requests),
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": response}, "finish_reason": "stop",
		}},
	})
}

func (p *protocolProbe) reject(w http.ResponseWriter, message string) {
	p.failure = message
	w.WriteHeader(http.StatusBadRequest)
	_, _ = fmt.Fprintf(w, `{"error":{"message":%q}}`, message)
}

func approximateTextTokens(text string) int {
	ascii, nonASCII := 0, 0
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r < utf8.RuneSelf {
			ascii++
		} else {
			nonASCII++
		}
		text = text[size:]
	}
	return (ascii+3)/4 + nonASCII
}

func latestFact(text, key string, values ...string) string {
	position, result := -1, "<missing>"
	for _, value := range values {
		if at := strings.LastIndex(text, key+"="+value); at > position {
			position, result = at, value
		}
	}
	return result
}

func fieldValue(text, key string) string {
	at := strings.LastIndex(text, key+"=")
	if at < 0 {
		return "<missing>"
	}
	value, _, _ := strings.Cut(text[at+len(key)+1:], "\n")
	return strings.TrimSpace(value)
}

func realisticProtocolTurn(turn int, check bool) string {
	fact := ""
	switch turn {
	case 0:
		fact = "PROJECT_CODE=ORCHID-731 OWNER=Lin PORT=43127 BACKUP_POLICY=NEVER_DELETE\n"
	case 12:
		fact = "AUTHORITATIVE_CORRECTION OWNER=Mei\n"
	case 24:
		fact = "AUTHORITATIVE_CORRECTION PORT=43128\n"
	}
	query := ""
	if check {
		query = "\nCHECKPOINT_QUERY=TRUE"
	}
	logLine := fmt.Sprintf("2026-09-09 WARN scheduler resource=workspace/src/service.go queue=%d retry=2 context deadline exceeded\n", turn)
	return fmt.Sprintf(`# Product/engineering turn %04d
%sTURN_INDEX=%04d

Review scheduler fairness, cancellation, JSONL recovery and exact terminal output. 这是包含需求、代码、日志、路径和纠错信息的实际对话载荷。

~~~go
func reconcile(ctx context.Context, claims []ResourceClaim) error {
	if err := ctx.Err(); err != nil { return err }
	return scheduler.Execute(ctx, claims)
}
~~~

%s
Tool result: {"path":"session/replay-%04d.jsonl","records":%d,"tail_repaired":true}%s`,
		turn, fact, turn, strings.Repeat(logLine, 12), turn, 800+turn, query,
	)
}

func newProtocolRuntime(t *testing.T) (*appRuntime, session.Options, *protocolProbe) {
	t.Helper()
	probe := &protocolProbe{}
	server := httptest.NewServer(probe)
	t.Cleanup(server.Close)
	config := appconfig.DefaultRuntime("test", appconfig.Provider{BaseURL: server.URL + "/v1"}, "test-model")
	config.ContextWindow, config.ModelMaxOutputTokens, config.MaxOutputTokens = protocolContextWindow, 2048, 512
	config.Streaming, config.Tools, config.ParallelToolCalls = false, false, false
	var stdout, stderr bytes.Buffer
	runtime, err := buildAppRuntime(config, "key", runtimeOptions{
		Workspace: t.TempDir(), Approver: staticApprover(false), Output: newPlainModelOutput(&stdout, &stderr),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	options, err := runtime.NewSessionOptions()
	if err != nil {
		t.Fatal(err)
	}
	return runtime, options, probe
}

func TestDefaultContextBuilderUsesProtocolAndRepeatedCompression(t *testing.T) {
	runtime, options, probe := newProtocolRuntime(t)
	builder, ok := options.ContextBuilder.(*contextwindow.Builder)
	if !ok {
		t.Fatalf("default builder = %T", options.ContextBuilder)
	}
	sess, err := session.NewWithOptions("protocol", time.Now().UTC(), nil, options)
	if err != nil {
		t.Fatal(err)
	}
	wants := map[int]string{
		8:  "CHECK code=ORCHID-731 owner=Lin port=43127 backup=NEVER_DELETE",
		20: "CHECK code=ORCHID-731 owner=Mei port=43127 backup=NEVER_DELETE",
		35: "CHECK code=ORCHID-731 owner=Mei port=43128 backup=NEVER_DELETE",
	}
	started := time.Now()
	var last po.RunResult
	for turn := 0; turn < 36; turn++ {
		_, check := wants[turn]
		user, _ := po.NewUserTextMessage(fmt.Sprintf("user-%04d", turn), realisticProtocolTurn(turn, check))
		last, err = sess.Prompt(context.Background(), runtime.Agent, user)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		want := fmt.Sprintf("ACK turn=%04d", turn)
		if check {
			want = wants[turn]
		}
		if last.FinalText() != want {
			t.Fatalf("turn %d: got %q, want %q", turn, last.FinalText(), want)
		}
	}
	if probe.failure != "" || probe.main != 36 || probe.summaries < 3 {
		t.Fatalf("protocol: main=%d summaries=%d failure=%q", probe.main, probe.summaries, probe.failure)
	}
	if len(last.Messages()) != 72 || len(sess.Transcript().Messages()) != 72 || !builder.LastStats().Compacted {
		t.Fatal("full transcript or compacted context was not preserved")
	}
	t.Logf("turns=36 summaries=%d max_main_tokens=%d elapsed=%s", probe.summaries, probe.maxMain, time.Since(started))
}

func TestLargeResumedSessionChunksSummaryRequests(t *testing.T) {
	runtime, options, probe := newProtocolRuntime(t)
	messages := make([]po.Message, 0, 60)
	for turn := 0; turn < 30; turn++ {
		user, _ := po.NewUserTextMessage(fmt.Sprintf("old-user-%04d", turn), realisticProtocolTurn(turn, false))
		assistant, _ := po.NewAssistantMessage(fmt.Sprintf("old-assistant-%04d", turn), po.TextPart("ack"))
		messages = append(messages, user, assistant)
	}
	sess, err := session.ResumeWithOptions(session.State{
		ID: "resumed", CreatedAt: time.Now().UTC(), Messages: messages,
	}, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	user, _ := po.NewUserTextMessage("resumed-user", realisticProtocolTurn(31, true))
	result, err := sess.Prompt(context.Background(), runtime.Agent, user)
	if err != nil {
		t.Fatal(err)
	}
	want := "CHECK code=ORCHID-731 owner=Mei port=43128 backup=NEVER_DELETE"
	if result.FinalText() != want || probe.failure != "" || probe.summaries < 2 || probe.main != 1 {
		t.Fatalf("result=%q main=%d summaries=%d failure=%q", result.FinalText(), probe.main, probe.summaries, probe.failure)
	}
}
