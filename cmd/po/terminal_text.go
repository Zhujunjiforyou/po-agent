package main

import (
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// lineEditor 按字素簇移动，并根据显示宽度计算光标列，从而让中日韩字符和组合表情符号
// 与终端单元格保持对齐。
type lineEditor struct {
	clusters []string
	cursor   int
}

func (e *lineEditor) Value() string { return strings.Join(e.clusters, "") }

func (e *lineEditor) SetValue(value string) {
	e.clusters = splitGraphemes(sanitizeInput(value))
	e.cursor = len(e.clusters)
}

func (e *lineEditor) Reset() {
	e.clusters = nil
	e.cursor = 0
}

func (e *lineEditor) Insert(value string) {
	value = sanitizeInput(value)
	if value == "" {
		return
	}
	before := strings.Join(e.clusters[:e.cursor], "")
	after := strings.Join(e.clusters[e.cursor:], "")
	targetBytes := len(before) + len(value)
	e.clusters = splitGraphemes(before + value + after)
	e.cursor = clusterBoundaryAtOrAfter(e.clusters, targetBytes)
}

func (e *lineEditor) Left()  { e.cursor = max(0, e.cursor-1) }
func (e *lineEditor) Right() { e.cursor = min(len(e.clusters), e.cursor+1) }
func (e *lineEditor) Home()  { e.cursor = 0 }
func (e *lineEditor) End()   { e.cursor = len(e.clusters) }

func (e *lineEditor) WordLeft() {
	for e.cursor > 0 && clusterIsSpace(e.clusters[e.cursor-1]) {
		e.cursor--
	}
	for e.cursor > 0 && !clusterIsSpace(e.clusters[e.cursor-1]) {
		e.cursor--
	}
}

func (e *lineEditor) WordRight() {
	for e.cursor < len(e.clusters) && clusterIsSpace(e.clusters[e.cursor]) {
		e.cursor++
	}
	for e.cursor < len(e.clusters) && !clusterIsSpace(e.clusters[e.cursor]) {
		e.cursor++
	}
}

func (e *lineEditor) Backspace() {
	if e.cursor == 0 {
		return
	}
	e.clusters = append(e.clusters[:e.cursor-1], e.clusters[e.cursor:]...)
	e.cursor--
}

func (e *lineEditor) Delete() {
	if e.cursor >= len(e.clusters) {
		return
	}
	e.clusters = append(e.clusters[:e.cursor], e.clusters[e.cursor+1:]...)
}

func (e *lineEditor) DeleteBefore() {
	e.clusters = append([]string(nil), e.clusters[e.cursor:]...)
	e.cursor = 0
}

func (e *lineEditor) DeleteAfter() { e.clusters = e.clusters[:e.cursor] }

func (e lineEditor) Visible(width int) (string, int) {
	width = max(width, 1)
	start := 0
	for start < e.cursor && displayWidth(e.clusters[start:e.cursor]) >= width {
		start++
	}
	end := start
	used := 0
	for end < len(e.clusters) {
		clusterWidth := runewidth.StringWidth(e.clusters[end])
		if used+clusterWidth > width {
			break
		}
		used += clusterWidth
		end++
	}
	return strings.Join(e.clusters[start:end], ""), displayWidth(e.clusters[start:e.cursor])
}

func splitGraphemes(value string) []string {
	graphemes := uniseg.NewGraphemes(value)
	clusters := make([]string, 0, uniseg.GraphemeClusterCount(value))
	for graphemes.Next() {
		clusters = append(clusters, graphemes.Str())
	}
	return clusters
}

func clusterBoundaryAtOrAfter(clusters []string, targetBytes int) int {
	consumed := 0
	for index, cluster := range clusters {
		consumed += len(cluster)
		if consumed >= targetBytes {
			return index + 1
		}
	}
	return len(clusters)
}

func clusterIsSpace(cluster string) bool {
	for _, current := range cluster {
		return unicode.IsSpace(current)
	}
	return false
}

func displayWidth(clusters []string) int { return runewidth.StringWidth(strings.Join(clusters, "")) }

func wrapDisplay(text string, width int) []string {
	width = max(width, 1)
	logicalLines := strings.Split(normalizeNewlines(text), "\n")
	wrapped := make([]string, 0, len(logicalLines))
	for _, line := range logicalLines {
		clusters := splitGraphemes(line)
		if len(clusters) == 0 {
			wrapped = append(wrapped, "")
			continue
		}
		for len(clusters) > 0 {
			end, used, lastSpace := 0, 0, -1
			for end < len(clusters) {
				clusterWidth := runewidth.StringWidth(clusters[end])
				if end > 0 && used+clusterWidth > width {
					break
				}
				used += clusterWidth
				if clusterIsSpace(clusters[end]) {
					lastSpace = end
				}
				end++
				if used >= width {
					break
				}
			}
			if end < len(clusters) && lastSpace > 0 {
				end = lastSpace
			}
			if end == 0 {
				end = 1
			}
			wrapped = append(wrapped, strings.TrimRightFunc(strings.Join(clusters[:end], ""), unicode.IsSpace))
			clusters = clusters[end:]
			for len(clusters) > 0 && clusterIsSpace(clusters[0]) {
				clusters = clusters[1:]
			}
		}
	}
	return wrapped
}

func sanitizeTerminalText(text string) string {
	// 运行时文本和模型文本都是不可信终端数据。保留可打印内容，但在数据进入 ANSI
	// 渲染器前丢弃控制字符。
	text = normalizeNewlines(text)
	var output strings.Builder
	for _, current := range text {
		switch {
		case current == '\n':
			output.WriteRune(current)
		case current == '\t':
			output.WriteString("    ")
		case !unicode.IsControl(current):
			output.WriteRune(current)
		}
	}
	return output.String()
}

func sanitizeInput(text string) string {
	text = sanitizeTerminalText(text)
	return strings.ReplaceAll(text, "\n", " ")
}

func truncateDisplay(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	clusters := splitGraphemes(text)
	used := 0
	end := 0
	for end < len(clusters) {
		clusterWidth := runewidth.StringWidth(clusters[end])
		if used+clusterWidth > width-1 {
			break
		}
		used += clusterWidth
		end++
	}
	return strings.Join(clusters[:end], "") + "…"
}

func terminalCyan(text string) string    { return "\x1b[36m" + text + "\x1b[0m" }
func terminalGreen(text string) string   { return "\x1b[32m" + text + "\x1b[0m" }
func terminalMagenta(text string) string { return "\x1b[35m" + text + "\x1b[0m" }
func terminalYellow(text string) string  { return "\x1b[33m" + text + "\x1b[0m" }
func terminalDim(text string) string     { return "\x1b[2m" + text + "\x1b[0m" }
