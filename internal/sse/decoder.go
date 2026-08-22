package sse

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrEventTooLarge 表示单个 SSE Event 累积的 data
// 超过 Runtime 配置上限。
var ErrEventTooLarge = errors.New("sse event too large")

// Event 是 Po Provider 真正需要的最小 SSE framing 结果。
//
// Type 对应 event:。
// ID 对应 SSE last event id。
// Data 是多个 data: 行通过 '\n' 拼接后的结果。
type Event struct {
	Type string
	ID   string
	Data string
}

// Decoder 从任意 io.Reader 中解析 SSE Event。
// 它不知道：
//   - OpenAI；
//   - 工具调用；
//   - ModelDelta。
//
// 它只负责 SSE framing。
type Decoder struct {
	scanner *bufio.Scanner

	// 当前尚未 dispatch 的 event:。
	eventType string

	// SSE id 可以跨 Event 保留。
	lastID string

	// 当前 Event 的所有 data: 行。
	data []string

	// eventBytes 防止很多小 data 行累计成无限 Event。
	eventBytes    int
	maxEventBytes int
}

// NewDecoder 创建 SSE Decoder。
//
// maxLineBytes：单行最大字节。
// maxEventBytes：一个 Event 内所有 data value 累计上限, <=0 时使用 Po 默认值。
func NewDecoder(reader io.Reader, maxLineBytes int, maxEventBytes int) (*Decoder, error) {
	if reader == nil {
		return nil, fmt.Errorf("sse reader is required")
	}
	if maxLineBytes <= 0 {
		maxLineBytes = 1 << 20 // 1 MiB
	}
	if maxEventBytes <= 0 {
		maxEventBytes = 4 << 20 // 4 MiB
	}
	scanner := bufio.NewScanner(reader)

	// 初始只申请 32 KiB。
	// 遇到更长正常行时 Scanner 可以增长，
	// 但绝不超过 maxLineBytes。
	scanner.Buffer(make([]byte, 32*1024), maxLineBytes)
	return &Decoder{scanner: scanner, maxEventBytes: maxEventBytes}, nil
}

// Next 返回下一条完整 SSE Event。
//
// 它可能跨越任意数量底层 reader.Read。
// 网络 chunk 没有 SSE Event 边界语义。
func (d *Decoder) Next() (Event, error) {
	for d.scanner.Scan() {
		// Scanner 去掉 '\n'，
		// 但 CRLF 情况仍可能留下 '\r'。
		line := strings.TrimSuffix(d.scanner.Text(), "\r")

		// 空行 dispatch 当前 Event。
		if line == "" {
			if len(d.data) == 0 {
				// 没有 data 的 Event 不向上层发送。
				d.eventType = ""
				continue
			}
			return d.flush(), nil
		}

		// ":" 开头是 comment / keepalive。
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			// SSE 只忽略冒号后的一个可选空格。
			// 不使用 TrimSpace，避免改变真实 Data。
			value = strings.TrimPrefix(value, " ")
		} else {
			// 没有冒号时整行是 field，value 为空。
			value = ""
		}
		switch field {
		case "event":
			d.eventType = value
		case "id":
			// 包含 NUL 的 id 不更新 lastID。
			if !strings.ContainsRune(value, '\x00') {
				d.lastID = value
			}
		case "data":
			d.eventBytes += len(value)
			if d.eventBytes > d.maxEventBytes {
				return Event{}, ErrEventTooLarge
			}
			d.data = append(d.data, value)
		case "retry":
		// retry: 是 EventSource 重连建议。
		//
		// Po 的 Provider Retry 已经由 Runtime 管理，
		// Parser 不在这里 sleep 或 reconnect。
		default:
			// SSE 允许未知扩展字段。
			// Parser 不应因此失败。
		}
	}
	if err := d.scanner.Err(); err != nil {
		return Event{}, fmt.Errorf("read sse stream: %w", err)
	}

	// 有些兼容服务 EOF 前缺少最后一个空行。
	// 已经累积 data 时仍然返回最后 Event。
	if len(d.data) > 0 {
		return d.flush(), nil
	}
	return Event{}, io.EOF
}

// flush 构造 Event 并清空“只属于当前 Event”的状态。
func (d *Decoder) flush() Event {
	eventType := d.eventType
	if eventType == "" {
		eventType = "message"
	}
	event := Event{Type: eventType, ID: d.lastID, Data: strings.Join(d.data, "\n")}

	// event: 不跨 Event 保留。
	d.eventType = ""

	// id: 按 SSE last event id 语义保留。
	// 复用 slice capacity，减少 Streaming 期间重复分配。
	d.data = d.data[:0]
	d.eventBytes = 0
	return event
}
