package main

import (
	"bufio"
	"bytes"
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
	terminalKeyClear
	terminalKeyBackspace
	terminalKeyDelete
	terminalKeyDeleteBefore
	terminalKeyDeleteAfter
	terminalKeyEnter
	terminalKeyInterrupt
	terminalKeyEOF
	terminalKeyReadError
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
