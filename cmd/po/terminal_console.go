package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	defaultTerminalWidth  = 80
	defaultTerminalHeight = 24
	terminalFooterHeight  = 6
	terminalResizePeriod  = 250 * time.Millisecond
	terminalEnterScreen   = "\x1b[?1049h\x1b[?2004h\x1b[?1000h\x1b[?1006h\x1b[2J\x1b[H"
	terminalLeaveScreen   = "\x1b[?1006l\x1b[?1000l\x1b[?2004l\x1b[?25h\x1b[?1049l"
)

var errConsoleInterrupted = errors.New("interactive console interrupted")

type terminalStreamKind uint8

const (
	terminalStreamNone terminalStreamKind = iota
	terminalStreamThinking
	terminalStreamText
)

type terminalCommand interface{ terminalCommand() }

type terminalAppendCommand struct {
	kind terminalStreamKind
	text string
}

func (terminalAppendCommand) terminalCommand() {}

type terminalPrintCommand struct{ text string }

func (terminalPrintCommand) terminalCommand() {}

type terminalStateCommand struct{ state consoleState }

func (terminalStateCommand) terminalCommand() {}

type terminalPresentInputCommand struct {
	mode consoleInputMode
	text string
}

func (terminalPresentInputCommand) terminalCommand() {}

type terminalEndResponseCommand struct{}

func (terminalEndResponseCommand) terminalCommand() {}

type terminalClearCommand struct{}

func (terminalClearCommand) terminalCommand() {}

type terminalFinishCommand struct{}

func (terminalFinishCommand) terminalCommand() {}

type terminalShowCommand struct{}

func (terminalShowCommand) terminalCommand() {}

type terminalSelectCommand struct {
	title   string
	choices []consoleChoice
	result  chan terminalSelection
}

func (terminalSelectCommand) terminalCommand() {}

type terminalQuitCommand struct{}

func (terminalQuitCommand) terminalCommand() {}

type terminalSelection struct {
	id       string
	selected bool
	err      error
}

// terminalConsole 负责设置原始模式，并通过同一个渲染循环处理输入、运行时事件和尺寸
// 检查。只有该循环能够写入终端画面。
type terminalConsole struct {
	stdin    io.Reader
	stdout   io.Writer
	inputFD  int
	outputFD int

	commands chan terminalCommand
	keys     chan terminalKey
	inputs   chan consoleInput
	done     chan struct{}

	textStream     *compactStream
	thinkingStream *compactStream

	stateMu  sync.Mutex
	started  bool
	closing  bool
	rawState *term.State
	runErr   error
}

func newTerminalConsole(stdin io.Reader, stdout io.Writer, inputFD, outputFD int) *terminalConsole {
	console := &terminalConsole{
		stdin:    stdin,
		stdout:   stdout,
		inputFD:  inputFD,
		outputFD: outputFD,
		commands: make(chan terminalCommand, 128),
		keys:     make(chan terminalKey, 32),
		inputs:   make(chan consoleInput, 16),
		done:     make(chan struct{}),
	}
	console.textStream = newCompactStream(terminalStreamWriter{console: console, kind: terminalStreamText})
	console.thinkingStream = newCompactStream(terminalStreamWriter{console: console, kind: terminalStreamThinking})
	return console
}

func (c *terminalConsole) Start() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closing {
		return fmt.Errorf("terminal console is closed")
	}
	if c.started {
		return nil
	}

	rawState, err := term.MakeRaw(c.inputFD)
	if err != nil {
		return fmt.Errorf("enable terminal raw mode: %w", err)
	}
	c.rawState = rawState
	c.started = true

	width, height := c.terminalSize()
	go c.readKeys()
	go c.run(width, height)
	return nil
}

func (c *terminalConsole) Close() error {
	c.stateMu.Lock()
	if !c.started {
		c.closing = true
		c.stateMu.Unlock()
		return nil
	}
	if c.closing {
		c.stateMu.Unlock()
		<-c.done
		return c.result()
	}
	c.closing = true
	c.stateMu.Unlock()

	finishErr := c.FinishModelMessage()
	select {
	case c.commands <- terminalQuitCommand{}:
	case <-c.done:
	}
	<-c.done
	return errors.Join(finishErr, c.result())
}

func (c *terminalConsole) ReadInput() consoleInput {
	select {
	case input := <-c.inputs:
		return input
	case <-c.done:
		if err := c.result(); err != nil {
			return consoleInput{err: err}
		}
		return consoleInput{err: io.EOF}
	}
}

func (c *terminalConsole) Print(text string) error {
	if err := c.FinishModelMessage(); err != nil {
		return err
	}
	text = compactBlock(text)
	if text == "" {
		return nil
	}
	return c.send(terminalPrintCommand{text: text})
}

func (c *terminalConsole) SetState(state consoleState) error {
	return c.send(terminalStateCommand{state: state})
}

func (c *terminalConsole) PresentInput(mode consoleInputMode, text string) error {
	return c.send(terminalPresentInputCommand{mode: mode, text: text})
}

func (c *terminalConsole) EndResponse() error {
	return c.send(terminalEndResponseCommand{})
}

func (c *terminalConsole) ClearTranscript() error {
	return c.send(terminalClearCommand{})
}

func (c *terminalConsole) ShowPrompt() error { return c.send(terminalShowCommand{}) }

func (c *terminalConsole) SelectChoice(title string, choices []consoleChoice) (string, bool, error) {
	if err := validateConsoleChoices(title, choices); err != nil {
		return "", false, err
	}
	result := make(chan terminalSelection, 1)
	if err := c.send(terminalSelectCommand{
		title:   title,
		choices: append([]consoleChoice(nil), choices...),
		result:  result,
	}); err != nil {
		return "", false, err
	}
	select {
	case selection := <-result:
		return selection.id, selection.selected, selection.err
	case <-c.done:
		if err := c.result(); err != nil {
			return "", false, err
		}
		return "", false, io.EOF
	}
}

func (c *terminalConsole) WriteModelText(text string) error {
	return c.textStream.WriteString(text)
}

func (c *terminalConsole) WriteThinking(text string) error {
	return c.thinkingStream.WriteString(text)
}

func (c *terminalConsole) FinishModelMessage() error {
	if err := c.thinkingStream.FinishMessage(); err != nil {
		return err
	}
	if err := c.textStream.FinishMessage(); err != nil {
		return err
	}
	return c.send(terminalFinishCommand{})
}

func (c *terminalConsole) send(command terminalCommand) error {
	c.stateMu.Lock()
	started := c.started
	c.stateMu.Unlock()
	if !started {
		return fmt.Errorf("terminal console is not started")
	}
	select {
	case c.commands <- command:
		return nil
	case <-c.done:
		if err := c.result(); err != nil {
			return err
		}
		return io.ErrClosedPipe
	}
}

func (c *terminalConsole) result() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.runErr
}

func (c *terminalConsole) terminalSize() (int, int) {
	width, height, err := term.GetSize(c.outputFD)
	if err != nil || width <= 0 || height <= 0 {
		return defaultTerminalWidth, defaultTerminalHeight
	}
	return width, height
}

func (c *terminalConsole) run(width, height int) {
	ui := newTerminalUI(width, height)
	renderer := terminalRenderer{writer: c.stdout}
	ticker := time.NewTicker(terminalResizePeriod)
	defer ticker.Stop()
	var selectionResult chan terminalSelection

	runErr := writeTerminal(c.stdout, terminalEnterScreen)
	if runErr == nil {
		runErr = renderer.render(ui)
	}

	for runErr == nil {
		shouldRender := true
		select {
		case command := <-c.commands:
			switch current := command.(type) {
			case terminalAppendCommand:
				ui.appendStream(current.kind, current.text)
			case terminalPrintCommand:
				ui.appendNotice(current.text)
			case terminalStateCommand:
				ui.setState(current.state)
			case terminalPresentInputCommand:
				ui.presentInput(current.mode, current.text)
			case terminalEndResponseCommand:
				ui.endResponse()
			case terminalClearCommand:
				ui.clearTranscript()
			case terminalFinishCommand:
				ui.finishStream()
			case terminalShowCommand:
			case terminalSelectCommand:
				if selectionResult != nil {
					selectionResult <- terminalSelection{err: fmt.Errorf("selector is already open")}
				}
				selectionResult = current.result
				ui.openSelector(current.title, current.choices)
			case terminalQuitCommand:
				if selectionResult != nil {
					selectionResult <- terminalSelection{err: io.EOF}
					selectionResult = nil
				}
				runErr = renderer.render(ui)
				goto finished
			}

		case key := <-c.keys:
			action := ui.handleKey(key)
			if action.selectionDone && selectionResult != nil {
				selection := terminalSelection{err: action.err}
				if action.err == nil && action.selectionID != "" {
					selection.id = action.selectionID
					selection.selected = true
				}
				selectionResult <- selection
				selectionResult = nil
				ui.closeSelector()
			}
			if action.submit {
				select {
				case c.inputs <- consoleInput{line: action.line, mode: action.mode, err: action.err}:
				case <-c.done:
				}
			}

		case <-ticker.C:
			newWidth, newHeight := c.terminalSize()
			shouldRender = ui.resize(newWidth, newHeight)
		}
		if shouldRender {
			runErr = renderer.render(ui)
		}
	}

finished:
	exitErr := writeTerminal(c.stdout, terminalLeaveScreen)
	c.stateMu.Lock()
	restoreErr := term.Restore(c.inputFD, c.rawState)
	c.rawState = nil
	c.runErr = errors.Join(runErr, exitErr, restoreErr)
	c.stateMu.Unlock()
	close(c.done)
}

type terminalRenderer struct {
	writer   io.Writer
	previous []string
}

// render 只更新发生变化的行。因此除非输入内容或光标位置改变，流式响应不会触碰输入行。
func (r *terminalRenderer) render(ui *terminalUI) error {
	view, cursorRow, cursorColumn := ui.view()
	var output strings.Builder
	output.WriteString("\x1b[?25l")
	for index, line := range view {
		if index < len(r.previous) && line == r.previous[index] {
			continue
		}
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[2K", index+1)
		output.WriteString(line)
	}
	fmt.Fprintf(&output, "\x1b[%d;%dH\x1b[?25h", cursorRow, cursorColumn)
	if err := writeTerminal(r.writer, output.String()); err != nil {
		return err
	}
	r.previous = append(r.previous[:0], view...)
	return nil
}

func (c *terminalConsole) readKeys() {
	reader := bufio.NewReader(c.stdin)
	for {
		key, err := readTerminalKey(reader)
		if err != nil {
			select {
			case c.keys <- terminalKey{kind: terminalKeyReadError, err: err}:
			case <-c.done:
			}
			return
		}
		select {
		case c.keys <- key:
		case <-c.done:
			return
		}
	}
}

type terminalStreamWriter struct {
	console *terminalConsole
	kind    terminalStreamKind
}

func (w terminalStreamWriter) Write(data []byte) (int, error) {
	if err := w.console.send(terminalAppendCommand{kind: w.kind, text: string(data)}); err != nil {
		return 0, err
	}
	return len(data), nil
}

func writeTerminal(writer io.Writer, text string) error {
	_, err := io.WriteString(writer, text)
	return err
}
