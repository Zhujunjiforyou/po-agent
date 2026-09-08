package session

import (
	"fmt"
	"time"

	"github.com/lemonzjj/po-agent-go"
)

// Entry 是 Session 消息树中一个已经提交的节点。
// 只有真正进入 Transcript 的消息会成为树节点，因此消息 ID 也作为唯一的 Entry ID。
type Entry struct {
	ID        string
	ParentID  string
	Timestamp time.Time
	Message   po.Message
}

// Validate 检查节点及其消息是否完整。
func (e Entry) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("%w: entry id is required", ErrInvalidSession)
	}
	if e.Timestamp.IsZero() {
		return fmt.Errorf("%w: entry timestamp is required", ErrInvalidSession)
	}
	if e.Message == nil {
		return fmt.Errorf("%w: entry message is nil", ErrInvalidSession)
	}
	if e.ID != e.Message.MessageID() {
		return fmt.Errorf("%w: entry id %q does not match message id %q", ErrInvalidSession, e.ID, e.Message.MessageID())
	}
	if err := e.Message.Validate(); err != nil {
		return fmt.Errorf("%w: entry message: %v", ErrInvalidSession, err)
	}
	return nil
}

// History 保存一个 Session 文件内的完整消息树和当前活动叶节点。
type History struct {
	entries []Entry
	byID    map[string]Entry
	leafID  string
}

// NewHistory 创建一棵空消息树。
func NewHistory() *History {
	return &History{byID: make(map[string]Entry)}
}

// RestoreHistory 从已有节点和活动叶节点恢复消息树。
func RestoreHistory(entries []Entry, leafID string) (*History, error) {
	history := NewHistory()
	for _, entry := range entries {
		if err := history.AppendEntry(entry); err != nil {
			return nil, err
		}
	}

	if leafID != "" {
		if err := history.SetLeaf(leafID); err != nil {
			return nil, err
		}
	}
	return history, nil
}

// LinearHistory 把线性 Transcript 迁移成一条 parent chain。
func LinearHistory(messages []po.Message, now time.Time) (*History, error) {
	history := NewHistory()
	parentID := ""
	for index, message := range messages {
		if message == nil {
			return nil, fmt.Errorf("%w: message %d is nil", ErrInvalidSession, index)
		}
		entry := Entry{ID: message.MessageID(), ParentID: parentID, Timestamp: now, Message: message}
		if err := history.AppendEntry(entry); err != nil {
			return nil, err
		}
		parentID = entry.ID
	}
	return history, nil
}

// Entries 返回全部节点的切片副本。
func (h *History) Entries() []Entry {
	return append([]Entry(nil), h.entries...)
}

// LeafID 返回当前活动叶节点的 ID。
func (h *History) LeafID() string {
	return h.leafID
}

// Has 判断消息树中是否存在指定节点。
func (h *History) Has(id string) bool {
	_, ok := h.byID[id]
	return ok
}

// PrepareAppend 根据当前叶节点准备一条待提交记录，但不修改消息树。
func (h *History) PrepareAppend(message po.Message, at time.Time) (Entry, error) {
	if message == nil {
		return Entry{}, fmt.Errorf("%w: cannot append nil message", ErrInvalidSession)
	}
	entry := Entry{ID: message.MessageID(), ParentID: h.leafID, Timestamp: at, Message: message}
	if err := entry.Validate(); err != nil {
		return Entry{}, err
	}
	if h.Has(entry.ID) {
		return Entry{}, fmt.Errorf("%w: duplicate message id %q", ErrInvalidSession, entry.ID)
	}
	return entry, nil
}

// AppendEntry 提交一个已经确定 parent 的树节点。
func (h *History) AppendEntry(entry Entry) error {
	if err := entry.Validate(); err != nil {
		return err
	}
	if h.Has(entry.ID) {
		return fmt.Errorf("%w: duplicate entry id %q", ErrInvalidSession, entry.ID)
	}
	if entry.ParentID != "" && !h.Has(entry.ParentID) {
		return fmt.Errorf("%w: parent entry %q does not exist", ErrInvalidSession, entry.ParentID)
	}

	h.entries = append(h.entries, entry)
	h.byID[entry.ID] = entry
	h.leafID = entry.ID
	return nil
}

// SetLeaf 切换活动分支。历史节点不会被删除。
func (h *History) SetLeaf(id string) error {
	if id == "" {
		h.leafID = ""
		return nil
	}
	if !h.Has(id) {
		return fmt.Errorf("%w: leaf entry %q does not exist", ErrInvalidSession, id)
	}
	h.leafID = id
	return nil
}

// Path 返回从根节点到叶节点的稳定活动分支。
func (h *History) Path() ([]Entry, error) {
	if h.leafID == "" {
		return nil, nil
	}

	path := make([]Entry, 0)
	seen := make(map[string]struct{})
	currentID := h.leafID
	for currentID != "" {
		if _, duplicate := seen[currentID]; duplicate {
			return nil, fmt.Errorf("%w: cycle detected at entry %q", ErrInvalidSession, currentID)
		}
		seen[currentID] = struct{}{}

		entry, ok := h.byID[currentID]
		if !ok {
			return nil, fmt.Errorf("%w: missing entry %q while rebuilding branch", ErrInvalidSession, currentID)
		}
		path = append(path, entry)
		currentID = entry.ParentID
	}

	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path, nil
}

// Messages 返回当前活动分支上的消息。
func (h *History) Messages() ([]po.Message, error) {
	path, err := h.Path()
	if err != nil {
		return nil, err
	}
	messages := make([]po.Message, 0, len(path))
	for _, entry := range path {
		messages = append(messages, entry.Message)
	}
	return messages, nil
}
