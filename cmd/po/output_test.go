package main

import (
	"bytes"
	"context"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
)

func TestCompactStreamCollapsesBlankLinesAcrossChunks(t *testing.T) {
	var output bytes.Buffer
	stream := newCompactStream(&output)

	for _, chunk := range []string{"\n\nhello\n", "\n\n", "\nworld"} {
		if err := stream.WriteString(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.FinishMessage(); err != nil {
		t.Fatal(err)
	}

	if got, want := output.String(), "hello\n\nworld\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestCompactStreamResetsLeadingWhitespaceForEachMessage(t *testing.T) {
	var output bytes.Buffer
	stream := newCompactStream(&output)

	if err := stream.WriteString("first"); err != nil {
		t.Fatal(err)
	}
	if err := stream.FinishMessage(); err != nil {
		t.Fatal(err)
	}
	if err := stream.WriteString("\n\nsecond"); err != nil {
		t.Fatal(err)
	}
	if err := stream.FinishMessage(); err != nil {
		t.Fatal(err)
	}

	if got, want := output.String(), "first\nsecond\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunPrintFallsBackToFinalTextWithoutDeltas(t *testing.T) {
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.Reply("assistant-1", "fallback response", po.Usage{})),
	)
	agent, err := po.NewAgent(po.AgentConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	output := newPlainModelOutput(&stdout, &stderr)

	if code := runPrint(context.Background(), agent, output, "hello", &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "fallback response\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
