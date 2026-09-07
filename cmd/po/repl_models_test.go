package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/internal/modelcatalog"
	"github.com/lemonzjj/po-agent-go/session"
	sessionjsonl "github.com/lemonzjj/po-agent-go/session/jsonl"
)

func TestREPLModelSelectionChangesNextRequestAndKeepsTranscript(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/models":
			_, _ = fmt.Fprint(w, `{"data":[{"id":"model-a"}]}`)
		case "/chat/completions":
			t.Fatal("request unexpectedly used the old provider")
		default:
			http.NotFound(w, request)
		}
	}))
	defer first.Close()

	var requestedModel string
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/models":
			_, _ = fmt.Fprint(w, `{"data":[{"id":"model-b"}]}`)
		case "/chat/completions":
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			requestedModel = body.Model
			w.Header().Set("Content-Type", "text/event-stream")
			writeSSEData(w, `{"id":"selected","choices":[{"index":0,"delta":{"content":"from model b"},"finish_reason":null}]}`)
			writeSSEData(w, `{"id":"selected","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			writeSSEData(w, `[DONE]`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer second.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	file := appconfig.New("first", first.URL, "model-a")
	file.Providers["second"] = appconfig.Provider{BaseURL: second.URL}
	if err := appconfig.Write(configPath, file, false); err != nil {
		t.Fatal(err)
	}
	document, err := appconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	current, err := document.Resolve(appconfig.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(appconfig.DefaultAPIKeyEnv, "test-key")

	console := newScriptedREPLConsole("second", "model-b")
	approver := newInteractiveApprover()
	runtime, err := buildAppRuntime(current, "test-key", runtimeOptions{
		Workspace: t.TempDir(),
		Approver:  approver,
		Output:    console,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	now := time.Now().UTC()
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	journal, err := sessionjsonl.Create(sessionPath, "model-switch-session", now)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.New("model-switch-session", now, journal)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	opened := &openedSession{Session: sess, Journal: journal, Path: sessionPath}
	defer opened.Close()

	manager := newREPLModelManager(configPath, current, runtime, nil)
	manager.client = modelcatalog.Client{HTTP: second.Client()}
	if code := runREPL(context.Background(), runtime, manager, opened, approver, console); code != 0 {
		t.Fatalf("runREPL code = %d, notices = %#v", code, console.notices)
	}
	if requestedModel != "model-b" {
		t.Fatalf("request model = %q", requestedModel)
	}
	if output := console.modelText(); !strings.Contains(output, "from model b") {
		t.Fatalf("model output = %q", output)
	}
	messages := opened.Session.Transcript().Messages()
	if len(messages) != 2 {
		t.Fatalf("transcript = %#v", messages)
	}
	user, userOK := messages[0].(po.UserMessage)
	assistant, assistantOK := messages[1].(po.AssistantMessage)
	if !userOK || !assistantOK ||
		user.Parts()[0].Text != "hello after switch" || assistant.Text() != "from model b" {
		t.Fatalf("transcript = %#v", messages)
	}
	updated, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Current != (appconfig.Selection{Provider: "second", Model: "model-b"}) {
		t.Fatalf("persisted selection = %#v", updated.Current)
	}
}

type scriptedREPLConsole struct {
	provider string
	model    string

	mu          sync.Mutex
	input       int
	text        strings.Builder
	notices     []string
	responseEnd chan struct{}
}

func newScriptedREPLConsole(provider, model string) *scriptedREPLConsole {
	return &scriptedREPLConsole{
		provider:    provider,
		model:       model,
		responseEnd: make(chan struct{}, 1),
	}
}

func (*scriptedREPLConsole) Start() error { return nil }
func (*scriptedREPLConsole) Close() error { return nil }

func (c *scriptedREPLConsole) ReadInput() consoleInput {
	c.mu.Lock()
	index := c.input
	c.input++
	c.mu.Unlock()
	switch index {
	case 0:
		return consoleInput{line: "/models", mode: consoleInputConversation}
	case 1:
		return consoleInput{line: "hello after switch", mode: consoleInputConversation}
	default:
		<-c.responseEnd
		return consoleInput{line: "/quit", mode: consoleInputConversation}
	}
}

func (c *scriptedREPLConsole) Print(text string) error {
	c.mu.Lock()
	c.notices = append(c.notices, text)
	c.mu.Unlock()
	return nil
}

func (*scriptedREPLConsole) PresentInput(consoleInputMode, string) error { return nil }

func (c *scriptedREPLConsole) EndResponse() error {
	select {
	case c.responseEnd <- struct{}{}:
	default:
	}
	return nil
}

func (*scriptedREPLConsole) ClearTranscript() error      { return nil }
func (*scriptedREPLConsole) SetState(consoleState) error { return nil }
func (*scriptedREPLConsole) ShowPrompt() error           { return nil }

func (c *scriptedREPLConsole) SelectChoice(_ string, choices []consoleChoice) (string, bool, error) {
	for _, choice := range choices {
		if choice.Group == c.provider && choice.Label == c.model {
			return choice.ID, true, nil
		}
	}
	return "", false, fmt.Errorf("choice %s:%s not found", c.provider, c.model)
}

func (c *scriptedREPLConsole) WriteModelText(text string) error {
	c.mu.Lock()
	c.text.WriteString(text)
	c.mu.Unlock()
	return nil
}

func (*scriptedREPLConsole) WriteThinking(string) error { return nil }
func (*scriptedREPLConsole) FinishModelMessage() error  { return nil }

func (c *scriptedREPLConsole) modelText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text.String()
}
