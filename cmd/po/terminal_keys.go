package main

import (
	"bufio"
	"bytes"
	"io"
	"strconv"
	"strings"
	"unicode"
)

type terminalKeyKind uint8

const (
	terminalKeyRunes terminalKeyKind = iota
	terminalKeyPaste
	terminalKeyLeft
	terminalKeyRight
	terminalKeyWordLeft
	terminalKeyWordRight
	terminalKeyHome
	terminalKeyEnd
	terminalKeyUp
	terminalKeyDown
	terminalKeyToggleMode
	terminalKeyPageUp
	terminalKeyPageDown
	terminalKeyScrollUp
	terminalKeyScrollDown
	terminalKeyClear
	terminalKeyBackspace
	terminalKeyDelete
	terminalKeyDeleteBefore
	terminalKeyDeleteAfter
	terminalKeyEnter
	terminalKeyInterrupt
	terminalKeyEOF
	terminalKeyReadError
	terminalKeyIgnored
)

type terminalKey struct {
	kind terminalKeyKind
	text string
	err  error
}

func readTerminalKey(reader *bufio.Reader) (terminalKey, error) {
	current, _, err := reader.ReadRune()
	if err != nil {
		return terminalKey{}, err
	}
	switch current {
	case '\r', '\n':
		return terminalKey{kind: terminalKeyEnter}, nil
	case 0x01:
		return terminalKey{kind: terminalKeyHome}, nil
	case 0x02:
		return terminalKey{kind: terminalKeyLeft}, nil
	case 0x03:
		return terminalKey{kind: terminalKeyInterrupt}, nil
	case 0x04:
		return terminalKey{kind: terminalKeyEOF}, nil
	case 0x05:
		return terminalKey{kind: terminalKeyEnd}, nil
	case 0x06:
		return terminalKey{kind: terminalKeyRight}, nil
	case 0x08, 0x7f:
		return terminalKey{kind: terminalKeyBackspace}, nil
	case 0x09:
		return terminalKey{kind: terminalKeyToggleMode}, nil
	case 0x0b:
		return terminalKey{kind: terminalKeyDeleteAfter}, nil
	case 0x0c:
		return terminalKey{kind: terminalKeyClear}, nil
	case 0x15:
		return terminalKey{kind: terminalKeyDeleteBefore}, nil
	case 0x1b:
		return readEscapeKey(reader)
	default:
		if unicode.IsControl(current) {
			return terminalKey{kind: terminalKeyRunes}, nil
		}
		return terminalKey{kind: terminalKeyRunes, text: string(current)}, nil
	}
}

func readEscapeKey(reader *bufio.Reader) (terminalKey, error) {
	next, err := reader.ReadByte()
	if err != nil {
		return terminalKey{}, err
	}
	if next != '[' && next != 'O' {
		switch next {
		case 'b':
			return terminalKey{kind: terminalKeyWordLeft}, nil
		case 'f':
			return terminalKey{kind: terminalKeyWordRight}, nil
		default:
			return terminalKey{kind: terminalKeyRunes}, nil
		}
	}

	var sequence strings.Builder
	for {
		current, err := reader.ReadByte()
		if err != nil {
			return terminalKey{}, err
		}
		sequence.WriteByte(current)
		if current >= 0x40 && current <= 0x7e {
			break
		}
	}
	value := sequence.String()
	if value == "M" {
		return readX10Mouse(reader)
	}
	if key, ok := parseSGRMouse(value); ok {
		return key, nil
	}
	switch value {
	case "A":
		return terminalKey{kind: terminalKeyUp}, nil
	case "B":
		return terminalKey{kind: terminalKeyDown}, nil
	case "C":
		return terminalKey{kind: terminalKeyRight}, nil
	case "D":
		return terminalKey{kind: terminalKeyLeft}, nil
	case "H", "1~", "7~":
		return terminalKey{kind: terminalKeyHome}, nil
	case "F", "4~", "8~":
		return terminalKey{kind: terminalKeyEnd}, nil
	case "3~":
		return terminalKey{kind: terminalKeyDelete}, nil
	case "5~":
		return terminalKey{kind: terminalKeyPageUp}, nil
	case "6~":
		return terminalKey{kind: terminalKeyPageDown}, nil
	case "1;2A", "1;3A", "1;5A":
		return terminalKey{kind: terminalKeyPageUp}, nil
	case "1;2B", "1;3B", "1;5B":
		return terminalKey{kind: terminalKeyPageDown}, nil
	case "1;5D", "1;3D":
		return terminalKey{kind: terminalKeyWordLeft}, nil
	case "1;5C", "1;3C":
		return terminalKey{kind: terminalKeyWordRight}, nil
	case "200~":
		pasted, err := readBracketedPaste(reader)
		return terminalKey{kind: terminalKeyPaste, text: pasted}, err
	default:
		return terminalKey{kind: terminalKeyRunes}, nil
	}
}

func parseSGRMouse(sequence string) (terminalKey, bool) {
	if !strings.HasPrefix(sequence, "<") ||
		(!strings.HasSuffix(sequence, "M") && !strings.HasSuffix(sequence, "m")) {
		return terminalKey{}, false
	}
	fields := strings.Split(sequence[1:len(sequence)-1], ";")
	if len(fields) != 3 {
		return terminalKey{kind: terminalKeyIgnored}, true
	}
	button, err := strconv.Atoi(fields[0])
	if err != nil {
		return terminalKey{kind: terminalKeyIgnored}, true
	}
	return mouseButtonKey(button), true
}

func readX10Mouse(reader *bufio.Reader) (terminalKey, error) {
	data := make([]byte, 3)
	if _, err := io.ReadFull(reader, data); err != nil {
		return terminalKey{}, err
	}
	return mouseButtonKey(int(data[0]) - 32), nil
}

func mouseButtonKey(button int) terminalKey {
	// Shift、Alt 和 Ctrl 会占用按钮编码中间的修饰位，不应改变滚轮方向。
	button &^= 4 | 8 | 16
	switch button {
	case 64:
		return terminalKey{kind: terminalKeyScrollUp}
	case 65:
		return terminalKey{kind: terminalKeyScrollDown}
	default:
		return terminalKey{kind: terminalKeyIgnored}
	}
}

func readBracketedPaste(reader *bufio.Reader) (string, error) {
	// 括号粘贴模式可以避免把粘贴文本中类似转义序列的字节解释成导航按键。
	const pasteEnd = "\x1b[201~"
	var pasted bytes.Buffer
	for {
		current, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		pasted.WriteByte(current)
		if bytes.HasSuffix(pasted.Bytes(), []byte(pasteEnd)) {
			value := pasted.Bytes()
			return string(value[:len(value)-len(pasteEnd)]), nil
		}
	}
}
