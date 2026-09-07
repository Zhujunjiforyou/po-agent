package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"
)

type consoleState struct {
	Activity string
	Approval string
	Model    string
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
	SelectChoice(string, []consoleChoice) (string, bool, error)
}

// consoleChoice 是终端选择器的通用视图模型。ID 只会返回给调用方，终端层不解释
// 其业务含义。
type consoleChoice struct {
	ID          string
	Group       string
	Label       string
	Description string
	Current     bool
}

func validateConsoleChoices(title string, choices []consoleChoice) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("selector title is required")
	}
	if len(choices) == 0 {
		return fmt.Errorf("no choices are available")
	}
	seen := make(map[string]struct{}, len(choices))
	for _, choice := range choices {
		if strings.TrimSpace(choice.ID) == "" || strings.TrimSpace(choice.Label) == "" {
			return fmt.Errorf("selector choices require an ID and label")
		}
		if _, duplicate := seen[choice.ID]; duplicate {
			return fmt.Errorf("duplicate selector choice ID %q", choice.ID)
		}
		seen[choice.ID] = struct{}{}
	}
	return nil
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

func (c *plainConsole) SelectChoice(title string, choices []consoleChoice) (string, bool, error) {
	if err := validateConsoleChoices(title, choices); err != nil {
		return "", false, err
	}
	var display strings.Builder
	display.WriteString(title + ":\n")
	group := ""
	for index, choice := range choices {
		if choice.Group != group {
			group = choice.Group
			fmt.Fprintf(&display, "  [%s]\n", group)
		}
		marker := ""
		if choice.Current {
			marker = "  (current)"
		}
		fmt.Fprintf(&display, "  %2d  %s%s", index+1, choice.Label, marker)
		if choice.Description != "" {
			fmt.Fprintf(&display, "  %s", choice.Description)
		}
		display.WriteByte('\n')
	}
	display.WriteString("select a number (empty to cancel):")
	if err := c.Print(display.String()); err != nil {
		return "", false, err
	}
	input := c.ReadInput()
	if input.err != nil {
		if input.err == io.EOF {
			return "", false, nil
		}
		return "", false, input.err
	}
	value := strings.TrimSpace(input.line)
	if value == "" {
		return "", false, nil
	}
	selected, err := strconv.Atoi(value)
	if err != nil || selected < 1 || selected > len(choices) {
		return "", false, fmt.Errorf("invalid selection %q", value)
	}
	return choices[selected-1].ID, true, nil
}
