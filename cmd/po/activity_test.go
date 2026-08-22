package main

import (
	"context"
	"io"
	"strings"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

type recordingConsole struct {
	state     consoleState
	finished  int
	modelText strings.Builder
}

func (*recordingConsole) Start() error                                { return nil }
func (*recordingConsole) Close() error                                { return nil }
func (*recordingConsole) ReadInput() consoleInput                     { return consoleInput{err: io.EOF} }
func (*recordingConsole) Print(string) error                          { return nil }
func (*recordingConsole) ShowPrompt() error                           { return nil }
func (*recordingConsole) PresentInput(consoleInputMode, string) error { return nil }
func (*recordingConsole) EndResponse() error                          { return nil }
func (*recordingConsole) ClearTranscript() error                      { return nil }
func (c *recordingConsole) WriteModelText(text string) error {
	c.modelText.WriteString(text)
	return nil
}
func (*recordingConsole) WriteThinking(string) error          { return nil }
func (c *recordingConsole) SetState(state consoleState) error { c.state = state; return nil }
func (c *recordingConsole) FinishModelMessage() error         { c.finished++; return nil }

func TestActivityReporterShowsConcreteToolProgress(t *testing.T) {
	console := &recordingConsole{}
	prompt := newPromptController(console)
	reporter := newActivityReporter(prompt, console)
	progress := 0.5

	reporter.Observe(context.Background(), po.RunStartEvent{RunID: "run-1"})
	if !console.state.Running {
		t.Fatal("run start did not mark the console as running")
	}
	reporter.Observe(context.Background(), po.TurnStartEvent{RunID: "run-1", TurnID: "turn-1", Turn: 2})
	reporter.Observe(context.Background(), po.ToolStartEvent{
		RunID:      "run-1",
		TurnID:     "turn-1",
		ToolCallID: "call-1",
		ToolName:   "read",
	})
	reporter.Observe(context.Background(), po.ToolUpdateEvent{
		RunID:      "run-1",
		TurnID:     "turn-1",
		ToolCallID: "call-1",
		ToolName:   "read",
		Update:     po.NewProgressToolUpdate("reading README.md", progress),
	})

	if !strings.Contains(console.state.Activity, "tool read · reading README.md · 50%") {
		t.Fatalf("activity = %q", console.state.Activity)
	}

	reporter.Observe(context.Background(), po.ToolEndEvent{ToolCallID: "call-1", ToolName: "read"})
	if !strings.Contains(console.state.Activity, "tool read done") {
		t.Fatalf("activity = %q", console.state.Activity)
	}
	reporter.Observe(context.Background(), po.RunEndEvent{RunID: "run-1"})
	if console.state != (consoleState{}) {
		t.Fatalf("state = %#v, want idle state", console.state)
	}
	if console.finished == 0 {
		t.Fatal("model output was not finalized at an event boundary")
	}
}

func TestActivityReporterKeepsParallelToolOrderStable(t *testing.T) {
	console := &recordingConsole{}
	reporter := newActivityReporter(newPromptController(console), console)

	reporter.Observe(context.Background(), po.ToolStartEvent{ToolCallID: "call-b", ToolName: "grep"})
	reporter.Observe(context.Background(), po.ToolStartEvent{ToolCallID: "call-a", ToolName: "read"})
	if !strings.Contains(console.state.Activity, "2 tools · grep, read") {
		t.Fatalf("activity = %q", console.state.Activity)
	}
}

func TestActivityReporterKeepsToolNameWhileArgumentsStream(t *testing.T) {
	console := &recordingConsole{}
	reporter := newActivityReporter(newPromptController(console), console)

	reporter.Observe(context.Background(), po.RunStartEvent{RunID: "run-1"})
	reporter.Observe(context.Background(), po.MessageUpdateEvent{Delta: po.ModelDelta{
		Kind:     po.ModelDeltaToolCallStart,
		ToolName: "grep",
	}})
	reporter.Observe(context.Background(), po.MessageUpdateEvent{Delta: po.ModelDelta{
		Kind: po.ModelDeltaToolCallArguments,
	}})

	if !strings.Contains(console.state.Activity, "preparing tool grep") {
		t.Fatalf("activity = %q", console.state.Activity)
	}
}

func TestActivityReporterPrintsNonStreamingMessageText(t *testing.T) {
	console := &recordingConsole{}
	reporter := newActivityReporter(newPromptController(console), console)
	message, err := po.NewAssistantMessage("assistant-1", po.TextPart("fallback response"))
	if err != nil {
		t.Fatal(err)
	}

	reporter.Observe(context.Background(), po.MessageStartEvent{RunID: "run-1", TurnID: "turn-1"})
	reporter.Observe(context.Background(), po.MessageEndEvent{RunID: "run-1", TurnID: "turn-1", Message: message})

	if got := console.modelText.String(); got != "fallback response" {
		t.Fatalf("model text = %q", got)
	}
}

func TestActivityReporterDoesNotRepeatStreamedText(t *testing.T) {
	console := &recordingConsole{}
	reporter := newActivityReporter(newPromptController(console), console)
	message, err := po.NewAssistantMessage("assistant-1", po.TextPart("streamed response"))
	if err != nil {
		t.Fatal(err)
	}

	reporter.Observe(context.Background(), po.MessageStartEvent{RunID: "run-1", TurnID: "turn-1"})
	if err := console.WriteModelText("streamed response"); err != nil {
		t.Fatal(err)
	}
	reporter.Observe(context.Background(), po.MessageUpdateEvent{Delta: po.ModelDelta{
		Kind: po.ModelDeltaText,
		Text: "streamed response",
	}})
	reporter.Observe(context.Background(), po.MessageEndEvent{RunID: "run-1", TurnID: "turn-1", Message: message})

	if got := console.modelText.String(); got != "streamed response" {
		t.Fatalf("model text = %q", got)
	}
}
