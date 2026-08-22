package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/mattn/go-runewidth"
)

type terminalEntryKind uint8

const (
	terminalEntryNotice terminalEntryKind = iota
	terminalEntryUser
	terminalEntryAssistant
	terminalEntryThinking
)

type terminalEntry struct {
	kind         terminalEntryKind
	text         string
	continuation bool
}

type terminalControlInput struct {
	mode consoleInputMode
	text string
}

// terminalUI 只保存显示状态；终端输入输出仍由 terminalConsole 负责，确保模型输出不能
// 修改正在使用的行编辑器。
type terminalUI struct {
	width  int
	height int
	state  consoleState
	mode   consoleInputMode

	editor       lineEditor
	history      []string
	historyIndex int
	historyDraft string

	entries       []terminalEntry
	activeStream  terminalStreamKind
	responseOpen  bool
	controlInputs []terminalControlInput
	scroll        int
	unseenOutput  bool

	transcriptDirty bool
	cachedWidth     int
	cachedLines     []string
}

func newTerminalUI(width, height int) *terminalUI {
	ui := &terminalUI{mode: consoleInputConversation}
	ui.resize(width, height)
	ui.resetHistoryNavigation()
	return ui
}

func (ui *terminalUI) setState(state consoleState) {
	wasRunning := ui.state.Running
	ui.state = state
	if !state.Running {
		ui.mode = consoleInputConversation
		ui.controlInputs = nil
		return
	}
	if !wasRunning {
		ui.mode = consoleInputSteering
	}
}

func (ui *terminalUI) presentInput(mode consoleInputMode, text string) {
	text = strings.TrimSpace(sanitizeTerminalText(text))
	if text == "" {
		return
	}
	if mode != consoleInputConversation {
		ui.controlInputs = append(ui.controlInputs, terminalControlInput{mode: mode, text: text})
		return
	}

	previousLineCount := len(ui.transcriptLines())
	ui.finishStream()
	ui.responseOpen = false
	ui.controlInputs = nil
	ui.entries = append(ui.entries, terminalEntry{kind: terminalEntryUser, text: text})
	ui.scroll = 0
	ui.unseenOutput = false
	ui.markOutput(previousLineCount)
}

func (ui *terminalUI) endResponse() {
	ui.finishStream()
	ui.responseOpen = false
	ui.controlInputs = nil
}

func (ui *terminalUI) clearTranscript() {
	ui.entries = nil
	ui.activeStream = terminalStreamNone
	ui.responseOpen = false
	ui.scroll = 0
	ui.unseenOutput = false
	ui.transcriptDirty = true
}

func (ui *terminalUI) resize(width, height int) bool {
	width = max(width, 8)
	height = max(height, terminalFooterHeight+1)
	if ui.width == width && ui.height == height {
		return false
	}
	ui.width = width
	ui.height = height
	return true
}

func (ui *terminalUI) appendStream(kind terminalStreamKind, text string) {
	text = sanitizeTerminalText(text)
	if text == "" {
		return
	}
	previousLineCount := len(ui.transcriptLines())
	entryKind := terminalEntryAssistant
	if kind == terminalStreamThinking {
		entryKind = terminalEntryThinking
	}
	if ui.activeStream == kind && len(ui.entries) > 0 && ui.entries[len(ui.entries)-1].kind == entryKind {
		ui.entries[len(ui.entries)-1].text += text
	} else {
		entry := terminalEntry{kind: entryKind, text: text}
		if kind == terminalStreamText {
			entry.continuation = ui.responseOpen
			ui.responseOpen = true
		}
		ui.entries = append(ui.entries, entry)
		ui.activeStream = kind
	}
	ui.markOutput(previousLineCount)
}

func (ui *terminalUI) appendNotice(text string) {
	text = strings.TrimSpace(sanitizeTerminalText(text))
	if text == "" {
		return
	}
	previousLineCount := len(ui.transcriptLines())
	ui.finishStream()
	ui.entries = append(ui.entries, terminalEntry{kind: terminalEntryNotice, text: text})
	ui.markOutput(previousLineCount)
}

func (ui *terminalUI) finishStream() { ui.activeStream = terminalStreamNone }

func (ui *terminalUI) markOutput(previousLineCount int) {
	ui.transcriptDirty = true
	if ui.scroll > 0 {
		ui.scroll += max(0, len(ui.transcriptLines())-previousLineCount)
		ui.unseenOutput = true
	}
}

type terminalInputAction struct {
	submit bool
	line   string
	mode   consoleInputMode
	err    error
}

func (ui *terminalUI) handleKey(key terminalKey) terminalInputAction {
	switch key.kind {
	case terminalKeyRunes, terminalKeyPaste:
		ui.editor.Insert(key.text)
		ui.resetHistoryNavigation()
	case terminalKeyLeft:
		ui.editor.Left()
	case terminalKeyRight:
		ui.editor.Right()
	case terminalKeyWordLeft:
		ui.editor.WordLeft()
	case terminalKeyWordRight:
		ui.editor.WordRight()
	case terminalKeyHome:
		ui.editor.Home()
	case terminalKeyEnd:
		ui.editor.End()
	case terminalKeyBackspace:
		ui.editor.Backspace()
		ui.resetHistoryNavigation()
	case terminalKeyDelete:
		ui.editor.Delete()
		ui.resetHistoryNavigation()
	case terminalKeyDeleteBefore:
		ui.editor.DeleteBefore()
		ui.resetHistoryNavigation()
	case terminalKeyDeleteAfter:
		ui.editor.DeleteAfter()
		ui.resetHistoryNavigation()
	case terminalKeyUp:
		ui.previousHistory()
	case terminalKeyDown:
		ui.nextHistory()
	case terminalKeyToggleMode:
		ui.toggleInputMode()
	case terminalKeyPageUp:
		ui.pageUp()
	case terminalKeyPageDown:
		ui.pageDown()
	case terminalKeyClear:
		ui.clearTranscript()
	case terminalKeyEnter:
		line := ui.editor.Value()
		ui.editor.Reset()
		if strings.TrimSpace(line) != "" {
			ui.history = append(ui.history, line)
		}
		ui.resetHistoryNavigation()
		mode := consoleInputConversation
		if ui.state.Running && ui.state.Approval == "" {
			mode = ui.mode
		}
		return terminalInputAction{submit: true, line: line, mode: mode}
	case terminalKeyInterrupt:
		return terminalInputAction{submit: true, err: errConsoleInterrupted}
	case terminalKeyEOF:
		if ui.editor.Value() == "" {
			return terminalInputAction{submit: true, err: io.EOF}
		}
		ui.editor.Delete()
	case terminalKeyReadError:
		return terminalInputAction{submit: true, err: key.err}
	}
	return terminalInputAction{}
}

func (ui *terminalUI) toggleInputMode() {
	if !ui.state.Running || ui.state.Approval != "" {
		return
	}
	if ui.mode == consoleInputFollowUp {
		ui.mode = consoleInputSteering
	} else {
		ui.mode = consoleInputFollowUp
	}
}

func (ui *terminalUI) pageUp() {
	ui.scroll += max(ui.viewportHeight()-1, 1)
	ui.clampScroll()
}

func (ui *terminalUI) pageDown() {
	ui.scroll = max(0, ui.scroll-max(ui.viewportHeight()-1, 1))
	if ui.scroll == 0 {
		ui.unseenOutput = false
	}
}

func (ui *terminalUI) previousHistory() {
	if len(ui.history) == 0 {
		return
	}
	if ui.historyIndex == len(ui.history) {
		ui.historyDraft = ui.editor.Value()
	}
	if ui.historyIndex > 0 {
		ui.historyIndex--
		ui.editor.SetValue(ui.history[ui.historyIndex])
	}
}

func (ui *terminalUI) nextHistory() {
	if len(ui.history) == 0 || ui.historyIndex >= len(ui.history) {
		return
	}
	ui.historyIndex++
	if ui.historyIndex >= len(ui.history) {
		ui.historyIndex = len(ui.history)
		ui.editor.SetValue(ui.historyDraft)
		return
	}
	ui.editor.SetValue(ui.history[ui.historyIndex])
}

func (ui *terminalUI) resetHistoryNavigation() {
	ui.historyIndex = len(ui.history)
	ui.historyDraft = ""
}

func (ui *terminalUI) viewportHeight() int { return max(ui.height-terminalFooterHeight, 1) }

func (ui *terminalUI) clampScroll() {
	lines := ui.transcriptLines()
	maximum := max(0, len(lines)-ui.viewportHeight())
	ui.scroll = min(ui.scroll, maximum)
}

func (ui *terminalUI) view() ([]string, int, int) {
	viewportHeight := ui.viewportHeight()
	transcript := ui.transcriptLines()
	maximumScroll := max(0, len(transcript)-viewportHeight)
	ui.scroll = min(ui.scroll, maximumScroll)
	end := max(0, len(transcript)-ui.scroll)
	start := max(0, end-viewportHeight)
	visible := append([]string(nil), transcript[start:end]...)
	for len(visible) < viewportHeight {
		visible = append(visible, "")
	}

	contextLine := ui.contextLine()
	status := ui.statusLine()
	inputTop, inputLine, inputBottom, cursorColumn := ui.inputBox()
	help := terminalDim(truncateDisplay(ui.helpText(), ui.width))
	view := append(visible, contextLine, status, inputTop, inputLine, inputBottom, help)
	return view, viewportHeight + 4, cursorColumn
}

func (ui *terminalUI) transcriptLines() []string {
	if !ui.transcriptDirty && ui.cachedWidth == ui.width {
		return ui.cachedLines
	}
	contentWidth := max(ui.width-2, 1)
	var lines []string
	for index, entry := range ui.entries {
		if index > 0 {
			lines = append(lines, "")
		}
		switch entry.kind {
		case terminalEntryNotice:
			for _, line := range wrapDisplay(strings.TrimRight(entry.text, "\n"), contentWidth) {
				lines = append(lines, terminalDim(line))
			}
		case terminalEntryUser:
			lines = append(lines, terminalMagenta("You"))
			lines = append(lines, wrapDisplay(strings.TrimRight(entry.text, "\n"), contentWidth)...)
		case terminalEntryAssistant:
			if !entry.continuation {
				lines = append(lines, terminalCyan("Po"))
			}
			lines = append(lines, wrapDisplay(strings.TrimRight(entry.text, "\n"), contentWidth)...)
		case terminalEntryThinking:
			lines = append(lines, terminalDim("Thinking"))
			for _, line := range wrapDisplay(strings.TrimRight(entry.text, "\n"), contentWidth) {
				lines = append(lines, terminalDim(line))
			}
		}
	}
	ui.cachedWidth = ui.width
	ui.cachedLines = lines
	ui.transcriptDirty = false
	return ui.cachedLines
}

func (ui *terminalUI) contextLine() string {
	var detail string
	switch {
	case ui.scroll > 0:
		detail = fmt.Sprintf("↑ viewing older output · %d lines from latest · PgDn returns", ui.scroll)
	case len(ui.controlInputs) > 0:
		latest := ui.controlInputs[len(ui.controlInputs)-1]
		label := "Steering submitted"
		if latest.mode == consoleInputFollowUp {
			label = "Follow-up queued"
		}
		detail = "↳ " + label + " · " + latest.text
		if earlier := len(ui.controlInputs) - 1; earlier > 0 {
			detail += fmt.Sprintf(" · +%d earlier", earlier)
		}
	case ui.state.Approval != "":
		detail = "Answer the approval request below"
	case ui.state.Running && ui.mode == consoleInputFollowUp:
		detail = "Queue mode · delivered only when the current run would naturally stop"
	case ui.state.Running:
		detail = "Steer mode · injected at the next safe turn boundary"
	default:
		detail = "New message · starts a run"
	}
	return terminalDim(truncateDisplay(strings.ReplaceAll(sanitizeTerminalText(detail), "\n", " "), ui.width))
}

func (ui *terminalUI) statusLine() string {
	var dot, detail string
	switch {
	case ui.state.Approval != "":
		dot = terminalYellow("●")
		detail = "Approval required · " + ui.state.Approval + " · type y to allow"
	case ui.state.Activity != "":
		dot = terminalCyan("●")
		detail = ui.state.Activity
	default:
		dot = terminalGreen("●")
		detail = "Ready"
	}
	if ui.unseenOutput {
		detail += "  ↓ new output"
	}
	detail = strings.ReplaceAll(sanitizeTerminalText(detail), "\n", " ")
	return dot + " " + truncateDisplay(detail, max(ui.width-2, 1))
}

func (ui *terminalUI) inputBox() (string, string, string, int) {
	innerWidth := max(ui.width-2, 1)
	top := "╭" + strings.Repeat("─", innerWidth) + "╮"
	bottom := "╰" + strings.Repeat("─", innerWidth) + "╯"
	label, placeholder, styleLabel := ui.inputPrompt()
	prefixWidth := 1 + runewidth.StringWidth(label)
	editorWidth := max(innerWidth-prefixWidth, 1)
	input, cursorOffset := ui.editor.Visible(editorWidth)
	display := input
	renderedInput := input
	if input == "" {
		display = truncateDisplay(placeholder, editorWidth)
		renderedInput = terminalDim(display)
	}
	plainPrefix := "│ " + label
	padding := max(0, innerWidth-prefixWidth-runewidth.StringWidth(display))
	line := "│ " + styleLabel(label) + renderedInput + strings.Repeat(" ", padding) + "│"
	cursorColumn := runewidth.StringWidth(plainPrefix) + cursorOffset + 1
	return top, line, bottom, min(cursorColumn, ui.width-1)
}

func (ui *terminalUI) inputPrompt() (label, placeholder string, style func(string) string) {
	switch {
	case ui.state.Approval != "":
		return "Allow › ", "Type y to allow; anything else denies", terminalYellow
	case ui.state.Running && ui.mode == consoleInputFollowUp:
		return "Queue › ", "Add a follow-up for after the current task", terminalYellow
	case ui.state.Running:
		return "Steer › ", "Change direction at the next safe boundary", terminalCyan
	default:
		return "› ", "Ask Po anything", terminalCyan
	}
}

func (ui *terminalUI) helpText() string {
	switch {
	case ui.state.Approval != "":
		return "Enter answer · y/yes allow · other deny · Ctrl-C cancel"
	case ui.state.Running && ui.mode == consoleInputFollowUp:
		return "Enter queue · Tab steer · ⇧↑↓/PgUp/PgDn scroll · Ctrl-C stop"
	case ui.state.Running:
		return "Enter steer · Tab queue · ⇧↑↓/PgUp/PgDn scroll · Ctrl-C stop"
	default:
		return "Enter send · ↑↓ history · ⇧↑↓/PgUp/PgDn scroll · Ctrl-L clear · Ctrl-C exit"
	}
}
