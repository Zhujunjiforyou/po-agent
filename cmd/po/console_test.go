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

func TestTerminalUISuggestsAndCompletesSlashCommands(t *testing.T) {
	ui := newTerminalUI(72, 18)
	ui.handleKey(terminalKey{kind: terminalKeyRunes, text: "/"})
	view, cursorRow, _ := ui.view()
	rendered := strings.Join(view, "\n")
	for _, fragment := range []string{"Commands", "/session", "/models", "/queue MESSAGE", "Tab complete"} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("slash suggestions missing %q: %q", fragment, rendered)
		}
	}
	if cursorRow != 16 {
		t.Fatalf("cursor row = %d, want fixed input row 16", cursorRow)
	}

	ui.handleKey(terminalKey{kind: terminalKeyRunes, text: "mo"})
	filtered, _, _ := ui.view()
	filteredText := strings.Join(filtered, "\n")
	if !strings.Contains(filteredText, "/models") || strings.Contains(filteredText, "/session") {
		t.Fatalf("filtered slash suggestions = %q", filteredText)
	}
	ui.handleKey(terminalKey{kind: terminalKeyToggleMode})
	if got := ui.editor.Value(); got != "/models" {
		t.Fatalf("completed command = %q, want /models", got)
	}
}

func TestREPLCommandCatalogDrivesHelpAndExactMatches(t *testing.T) {
	help := replHelp()
	seen := make(map[string]struct{}, len(replCommands))
	for _, command := range replCommands {
		if _, duplicate := seen[command.name]; duplicate {
			t.Fatalf("duplicate public command %q", command.name)
		}
		seen[command.name] = struct{}{}
		if !strings.Contains(help, command.usage) || !strings.Contains(help, command.description) {
			t.Fatalf("help does not contain command %#v: %q", command, help)
		}
		matches := matchingREPLCommands(command.name)
		if len(matches) != 1 || matches[0].name != command.name {
			t.Fatalf("exact matches for %q = %#v", command.name, matches)
		}
	}
}

func TestTerminalUISlashSuggestionsDoNotTakeHistoryArrows(t *testing.T) {
	ui := newTerminalUI(64, 16)
	ui.editor.SetValue("earlier input")
	ui.handleKey(terminalKey{kind: terminalKeyEnter})
	ui.editor.SetValue("/")

	ui.handleKey(terminalKey{kind: terminalKeyUp})
	if got := ui.editor.Value(); got != "earlier input" {
		t.Fatalf("up arrow selected %q, want input history", got)
	}
	ui.handleKey(terminalKey{kind: terminalKeyDown})
	if got := ui.editor.Value(); got != "/" {
		t.Fatalf("down arrow restored %q, want slash draft", got)
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

func TestTerminalUIWheelScrollsTranscriptWithoutChangingInput(t *testing.T) {
	ui := newTerminalUI(40, 12)
	for index := range 12 {
		ui.presentInput(consoleInputConversation, fmt.Sprintf("message %d", index))
	}
	ui.editor.SetValue("draft")

	ui.handleKey(terminalKey{kind: terminalKeyScrollUp})
	if ui.scroll == 0 {
		t.Fatal("wheel up did not scroll the transcript")
	}
	if got := ui.editor.Value(); got != "draft" {
		t.Fatalf("wheel changed input to %q", got)
	}
	ui.handleKey(terminalKey{kind: terminalKeyScrollDown})
	if ui.scroll != 0 {
		t.Fatalf("wheel down left scroll at %d, want bottom", ui.scroll)
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

func TestReadTerminalKeyDecodesMouseWheelProtocols(t *testing.T) {
	sgr := bufio.NewReader(strings.NewReader("\x1b[<64;20;8M\x1b[<69;20;8M"))
	for index, expected := range []terminalKeyKind{terminalKeyScrollUp, terminalKeyScrollDown} {
		key, err := readTerminalKey(sgr)
		if err != nil {
			t.Fatal(err)
		}
		if key.kind != expected {
			t.Fatalf("SGR wheel key %d = %v, want %v", index, key.kind, expected)
		}
	}

	x10Bytes := []byte("\x1b[M")
	x10Bytes = append(x10Bytes, byte(64+32), byte(10+32), byte(5+32))
	x10Bytes = append(x10Bytes, []byte("\x1b[M")...)
	x10Bytes = append(x10Bytes, byte(65+32), byte(10+32), byte(5+32))
	x10 := bufio.NewReader(bytes.NewReader(x10Bytes))
	for index, expected := range []terminalKeyKind{terminalKeyScrollUp, terminalKeyScrollDown} {
		key, err := readTerminalKey(x10)
		if err != nil {
			t.Fatal(err)
		}
		if key.kind != expected {
			t.Fatalf("X10 wheel key %d = %v, want %v", index, key.kind, expected)
		}
	}
}

func TestTerminalScreenModeEnablesAndRestoresMouseTracking(t *testing.T) {
	for _, sequence := range []string{"\x1b[?1000h", "\x1b[?1006h"} {
		if !strings.Contains(terminalEnterScreen, sequence) {
			t.Fatalf("terminal enter sequence missing %q", sequence)
		}
	}
	for _, sequence := range []string{"\x1b[?1006l", "\x1b[?1000l"} {
		if !strings.Contains(terminalLeaveScreen, sequence) {
			t.Fatalf("terminal leave sequence missing %q", sequence)
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

func TestTerminalSelectorGroupsFiltersAndSelects(t *testing.T) {
	ui := newTerminalUI(64, 16)
	ui.openSelector("Select model", []consoleChoice{
		{ID: "kimi", Group: "hub", Label: "aliyun/kimi-k3", Current: true},
		{ID: "deepseek", Group: "hub", Label: "aliyun/deepseek-v4-pro"},
		{ID: "local", Group: "local", Label: "local-model"},
	})

	view, cursorRow, _ := ui.view()
	rendered := strings.Join(view, "\n")
	for _, fragment := range []string{
		"Select model", "hub", "local", "aliyun/kimi-k3", "local-model", "current", "wheel",
	} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("selector missing %q: %q", fragment, rendered)
		}
	}
	if cursorRow != 2 {
		t.Fatalf("selector cursor row = %d, want 2", cursorRow)
	}

	ui.handleKey(terminalKey{kind: terminalKeyRunes, text: "deepseek"})
	filtered, _, _ := ui.view()
	filteredText := strings.Join(filtered, "\n")
	if !strings.Contains(filteredText, "aliyun/deepseek-v4-pro") || strings.Contains(filteredText, "kimi-k3") {
		t.Fatalf("filtered selector = %q", filteredText)
	}
	selected := ui.handleKey(terminalKey{kind: terminalKeyEnter})
	if !selected.selectionDone || selected.selectionID != "deepseek" {
		t.Fatalf("selection = %#v", selected)
	}
}

func TestTerminalSelectorUsesWheelButIgnoresPageKeys(t *testing.T) {
	ui := newTerminalUI(64, 16)
	ui.openSelector("Select model", []consoleChoice{
		{ID: "a", Group: "hub", Label: "model-a"},
		{ID: "b", Group: "hub", Label: "model-b"},
		{ID: "c", Group: "hub", Label: "model-c"},
		{ID: "d", Group: "hub", Label: "model-d"},
	})

	ui.handleKey(terminalKey{kind: terminalKeyPageDown})
	unchanged := ui.handleKey(terminalKey{kind: terminalKeyEnter})
	if unchanged.selectionID != "a" {
		t.Fatalf("Page Down changed selection to %q", unchanged.selectionID)
	}

	ui.handleKey(terminalKey{kind: terminalKeyScrollDown})
	scrolled := ui.handleKey(terminalKey{kind: terminalKeyEnter})
	if scrolled.selectionID != "d" {
		t.Fatalf("wheel selected %q, want d", scrolled.selectionID)
	}
}

func TestPlainConsoleSelectorUsesNumberedFallback(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	console := newPlainConsole(strings.NewReader("2\n"), &stdout, &stderr)
	id, selected, err := console.SelectChoice("Select model", []consoleChoice{
		{ID: "a:model-a", Group: "a", Label: "model-a"},
		{ID: "b:model-b", Group: "b", Label: "model-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !selected || id != "b:model-b" {
		t.Fatalf("selection = %q, selected=%t", id, selected)
	}
	if output := stderr.String(); !strings.Contains(output, "[a]") || !strings.Contains(output, "[b]") {
		t.Fatalf("plain selector output = %q", output)
	}
}

func TestConsoleSelectorRejectsDuplicateIDs(t *testing.T) {
	err := validateConsoleChoices("Choose", []consoleChoice{
		{ID: "same", Label: "first"},
		{ID: "same", Label: "second"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v, want duplicate ID rejection", err)
	}
}

func TestREPLExposesOnlyModelsCommand(t *testing.T) {
	if !isModelCommand("/models") || !isModelCommand("/models unexpected") {
		t.Fatal("/models was not recognized as the model command")
	}
	for _, command := range []string{"/model", "/model anything"} {
		if isModelCommand(command) {
			t.Fatalf("removed alias %q is still recognized", command)
		}
	}
	if _, _, err := chooseREPLModel(nil, "/models direct-model", nil, nil); err == nil {
		t.Fatal("/models unexpectedly accepted a direct model argument")
	}
}
