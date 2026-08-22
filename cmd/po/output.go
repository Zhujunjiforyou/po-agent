package main

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

const maxConsecutiveLineBreaks = 2

// modelOutput 是模型流式回调使用的唯一输出路径。精简的接口让单次命令可以直接写入，
// 同时让交互终端能够在显示新输出前保护当前输入行。
type modelOutput interface {
	WriteModelText(string) error
	WriteThinking(string) error
	FinishModelMessage() error
}

type plainModelOutput struct {
	text     *compactStream
	thinking *compactStream
	textSeen atomic.Bool
}

func newPlainModelOutput(stdout, stderr io.Writer) *plainModelOutput {
	return &plainModelOutput{
		text:     newCompactStream(stdout),
		thinking: newCompactStream(stderr),
	}
}

func (o *plainModelOutput) WriteModelText(text string) error {
	if text != "" {
		o.textSeen.Store(true)
	}
	return o.text.WriteString(text)
}
func (o *plainModelOutput) WriteThinking(text string) error { return o.thinking.WriteString(text) }

func (o *plainModelOutput) HasModelText() bool { return o.textSeen.Load() }

func (o *plainModelOutput) FinishModelMessage() error {
	if err := o.thinking.FinishMessage(); err != nil {
		return err
	}
	return o.text.FinishMessage()
}

// compactStream 在保留流式行为的同时移除开头空行，并将间隔限制为一个空行。状态需要
// 跨数据块保存，因为模型服务可能把连续换行拆分到多个增量中。
type compactStream struct {
	mu sync.Mutex

	writer           io.Writer
	messageStarted   bool
	trailingNewlines int
}

func newCompactStream(writer io.Writer) *compactStream {
	return &compactStream{writer: writer}
}

func (w *compactStream) WriteString(text string) error {
	if text == "" {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	normalized := normalizeNewlines(text)
	var output strings.Builder
	for _, current := range normalized {
		if current == '\n' {
			if !w.messageStarted || w.trailingNewlines >= maxConsecutiveLineBreaks {
				continue
			}
			output.WriteRune(current)
			w.trailingNewlines++
			continue
		}

		output.WriteRune(current)
		w.messageStarted = true
		w.trailingNewlines = 0
	}
	if output.Len() == 0 {
		return nil
	}
	_, err := io.WriteString(w.writer, output.String())
	return err
}

func (w *compactStream) FinishMessage() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.messageStarted && w.trailingNewlines == 0 {
		if _, err := io.WriteString(w.writer, "\n"); err != nil {
			return err
		}
	}
	w.messageStarted = false
	w.trailingNewlines = 0
	return nil
}

func normalizeNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func compactBlock(text string) string {
	var builder strings.Builder
	stream := newCompactStream(&builder)
	_ = stream.WriteString(text)
	_ = stream.FinishMessage()
	return builder.String()
}
