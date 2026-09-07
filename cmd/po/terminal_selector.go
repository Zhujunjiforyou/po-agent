package main

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// terminalSelector 只处理选择器的显示和按键状态。它不知道 ID 对应模型、Session
// 还是其他业务对象，因此后续命令可以共用同一套交互。
type terminalSelector struct {
	title    string
	choices  []consoleChoice
	filtered []int
	selected int
	filter   lineEditor
}

type terminalSelectorRow struct {
	choiceIndex int
	text        string
}

func newTerminalSelector(title string, choices []consoleChoice) *terminalSelector {
	selector := &terminalSelector{
		title:   title,
		choices: append([]consoleChoice(nil), choices...),
	}
	selector.rebuild("")
	for index, choiceIndex := range selector.filtered {
		if selector.choices[choiceIndex].Current {
			selector.selected = index
			break
		}
	}
	return selector
}

func (s *terminalSelector) handleKey(key terminalKey) terminalInputAction {
	switch key.kind {
	case terminalKeyRunes, terminalKeyPaste:
		if key.text != "" {
			s.filter.Insert(key.text)
			s.rebuild(s.filter.Value())
		}
	case terminalKeyLeft:
		s.filter.Left()
	case terminalKeyRight:
		s.filter.Right()
	case terminalKeyWordLeft:
		s.filter.WordLeft()
	case terminalKeyWordRight:
		s.filter.WordRight()
	case terminalKeyHome:
		s.filter.Home()
	case terminalKeyEnd:
		s.filter.End()
	case terminalKeyBackspace:
		s.filter.Backspace()
		s.rebuild(s.filter.Value())
	case terminalKeyDelete:
		s.filter.Delete()
		s.rebuild(s.filter.Value())
	case terminalKeyDeleteBefore:
		s.filter.DeleteBefore()
		s.rebuild(s.filter.Value())
	case terminalKeyDeleteAfter:
		s.filter.DeleteAfter()
		s.rebuild(s.filter.Value())
	case terminalKeyUp:
		s.selected = max(0, s.selected-1)
	case terminalKeyDown:
		s.selected = min(max(len(s.filtered)-1, 0), s.selected+1)
	case terminalKeyScrollUp:
		s.selected = max(0, s.selected-terminalWheelScrollLines)
	case terminalKeyScrollDown:
		s.selected = min(max(len(s.filtered)-1, 0), s.selected+terminalWheelScrollLines)
	case terminalKeyEnter:
		if len(s.filtered) > 0 {
			return terminalInputAction{
				selectionID:   s.choices[s.filtered[s.selected]].ID,
				selectionDone: true,
			}
		}
	case terminalKeyInterrupt, terminalKeyEOF:
		return terminalInputAction{selectionDone: true}
	case terminalKeyReadError:
		return terminalInputAction{selectionDone: true, err: key.err}
	}
	return terminalInputAction{}
}

func (s *terminalSelector) rebuild(query string) {
	query = strings.ToLower(strings.TrimSpace(query))
	selectedID := ""
	if len(s.filtered) > 0 && s.selected < len(s.filtered) {
		selectedID = s.choices[s.filtered[s.selected]].ID
	}
	s.filtered = s.filtered[:0]
	for index, choice := range s.choices {
		haystack := strings.ToLower(choice.Group + " " + choice.Label + " " + choice.Description)
		if query == "" || strings.Contains(haystack, query) {
			s.filtered = append(s.filtered, index)
		}
	}
	s.selected = 0
	for index, choiceIndex := range s.filtered {
		if s.choices[choiceIndex].ID == selectedID {
			s.selected = index
			break
		}
	}
}

func (s *terminalSelector) view(width, height int) ([]string, int, int) {
	width = max(width, 8)
	height = max(height, 6)
	lines := make([]string, height)
	lines[0] = terminalCyan(s.title)

	filterWidth := max(width-len("Search: "), 1)
	filter, cursorOffset := s.filter.Visible(filterWidth)
	displayFilter := filter
	if displayFilter == "" {
		displayFilter = terminalDim("type to filter")
	}
	lines[1] = "Search: " + displayFilter

	listHeight := max(height-3, 1)
	rows, selectedRow := s.rows(width)
	start := max(0, selectedRow-listHeight/2)
	start = min(start, max(len(rows)-listHeight, 0))
	end := min(start+listHeight, len(rows))
	for index := start; index < end; index++ {
		lines[2+index-start] = rows[index].text
	}
	if len(rows) == 0 {
		lines[2] = terminalDim("  No matching choices")
	}
	lines[height-1] = terminalDim(truncateDisplay(
		"↑↓/wheel select · type to filter · Enter confirm · Ctrl-C cancel",
		width,
	))
	return lines, 2, min(runewidth.StringWidth("Search: ")+cursorOffset+1, width)
}

func (s *terminalSelector) rows(width int) ([]terminalSelectorRow, int) {
	rows := make([]terminalSelectorRow, 0, len(s.filtered)*2)
	group := ""
	selectedRow := 0
	for filteredIndex, choiceIndex := range s.filtered {
		choice := s.choices[choiceIndex]
		if choice.Group != group {
			group = choice.Group
			rows = append(rows, terminalSelectorRow{choiceIndex: -1, text: terminalDim(group)})
		}
		marker := "  "
		if filteredIndex == s.selected {
			marker = terminalCyan("› ")
			selectedRow = len(rows)
		}
		currentLabel := ""
		current := ""
		if choice.Current {
			currentLabel = "  current"
			current = terminalGreen(currentLabel)
		}
		label := choice.Label
		if choice.Description != "" {
			label += "  " + choice.Description
		}
		labelWidth := max(width-4-runewidth.StringWidth(currentLabel), 1)
		label = truncateDisplay(label, labelWidth)
		rows = append(rows, terminalSelectorRow{
			choiceIndex: choiceIndex,
			text:        marker + label + current,
		})
	}
	return rows, selectedRow
}
