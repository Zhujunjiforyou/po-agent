// Package output 提供 Tool 层可复用的文本输出整形能力。
//
// Po Core 不拥有一个全局的“Tool 输出最大长度”配置，因为不同 Tool 的信息分布不同：
// read/search 往往更关心开头，build/test/shell 日志往往更关心结尾。具体 Tool 应自己
// 选择保留策略，并在需要时把完整输出保存到 Artifact、临时文件或其他外部存储中。
package output

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultMaxBytes 是一个适合 Coding Agent 的经验默认值，不是协议硬限制。
	// Tool 可以根据自己的数据语义显式传入更小或更大的限制。
	DefaultMaxBytes = 50 * 1024
	DefaultMaxLines = 2000
)

// Limits 描述“允许送回模型上下文”的文本上限。
//
// 字段为 0 时使用 package 默认值，而不是表示“不限制”。这样 Limits{} 的零值就是
// 一个安全可用的配置，不会因为调用方忘记赋值而意外把几十 MB 输出塞进 Context。
type Limits struct {
	MaxBytes int
	MaxLines int
}

// Result 同时记录保留下来的文本和被省略的信息量。
//
// Tool 可以把这些计数写入 ToolResult.Details。当 Truncated=true 时，还可以把完整
// 输出保存到外部位置，然后用 Notice 告诉模型“当前看到的只是一个窗口”。
type Result struct {
	Content string

	Truncated bool

	TotalBytes  int
	OutputBytes int
	TotalLines  int
	OutputLines int
}

// OmittedBytes 返回没有出现在 Content 中的原始字节数。
func (r Result) OmittedBytes() int { return r.TotalBytes - r.OutputBytes }

// OmittedLines 返回没有出现在 Content 中的逻辑行数。
func (r Result) OmittedLines() int { return r.TotalLines - r.OutputLines }

// Notice 构造一段明确的模型可见提示，避免“静默截断”。
//
// fullOutputRef 可以是临时文件路径、Artifact ID、分页 cursor 等任何可继续访问完整
// 数据的引用。当前 Tool 没有完整输出存储时可以传空字符串。
func (r Result) Notice(fullOutputRef string) string {
	if !r.Truncated {
		return ""
	}

	notice := fmt.Sprintf(
		"[Output truncated: showing %d of %d lines and %d of %d bytes; %d lines / %d bytes omitted.",
		r.OutputLines,
		r.TotalLines,
		r.OutputBytes,
		r.TotalBytes,
		r.OmittedLines(),
		r.OmittedBytes(),
	)
	if fullOutputRef != "" {
		notice += " Full output: " + fullOutputRef
	}
	return notice + "]"
}

// TruncateHead 保留文本开头。
//
// 它通常适合 read、目录列表和按相关性排序的搜索结果，因为这些 Tool 的前部信息更
// 可能直接决定模型下一步要做什么。
func TruncateHead(text string, limits Limits) Result {
	limits = limits.normalized()
	return truncate(text, limits, true)
}

// TruncateTail 保留文本结尾。
//
// 它通常适合 shell/build/test 日志，因为最终失败摘要、panic 尾部和编译器错误经常
// 出现在输出末尾。注意这只是常见策略，不是所有日志都必须 Tail。
func TruncateTail(text string, limits Limits) Result {
	limits = limits.normalized()
	return truncate(text, limits, false)
}

func (l Limits) normalized() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxLines <= 0 {
		l.MaxLines = DefaultMaxLines
	}
	return l
}

func truncate(text string, limits Limits, keepHead bool) Result {
	totalBytes := len(text)
	totalLines := countLines(text)

	// 快路径：如果原文本同时满足 bytes 和 lines 两个限制，就直接返回原 string。
	// Go string 是不可变值，因此这里不需要为了“防御性复制”重新分配一份字符串。
	if totalBytes <= limits.MaxBytes && totalLines <= limits.MaxLines {
		return Result{
			Content:     text,
			Truncated:   false,
			TotalBytes:  totalBytes,
			OutputBytes: totalBytes,
			TotalLines:  totalLines,
			OutputLines: totalLines,
		}
	}

	var content string
	if keepHead {
		// 两个限制取更严格者。例如行数没有超限，但某一行是很大的 minified JSON，
		// 此时 MaxBytes 必须先截断。
		endByBytes := safeHeadByteEnd(text, limits.MaxBytes)
		endByLines := headLineEnd(text, limits.MaxLines)
		end := min(endByBytes, endByLines)
		content = text[:end]
	} else {
		startByBytes := safeTailByteStart(text, limits.MaxBytes)
		startByLines := tailLineStart(text, limits.MaxLines)
		start := max(startByBytes, startByLines)
		content = text[start:]
	}

	return Result{
		Content:     content,
		Truncated:   true,
		TotalBytes:  totalBytes,
		OutputBytes: len(content),
		TotalLines:  totalLines,
		OutputLines: countLines(content),
	}
}

// countLines 统计“逻辑文本行”。如果文本以 \n 结尾，最后那个空 segment 不再额外
// 算一行，因此 "a\n" 是 1 行，"a\nb" 是 2 行。
func countLines(text string) int {
	if text == "" {
		return 0
	}

	lines := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		lines++
	}
	return lines
}

// headLineEnd 返回“保留前 maxLines 行”时的结束 byte index。
func headLineEnd(text string, maxLines int) int {
	if countLines(text) <= maxLines {
		return len(text)
	}

	position := 0
	for range maxLines {
		relative := strings.IndexByte(text[position:], '\n')
		if relative < 0 {
			return len(text)
		}
		position += relative + 1
	}
	return position
}

// tailLineStart 返回“保留最后 maxLines 行”时的开始 byte index。
func tailLineStart(text string, maxLines int) int {
	totalLines := countLines(text)
	if totalLines <= maxLines {
		return 0
	}

	linesToDrop := totalLines - maxLines
	position := 0
	for range linesToDrop {
		relative := strings.IndexByte(text[position:], '\n')
		if relative < 0 {
			return len(text)
		}
		position += relative + 1
	}
	return position
}

// safeHeadByteEnd 解决“字节限制落在一个 UTF-8 多字节字符中间”的问题。
//
// len(string) 统计 byte，而不是 rune。假如 MaxBytes 正好切在汉字第二个 byte，直接
// text[:MaxBytes] 会得到非法 UTF-8。这里向左退到一个完整前缀边界再返回。
func safeHeadByteEnd(text string, maxBytes int) int {
	if len(text) <= maxBytes {
		return len(text)
	}

	end := maxBytes
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return end
}

// safeTailByteStart 是 Tail 对称版本：如果起点落在 UTF-8 continuation byte 上，就
// 向右移动到下一个 rune 起点，保证返回的后缀仍然是合法 UTF-8。
func safeTailByteStart(text string, maxBytes int) int {
	if len(text) <= maxBytes {
		return 0
	}

	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return start
}
