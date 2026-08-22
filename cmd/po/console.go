package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"

	"golang.org/x/term"
)

type consoleState struct {
	Activity string
	Approval string
	Running  bool
}

func (s consoleState) prompt() string {
	if s.Approval != "" {
		return fmt.Sprintf("po [approve %s · y/N]> ", s.Approval)
	}
	if s.Activity != "" {
		return fmt.Sprintf("po [%s]> ", s.Activity)
	}
	return "po> "
}

type replConsole interface {
	modelOutput

	Start() error
	Close() error
	ReadInput() consoleInput
	Print(string) error
	PresentInput(consoleInputMode, string) error
	EndResponse() error
	ClearTranscript() error
	SetState(consoleState) error
	ShowPrompt() error
}

type consoleInputMode uint8

const (
	consoleInputConversation consoleInputMode = iota
	consoleInputSteering
	consoleInputFollowUp
)

type consoleInput struct {
	line string
	mode consoleInputMode
	err  error
}

func newREPLConsole(stdin io.Reader, stdout, stderr io.Writer) replConsole {
	inputFD, inputOK := descriptor(stdin)
	outputFD, outputOK := descriptor(stdout)
	if inputOK && outputOK && term.IsTerminal(inputFD) && term.IsTerminal(outputFD) {
		return newTerminalConsole(stdin, stdout, inputFD, outputFD)
	}
	return newPlainConsole(stdin, stdout, stderr)
}

type fileDescriptor interface {
	Fd() uintptr
}

func descriptor(value any) (int, bool) {
	file, ok := value.(fileDescriptor)
	if !ok {
		return 0, false
	}
	return int(file.Fd()), true
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(buffer []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(buffer)
}

type plainConsole struct {
	*plainModelOutput

	reader *bufio.Reader
	meta   *lockedWriter

	stateMu sync.Mutex
	state   consoleState
}

func newPlainConsole(stdin io.Reader, stdout, stderr io.Writer) *plainConsole {
	textWriter := &lockedWriter{writer: stdout}
	metaWriter := &lockedWriter{writer: stderr}
	return &plainConsole{
		plainModelOutput: &plainModelOutput{
			text:     newCompactStream(textWriter),
			thinking: newCompactStream(metaWriter),
		},
		reader: bufio.NewReader(stdin),
		meta:   metaWriter,
	}
}

func (c *plainConsole) Start() error { return nil }
func (c *plainConsole) Close() error { return c.FinishModelMessage() }

func (c *plainConsole) ReadInput() consoleInput {
	line, err := c.reader.ReadString('\n')
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if len(line) > 0 && err == io.EOF {
		err = nil
	}
	return consoleInput{line: line, mode: consoleInputConversation, err: err}
}

func (c *plainConsole) Print(text string) error {
	text = compactBlock(text)
	if text == "" {
		return nil
	}
	_, err := io.WriteString(c.meta, text)
	return err
}

func (c *plainConsole) PresentInput(mode consoleInputMode, _ string) error {
	var notice string
	switch mode {
	case consoleInputSteering:
		notice = "[steering submitted]\n"
	case consoleInputFollowUp:
		notice = "[follow-up queued]\n"
	default:
		return nil
	}
	_, err := io.WriteString(c.meta, notice)
	return err
}

func (*plainConsole) EndResponse() error     { return nil }
func (*plainConsole) ClearTranscript() error { return nil }

func (c *plainConsole) SetState(state consoleState) error {
	c.stateMu.Lock()
	c.state = state
	c.stateMu.Unlock()
	return nil
}

func (c *plainConsole) ShowPrompt() error {
	c.stateMu.Lock()
	prompt := c.state.prompt()
	c.stateMu.Unlock()
	_, err := io.WriteString(c.meta, prompt)
	return err
}
