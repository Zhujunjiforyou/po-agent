package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestLineEditorUsesDisplayWidthForChineseCursorMovement(t *testing.T) {
	var editor lineEditor
	editor.SetValue("来 点场")
	_, endColumn := editor.Visible(20)

	editor.Left()
	_, firstColumn := editor.Visible(20)
	editor.Left()
	_, secondColumn := editor.Visible(20)

	if endColumn-firstColumn != 2 || firstColumn-secondColumn != 2 {
		t.Fatalf("cursor columns = %d, %d, %d; each Chinese character should occupy two cells", endColumn, firstColumn, secondColumn)
	}
	editor.Insert("对")
	if got, want := editor.Value(), "来 对点场"; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

func TestTerminalUIKeepsInputStableWhileOutputChanges(t *testing.T) {
	ui := newTerminalUI(48, 14)
	ui.editor.SetValue("来 点场对话？")
	ui.editor.Left()
	position := ui.editor.cursor

	ui.state = consoleState{Activity: "tool read · README.md"}
	ui.appendStream(terminalStreamText, "正在读取项目文件。")
	view, cursorRow, cursorColumn := ui.view()

	if got := ui.editor.Value(); got != "来 点场对话？" || ui.editor.cursor != position {
		t.Fatalf("output update changed input: value=%q cursor=%d", got, ui.editor.cursor)
	}
	if len(view) != 14 {
		t.Fatalf("view height = %d, want 14", len(view))
	}
	if cursorRow != 12 || cursorColumn <= 1 {
		t.Fatalf("cursor = (%d, %d), want fixed input row with a valid column", cursorRow, cursorColumn)
	}
	rendered := strings.Join(view, "\n")
	for _, fragment := range []string{"正在读取项目文件", "tool read", "来 点场对话？", "╭", "╰"} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("view missing %q: %q", fragment, rendered)
		}
	}
}

func TestTerminalRendererOnlyRepaintsChangedRows(t *testing.T) {
	ui := newTerminalUI(48, 14)
	var output bytes.Buffer
	renderer := terminalRenderer{writer: &output}
	if err := renderer.render(ui); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	ui.editor.Insert("来")
	if err := renderer.render(ui); err != nil {
		t.Fatal(err)
	}
	rendered := output.String()
	if !strings.Contains(rendered, "\x1b[12;1H") {
		t.Fatalf("input row was not repainted: %q", rendered)
	}
	if strings.Contains(rendered, "\x1b[1;1H") {
		t.Fatalf("unchanged transcript row was repainted: %q", rendered)
	}
}

func TestTerminalUIHistoryRestoresDraft(t *testing.T) {
	ui := newTerminalUI(48, 14)
	ui.editor.SetValue("first")
	ui.handleKey(terminalKey{kind: terminalKeyEnter})
	ui.editor.SetValue("second")
	ui.handleKey(terminalKey{kind: terminalKeyEnter})
	ui.editor.SetValue("draft")

	ui.handleKey(terminalKey{kind: terminalKeyUp})
	if got := ui.editor.Value(); got != "second" {
		t.Fatalf("previous history = %q, want second", got)
	}
	ui.handleKey(terminalKey{kind: terminalKeyUp})
	if got := ui.editor.Value(); got != "first" {
		t.Fatalf("previous history = %q, want first", got)
	}
	ui.handleKey(terminalKey{kind: terminalKeyUp})
	if got := ui.editor.Value(); got != "first" {
		t.Fatalf("history should remain at oldest entry, got %q", got)
	}
	ui.handleKey(terminalKey{kind: terminalKeyDown})
	ui.handleKey(terminalKey{kind: terminalKeyDown})
	if got := ui.editor.Value(); got != "draft" {
		t.Fatalf("restored draft = %q, want draft", got)
	}
}

func TestTerminalUISubmitsRunningInputInSelectedMode(t *testing.T) {
	ui := newTerminalUI(64, 16)
	ui.setState(consoleState{Running: true, Activity: "turn 1 · responding"})
	ui.editor.SetValue("change direction")

	steering := ui.handleKey(terminalKey{kind: terminalKeyEnter})
	if steering.mode != consoleInputSteering || steering.line != "change direction" {
		t.Fatalf("steering input = %#v", steering)
	}
	if len(ui.entries) != 0 {
		t.Fatalf("running input leaked into transcript: %#v", ui.entries)
	}

	ui.handleKey(terminalKey{kind: terminalKeyToggleMode})
	ui.editor.SetValue("do this afterwards")
	followUp := ui.handleKey(terminalKey{kind: terminalKeyEnter})
	if followUp.mode != consoleInputFollowUp || followUp.line != "do this afterwards" {
		t.Fatalf("follow-up input = %#v", followUp)
	}
}

func TestTerminalUIKeepsRunOutputUnderOnePoHeading(t *testing.T) {
	ui := newTerminalUI(64, 18)
	ui.presentInput(consoleInputConversation, "start")
	ui.appendStream(terminalStreamText, "first turn")
	ui.finishStream()
	ui.presentInput(consoleInputSteering, "change direction")
	ui.appendStream(terminalStreamText, "second turn")

	transcript := strings.Join(ui.transcriptLines(), "\n")
	if count := strings.Count(transcript, terminalCyan("Po")); count != 1 {
		t.Fatalf("Po headings = %d, want 1: %q", count, transcript)
	}
	if strings.Contains(transcript, "change direction") {
		t.Fatalf("steering input should stay in the fixed control line: %q", transcript)
	}
	contextLine := ui.contextLine()
	if !strings.Contains(contextLine, "Steering submitted") ||
		!strings.Contains(contextLine, "change direction") {
		t.Fatalf("control line = %q", contextLine)
	}
}

func TestTerminalUIProvidesInputPromptsForEachMode(t *testing.T) {
	ui := newTerminalUI(64, 16)
	if width := runewidth.StringWidth(ui.helpText()); width > 80 {
		t.Fatalf("idle help width = %d, want <= 80", width)
	}
	_, idle, _, _ := ui.inputBox()
	if !strings.Contains(idle, "Ask Po anything") {
		t.Fatalf("idle input = %q", idle)
	}

	ui.setState(consoleState{Running: true})
	_, steering, _, _ := ui.inputBox()
	if !strings.Contains(steering, "Steer") || !strings.Contains(steering, "Change direction") {
		t.Fatalf("steering input = %q", steering)
	}

	ui.toggleInputMode()
	_, followUp, _, _ := ui.inputBox()
	if !strings.Contains(followUp, "Queue") || !strings.Contains(followUp, "Add a follow-up") {
		t.Fatalf("follow-up input = %q", followUp)
	}
}

func TestTerminalUIPageNavigationMovesThroughTranscript(t *testing.T) {
	ui := newTerminalUI(40, 12)
	for index := range 12 {
		ui.presentInput(consoleInputConversation, fmt.Sprintf("message %d", index))
	}
	if ui.scroll != 0 {
		t.Fatalf("initial scroll = %d, want bottom", ui.scroll)
	}

	ui.handleKey(terminalKey{kind: terminalKeyPageUp})
	if ui.scroll == 0 {
		t.Fatal("page up did not move through transcript")
	}
	for ui.scroll > 0 {
		ui.handleKey(terminalKey{kind: terminalKeyPageDown})
	}
	if ui.unseenOutput {
		t.Fatal("returning to the bottom should clear the unseen-output marker")
	}
}

func TestTerminalTranscriptKeepsOneGapBetweenEntries(t *testing.T) {
	ui := newTerminalUI(48, 14)
	ui.appendStream(terminalStreamText, "first\n")
	ui.finishStream()
	ui.appendNotice("second")
	lines := ui.transcriptLines()

	for index := 1; index < len(lines); index++ {
		if lines[index] == "" && lines[index-1] == "" {
			t.Fatalf("transcript contains consecutive blank lines: %#v", lines)
		}
	}
}

func TestReadTerminalKeyDecodesUTF8AndNavigation(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("来\x1b[D"))
	text, err := readTerminalKey(reader)
	if err != nil {
		t.Fatal(err)
	}
	left, err := readTerminalKey(reader)
	if err != nil {
		t.Fatal(err)
	}
	if text.kind != terminalKeyRunes || text.text != "来" || left.kind != terminalKeyLeft {
		t.Fatalf("decoded keys = %#v, %#v", text, left)
	}
}

func TestReadTerminalKeySupportsAlternatePageNavigation(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\x1b[1;2A\x1b[1;5B\t\x0c"))
	want := []terminalKeyKind{terminalKeyPageUp, terminalKeyPageDown, terminalKeyToggleMode, terminalKeyClear}
	for index, expected := range want {
		key, err := readTerminalKey(reader)
		if err != nil {
			t.Fatal(err)
		}
		if key.kind != expected {
			t.Fatalf("key %d = %v, want %v", index, key.kind, expected)
		}
	}
}

func TestWrapDisplayNeverExceedsTerminalWidth(t *testing.T) {
	for _, line := range wrapDisplay("中文和 English words should wrap cleanly", 10) {
		if width := runewidth.StringWidth(line); width > 10 {
			t.Fatalf("line width = %d, want <= 10: %q", width, line)
		}
	}
}

func TestTerminalOutputRemovesControlCharacters(t *testing.T) {
	if got, want := sanitizeTerminalText("safe\x1b[2J\x00text"), "safe[2Jtext"; got != want {
		t.Fatalf("sanitized = %q, want %q", got, want)
	}
}
