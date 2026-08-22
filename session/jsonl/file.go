// Package jsonl 实现 Session 的 append-only JSONL Tree Journal。
//
// v1 文件只有线性 message records；v2 在不重写旧历史的前提下增加：
//   - message parent_id：形成 branch tree；
//   - head：持久化 active leaf；
//   - run_start / run_end / run_resolved：标记语义恢复边界。
//
// 已有 v1 文件第一次发生新写入时，会追加一条 format_upgrade record 切换到 v2，旧消息
// 在内存中按物理顺序迁移成线性 parent chain。这种迁移仍然保持 append-only。
package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/session"
)

const (
	Version        = 2
	legacyVersion  = 1
	maxRecordBytes = 8 << 20
)

var (
	ErrInvalidFile        = errors.New("invalid jsonl session file")
	ErrUnsupportedVersion = errors.New("unsupported jsonl session version")
	ErrRecordTooLarge     = errors.New("jsonl session record too large")
	ErrClosed             = errors.New("jsonl session file is closed")
	// ErrUncertainJournal 表示某次 append / fsync 已失败，当前进程无法再证明文件尾部处于可继续追加的边界。
	// 调用方必须 Close 后重新 Open，让 replay/repair 决定真实状态。
	ErrUncertainJournal = errors.New("jsonl session journal state is uncertain; reopen required")
)

type headerRecord struct {
	Type      string    `json:"type"`
	Version   int       `json:"version"`
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
}

type upgradeRecord struct {
	Type     string `json:"type"`
	Sequence uint64 `json:"sequence"`
	Version  int    `json:"version"`
}

type messageRecord struct {
	Type      string          `json:"type"`
	Sequence  uint64          `json:"sequence"`
	Timestamp time.Time       `json:"timestamp"`
	ID        string          `json:"id,omitempty"`
	ParentID  string          `json:"parent_id,omitempty"`
	Message   json.RawMessage `json:"message"`
}

type headRecord struct {
	Type      string    `json:"type"`
	Sequence  uint64    `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	LeafID    string    `json:"leaf_id"`
}

type runStartRecord struct {
	Type          string    `json:"type"`
	Sequence      uint64    `json:"sequence"`
	Timestamp     time.Time `json:"timestamp"`
	AttemptID     string    `json:"attempt_id"`
	UserMessageID string    `json:"user_message_id"`
}

type runEndRecord struct {
	Type      string    `json:"type"`
	Sequence  uint64    `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	AttemptID string    `json:"attempt_id"`
	RunID     string    `json:"run_id,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type runResolvedRecord struct {
	Type      string    `json:"type"`
	Sequence  uint64    `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	AttemptID string    `json:"attempt_id"`
	Note      string    `json:"note,omitempty"`
}

type recordEnvelope struct {
	Type     string `json:"type"`
	Sequence uint64 `json:"sequence"`
}

// File 是一个 Session JSONL 的独占 append handle。
type File struct {
	mu sync.Mutex

	path string
	file *os.File

	sessionID      string
	version        int
	nextSeq        uint64
	leafID         string
	pendingAttempt string
	poisoned       bool
}

func Create(path, sessionID string, createdAt time.Time) (*File, error) {
	if path == "" || sessionID == "" || createdAt.IsZero() {
		return nil, fmt.Errorf("create jsonl session: path, session id, and created time are required")
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create jsonl session: %w", err)
	}
	journal := &File{path: path, file: file, sessionID: sessionID, version: Version, nextSeq: 1}
	if err := journal.writeRecord(headerRecord{Type: "session", Version: Version, SessionID: sessionID, CreatedAt: createdAt}); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync jsonl session header: %w", err)
	}
	return journal, nil
}

// Open 重放完整 Journal，恢复 Session 消息树、活动叶节点和未闭合 Run marker。
// 只有“最后一条没有 newline 且 JSON 不完整”的 crash tail 会被自动截断；
// 中间坏行或完整但非法的最后一行都会报错，避免静默伪造 Conversation History。
func Open(path string) (*File, session.State, error) {
	if path == "" {
		return nil, session.State{}, fmt.Errorf("open jsonl session: path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, session.State{}, fmt.Errorf("read jsonl session: %w", err)
	}

	parsed, err := parseFile(data)
	if err != nil {
		return nil, session.State{}, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, session.State{}, fmt.Errorf("open jsonl session for append: %w", err)
	}
	if parsed.validBytes < int64(len(data)) {
		if err := file.Truncate(parsed.validBytes); err != nil {
			_ = file.Close()
			return nil, session.State{}, fmt.Errorf("truncate incomplete jsonl tail: %w", err)
		}
	}
	if parsed.needsTrailingNewline {
		if _, err := file.WriteString("\n"); err != nil {
			_ = file.Close()
			return nil, session.State{}, fmt.Errorf("repair jsonl trailing newline: %w", err)
		}
	}

	journal := &File{
		path:      path,
		file:      file,
		sessionID: parsed.header.SessionID,
		version:   parsed.version,
		nextSeq:   parsed.nextSeq,
		leafID:    parsed.leafID,
	}
	if parsed.pending != nil {
		journal.pendingAttempt = parsed.pending.AttemptID
	}
	state := session.State{
		ID:        parsed.header.SessionID,
		CreatedAt: parsed.header.CreatedAt,
		Entries:   parsed.entries,
		LeafID:    parsed.leafID,
		Pending:   parsed.pending,
	}
	if history, historyErr := session.RestoreHistory(state.Entries, state.LeafID); historyErr == nil {
		state.Messages, _ = history.Messages()
	}
	if err := state.Validate(); err != nil {
		_ = file.Close()
		return nil, session.State{}, fmt.Errorf("invalid restored session state: %w", err)
	}
	return journal, state, nil
}

func (f *File) Path() string {
	if f == nil {
		return ""
	}
	return f.path
}

// AppendMessages 保留线性历史的便利 API。
// 新代码中的 Session 会显式构造 parent-aware Entry；独立调用者如果只想顺序追加消息，
// 可以继续使用这个方法，它会把每条消息接到当前 leaf 后面。
func (f *File) AppendMessages(ctx context.Context, messages ...po.Message) error {
	if len(messages) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureWritableV2Locked(); err != nil {
		return err
	}

	parentID := f.leafID
	for _, message := range messages {
		entry := session.Entry{ID: message.MessageID(), ParentID: parentID, Timestamp: time.Now().UTC(), Message: message}
		if err := entry.Validate(); err != nil {
			return err
		}
		payload, err := po.MarshalMessage(message)
		if err != nil {
			return fmt.Errorf("marshal session message: %w", err)
		}
		record := messageRecord{
			Type: "message", Sequence: f.nextSeq, Timestamp: entry.Timestamp,
			ID: entry.ID, ParentID: entry.ParentID, Message: payload,
		}
		if err := f.writeRecord(record); err != nil {
			return err
		}
		f.nextSeq++
		parentID = entry.ID
		f.leafID = entry.ID
	}
	return f.syncLocked()
}

func (f *File) AppendEntries(ctx context.Context, entries ...session.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureWritableV2Locked(); err != nil {
		return err
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := entry.Validate(); err != nil {
			return err
		}
		payload, err := po.MarshalMessage(entry.Message)
		if err != nil {
			return fmt.Errorf("marshal session message: %w", err)
		}
		record := messageRecord{
			Type: "message", Sequence: f.nextSeq, Timestamp: entry.Timestamp,
			ID: entry.ID, ParentID: entry.ParentID, Message: payload,
		}
		if err := f.writeRecord(record); err != nil {
			return err
		}
		f.nextSeq++
		f.leafID = entry.ID
	}
	return f.syncLocked()
}

func (f *File) SetHead(ctx context.Context, leafID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureWritableV2Locked(); err != nil {
		return err
	}
	if err := f.writeRecord(headRecord{Type: "head", Sequence: f.nextSeq, Timestamp: time.Now().UTC(), LeafID: leafID}); err != nil {
		return err
	}
	f.nextSeq++
	f.leafID = leafID
	return f.syncLocked()
}

func (f *File) BeginRun(ctx context.Context, pending session.PendingRun) error {
	if err := pending.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureWritableV2Locked(); err != nil {
		return err
	}
	if f.pendingAttempt != "" {
		return fmt.Errorf("cannot begin run %q while attempt %q is unresolved", pending.AttemptID, f.pendingAttempt)
	}
	record := runStartRecord{
		Type: "run_start", Sequence: f.nextSeq, Timestamp: pending.StartedAt,
		AttemptID: pending.AttemptID, UserMessageID: pending.UserMessageID,
	}

	if err := f.writeRecord(record); err != nil {
		return err
	}
	f.nextSeq++
	if err := f.syncLocked(); err != nil {
		return err
	}
	f.pendingAttempt = pending.AttemptID
	return nil
}

func (f *File) EndRun(ctx context.Context, attemptID, runID string, runErr error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attemptID == "" {
		return fmt.Errorf("run end attempt id is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureWritableV2Locked(); err != nil {
		return err
	}
	if f.pendingAttempt == "" {
		return fmt.Errorf("cannot end run %q: no run is pending", attemptID)
	}
	if f.pendingAttempt != attemptID {
		return fmt.Errorf("cannot end run %q: pending attempt is %q", attemptID, f.pendingAttempt)
	}
	record := runEndRecord{Type: "run_end", Sequence: f.nextSeq, Timestamp: time.Now().UTC(), AttemptID: attemptID, RunID: runID}
	if runErr != nil {
		record.Error = runErr.Error()
	}
	if err := f.writeRecord(record); err != nil {
		return err
	}
	f.nextSeq++
	if err := f.syncLocked(); err != nil {
		return err
	}
	f.pendingAttempt = ""
	return nil
}

func (f *File) ResolveRun(ctx context.Context, attemptID, note string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attemptID == "" {
		return fmt.Errorf("resolved run attempt id is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureWritableV2Locked(); err != nil {
		return err
	}
	if f.pendingAttempt == "" {
		return fmt.Errorf("cannot resolve run %q: no run is pending", attemptID)
	}
	if f.pendingAttempt != attemptID {
		return fmt.Errorf("cannot resolve run %q: pending attempt is %q", attemptID, f.pendingAttempt)
	}
	record := runResolvedRecord{Type: "run_resolved", Sequence: f.nextSeq, Timestamp: time.Now().UTC(), AttemptID: attemptID, Note: note}
	if err := f.writeRecord(record); err != nil {
		return err
	}
	f.nextSeq++
	if err := f.syncLocked(); err != nil {
		return err
	}
	f.pendingAttempt = ""
	return nil
}

func (f *File) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

// ensureWritableV2Locked 用一条 append-only upgrade record 迁移 v1，而不是重写旧文件。
func (f *File) ensureWritableV2Locked() error {
	if f.file == nil {
		return ErrClosed
	}
	if f.poisoned {
		return ErrUncertainJournal
	}
	if f.version == Version {
		return nil
	}
	if f.version != legacyVersion {
		return ErrUnsupportedVersion
	}

	if err := f.writeRecord(upgradeRecord{Type: "format_upgrade", Sequence: f.nextSeq, Version: Version}); err != nil {
		return err
	}
	f.nextSeq++
	if err := f.syncLocked(); err != nil {
		return err
	}
	f.version = Version
	return nil
}

func (f *File) syncLocked() error {
	if f.file == nil {
		return ErrClosed
	}
	if f.poisoned {
		return ErrUncertainJournal
	}
	if err := f.file.Sync(); err != nil {
		// fsync 失败后的 durability 状态无法由当前进程可靠判断。禁止继续 append，
		// 否则后续 record 可能建立在一个“到底 durable 没有”的尾部之上。
		f.poisoned = true
		return fmt.Errorf("sync jsonl session: %w", err)
	}
	return nil
}

func (f *File) writeRecord(value any) error {
	if f.file == nil {
		return ErrClosed
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode jsonl session record: %w", err)
	}
	if len(encoded) > maxRecordBytes {
		return ErrRecordTooLarge
	}
	encoded = append(encoded, '\n')
	if err := writeAll(f.file, encoded); err != nil {
		// Write 可能已经写出 record 的一部分。继续在同一个 handle 上 append 会把
		// 一个可修复的 crash tail 变成“中间坏行 + 后续合法行”，因此直接 poison。
		f.poisoned = true
		return err
	}
	return nil
}

type parsedFile struct {
	header               headerRecord
	version              int
	entries              []session.Entry
	leafID               string
	pending              *session.PendingRun
	nextSeq              uint64
	validBytes           int64
	needsTrailingNewline bool
}

func parseFile(data []byte) (parsedFile, error) {
	lines, validBytes, trailingNewline, err := splitValidLines(data)
	if err != nil {
		return parsedFile{}, err
	}
	if len(lines) == 0 {
		return parsedFile{}, fmt.Errorf("%w: missing session header", ErrInvalidFile)
	}

	var header headerRecord
	if err := json.Unmarshal(lines[0], &header); err != nil {
		return parsedFile{}, fmt.Errorf("%w: invalid header: %v", ErrInvalidFile, err)
	}
	if header.Type != "session" || header.SessionID == "" || header.CreatedAt.IsZero() {
		return parsedFile{}, fmt.Errorf("%w: invalid session header", ErrInvalidFile)
	}
	if header.Version != legacyVersion && header.Version != Version {
		return parsedFile{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, header.Version)
	}

	version := header.Version
	entries := make([]session.Entry, 0, len(lines)-1)
	leafID := ""
	var pending *session.PendingRun
	var maxSeq uint64

	for lineIndex, line := range lines[1:] {
		var envelope recordEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			return parsedFile{}, fmt.Errorf("%w: line %d: %v", ErrInvalidFile, lineIndex+2, err)
		}
		if envelope.Sequence == 0 || envelope.Sequence <= maxSeq {
			return parsedFile{}, fmt.Errorf("%w: line %d has invalid sequence %d", ErrInvalidFile, lineIndex+2, envelope.Sequence)
		}
		maxSeq = envelope.Sequence

		switch envelope.Type {
		case "format_upgrade":
			var record upgradeRecord
			if err := json.Unmarshal(line, &record); err != nil || record.Version != Version {
				return parsedFile{}, fmt.Errorf("%w: invalid format upgrade", ErrInvalidFile)
			}
			version = Version

		case "message":
			var record messageRecord
			if err := json.Unmarshal(line, &record); err != nil {
				return parsedFile{}, err
			}
			message, err := po.UnmarshalMessage(record.Message)
			if err != nil {
				return parsedFile{}, fmt.Errorf("%w: line %d message: %v", ErrInvalidFile, lineIndex+2, err)
			}

			id := record.ID
			parentID := record.ParentID
			if version == legacyVersion || id == "" {
				// v1 没有 tree fields，按物理顺序迁成一条 chain。
				id = message.MessageID()
				parentID = leafID
			}
			entry := session.Entry{ID: id, ParentID: parentID, Timestamp: record.Timestamp, Message: message}
			if entry.Timestamp.IsZero() {
				entry.Timestamp = header.CreatedAt
			}
			entries = append(entries, entry)
			leafID = entry.ID

		case "head":
			if version != Version {
				return parsedFile{}, fmt.Errorf("%w: head record before v2 upgrade", ErrInvalidFile)
			}
			var record headRecord
			if err := json.Unmarshal(line, &record); err != nil {
				return parsedFile{}, err
			}
			leafID = record.LeafID

		case "run_start":
			if pending != nil {
				return parsedFile{}, fmt.Errorf("%w: new run_start while attempt %q is unresolved", ErrInvalidFile, pending.AttemptID)
			}
			if version != Version {
				return parsedFile{}, fmt.Errorf("%w: run marker before v2 upgrade", ErrInvalidFile)
			}
			var record runStartRecord
			if err := json.Unmarshal(line, &record); err != nil {
				return parsedFile{}, err
			}
			candidate := session.PendingRun{AttemptID: record.AttemptID, UserMessageID: record.UserMessageID, StartedAt: record.Timestamp}
			if err := candidate.Validate(); err != nil {
				return parsedFile{}, err
			}
			pending = &candidate

		case "run_end":
			var record runEndRecord
			if err := json.Unmarshal(line, &record); err != nil {
				return parsedFile{}, err
			}
			if pending == nil || pending.AttemptID != record.AttemptID {
				return parsedFile{}, fmt.Errorf("%w: run_end %q has no matching run_start", ErrInvalidFile, record.AttemptID)
			}
			pending = nil

		case "run_resolved":
			var record runResolvedRecord
			if err := json.Unmarshal(line, &record); err != nil {
				return parsedFile{}, err
			}
			if pending == nil || pending.AttemptID != record.AttemptID {
				return parsedFile{}, fmt.Errorf("%w: run_resolved %q has no matching run_start", ErrInvalidFile, record.AttemptID)
			}
			pending = nil

		default:
			return parsedFile{}, fmt.Errorf("%w: line %d unknown record type %q", ErrInvalidFile, lineIndex+2, envelope.Type)
		}
	}

	history, err := session.RestoreHistory(entries, leafID)
	if err != nil {
		return parsedFile{}, fmt.Errorf("%w: rebuild tree: %v", ErrInvalidFile, err)
	}
	_ = history

	return parsedFile{
		header: header, version: version, entries: entries, leafID: leafID, pending: pending,
		nextSeq: maxSeq + 1, validBytes: validBytes,
		needsTrailingNewline: len(data) > 0 && validBytes == int64(len(data)) && !trailingNewline,
	}, nil
}

// splitValidLines 返回已经确认完整的 JSON records。
// 最后一段没有 newline 且 JSON 不完整时只把它视为 crash tail；任何完整物理行损坏都报错。
func splitValidLines(data []byte) (lines [][]byte, validBytes int64, trailingNewline bool, err error) {
	if len(data) == 0 {
		return nil, 0, false, nil
	}
	start := 0
	for start < len(data) {
		relative := bytes.IndexByte(data[start:], '\n')
		if relative < 0 {
			break
		}
		end := start + relative
		line := bytes.TrimSpace(data[start:end])
		if len(line) > maxRecordBytes {
			return nil, 0, false, ErrRecordTooLarge
		}
		if len(line) == 0 || !json.Valid(line) {
			return nil, 0, false, fmt.Errorf("%w: malformed complete jsonl line", ErrInvalidFile)
		}
		lines = append(lines, bytes.Clone(line))
		start = end + 1
		validBytes = int64(start)
	}

	if start == len(data) {
		return lines, validBytes, true, nil
	}
	final := bytes.TrimSpace(data[start:])
	if len(final) > maxRecordBytes {
		return nil, 0, false, ErrRecordTooLarge
	}
	if len(final) == 0 {
		return lines, int64(len(data)), false, nil
	}
	if !json.Valid(final) {
		// 唯一允许自动恢复的情况：物理文件尾部没有 newline，而且 JSON 自己不完整。
		return lines, int64(start), false, nil
	}
	lines = append(lines, bytes.Clone(final))
	return lines, int64(len(data)), false, nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

var _ session.Journal = (*File)(nil)
