// Package session 提供 Po Agent 之上的会话状态层。
//
// package po 仍然只负责一次 Run 的执行机制；Session 负责把多个 Run 串成一段可恢复的
// Conversation，并把 Transcript 交给可选 Journal 持久化。
package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	po "github.com/lemonzjj/po-agent-go"
)

var (
	// ErrSessionBusy 表示同一个 Session 已经有一个 Prompt 正在运行。
	// 一个 Session 的 Transcript 必须有单一提交顺序。
	// 为避免两个 Run 同时基于同一份历史提交后产生歧义，这一层明确要求“一次只推进一个 Run”。
	ErrSessionBusy = errors.New("session already has an active run")

	// ErrInvalidSession 表示 Session ID、时间或 Transcript 不满足基本不变量。
	ErrInvalidSession = errors.New("invalid session")

	// ErrRecoveryRequired 表示上一次 Run 没有可靠闭合，必须先显式处理恢复状态。
	ErrRecoveryRequired = errors.New("session recovery requires explicit resolution")
)

// PendingRun 表示 Journal 中已经写入 run_start、但还没有对应 run_end 的执行尝试。
// 这不等于“Tool 一定执行过”；它只说明进程可能在一个无法证明副作用状态的窗口中退出。
type PendingRun struct {
	AttemptID     string
	UserMessageID string
	StartedAt     time.Time
}

func (p PendingRun) Validate() error {
	if p.AttemptID == "" || p.UserMessageID == "" || p.StartedAt.IsZero() {
		return fmt.Errorf("%w: invalid pending run marker", ErrInvalidSession)
	}
	return nil
}

// Journal 是 Session 的最小持久化能力。
// Session 不知道底层是 JSONL、SQLite 还是远程存储；它只要求“把一批已经提交的消息
// 按顺序追加进去”。具体读取/创建由各 Backend 自己负责。
type Journal interface {
	AppendEntries(ctx context.Context, entries ...Entry) error
	SetHead(ctx context.Context, leafID string) error
	BeginRun(ctx context.Context, pending PendingRun) error
	EndRun(ctx context.Context, attemptID, runID string, runErr error) error
	ResolveRun(ctx context.Context, attemptID, note string) error
}

// State 是可以从持久化层恢复 Session 的状态。
type State struct {
	ID        string
	CreatedAt time.Time
	Messages  []po.Message

	Entries []Entry
	LeafID  string
	Pending *PendingRun
}

// Clone 防止调用方修改 State 顶层 Slice。
func (s State) Clone() State {
	clone := s
	clone.Messages = append([]po.Message(nil), s.Messages...)
	clone.Entries = append([]Entry(nil), s.Entries...)
	if s.Pending != nil {
		pending := *s.Pending
		clone.Pending = &pending
	}
	return clone
}

func (s State) Validate() error {
	_, err := s.restoreHistory()
	return err
}

func (s State) restoreHistory() (*History, error) {
	if s.ID == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidSession)
	}
	if s.CreatedAt.IsZero() {
		return nil, fmt.Errorf("%w: created time is required", ErrInvalidSession)
	}
	if s.Pending != nil {
		if err := s.Pending.Validate(); err != nil {
			return nil, err
		}
	}

	if len(s.Entries) > 0 {
		return RestoreHistory(s.Entries, s.LeafID)
	}
	return LinearHistory(s.Messages, s.CreatedAt)
}

// Transcript 表示 Session 中已经提交的逻辑事实消息。
// 发送给模型前，从完整 Transcript 构造一个受Token Budget 约束的 Context。这里先保留完整逻辑历史。
type Transcript struct {
	messages []po.Message
}

func NewTranscript(messages ...po.Message) (Transcript, error) {
	for index, message := range messages {
		if message == nil {
			return Transcript{}, fmt.Errorf("transcript message %d is nil", index)
		}
		if err := message.Validate(); err != nil {
			return Transcript{}, fmt.Errorf("transcript message %d: %w", index, err)
		}
	}

	return Transcript{messages: append([]po.Message(nil), messages...)}, nil
}

func (t Transcript) Len() int {
	return len(t.messages)
}

func (t Transcript) Messages() []po.Message {
	return append([]po.Message(nil), t.messages...)
}

func (t *Transcript) replace(messages []po.Message) {
	t.messages = append(t.messages[:0], messages...)
}

// Options 保存属于某个 Session 的可选运行策略。
// ContextBuilder 通常是有 checkpoint 的 stateful builder，因此它绑定 Session 而不是全局 Agent。
// 不同 Session 可以共享 Agent，却拥有完全不同的 Context Window 状态。
type Options struct {
	ContextBuilder po.ContextBuilder
}

// Session 把多个 Agent Run 串成一段有稳定 ID 的 Conversation。
// Agent 可以并发服务很多 Session；单个 Session 自己串行推进 Transcript。
type Session struct {
	mu sync.RWMutex

	id             string
	createdAt      time.Time
	history        *History
	transcript     Transcript
	journal        Journal
	contextBuilder po.ContextBuilder
	pending        *PendingRun

	running bool
}

func New(id string, createdAt time.Time, journal Journal) (*Session, error) {
	return NewWithOptions(id, createdAt, journal, Options{})
}

func NewWithOptions(id string, createdAt time.Time, journal Journal, options Options) (*Session, error) {
	state := State{ID: id, CreatedAt: createdAt}
	history, err := state.restoreHistory()
	if err != nil {
		return nil, err
	}

	return &Session{
		id:             id,
		createdAt:      createdAt,
		history:        history,
		journal:        journal,
		contextBuilder: options.ContextBuilder,
	}, nil
}

// Resume 从持久化层已经验证过的 State 恢复 Session。
func Resume(state State, journal Journal) (*Session, error) {
	return ResumeWithOptions(state, journal, Options{})
}

func ResumeWithOptions(state State, journal Journal, options Options) (*Session, error) {
	history, err := state.restoreHistory()
	if err != nil {
		return nil, err
	}

	messages, err := history.Messages()
	if err != nil {
		return nil, err
	}

	var pending *PendingRun
	if state.Pending != nil {
		copy := *state.Pending
		pending = &copy
	}

	return &Session{
		id:             state.ID,
		createdAt:      state.CreatedAt,
		history:        history,
		transcript:     Transcript{messages: messages},
		journal:        journal,
		contextBuilder: options.ContextBuilder,
		pending:        pending,
	}, nil
}

func (s *Session) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

func (s *Session) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state := State{
		ID:        s.id,
		CreatedAt: s.createdAt,
		Messages:  s.transcript.Messages(),
		Entries:   s.history.Entries(),
		LeafID:    s.history.LeafID(),
	}
	if s.pending != nil {
		pending := *s.pending
		state.Pending = &pending
	}
	return state
}

func (s *Session) Transcript() Transcript {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Transcript{messages: s.transcript.Messages()}
}

// BranchTo 把活动叶节点切换到一条历史消息，后续 Prompt 将从那里创建新分支。
// 旧分支不会被删除。
func (s *Session) BranchTo(ctx context.Context, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return ErrSessionBusy
	}

	entry, ok := s.history.byID[messageID]
	if !ok {
		return fmt.Errorf("%w: branch target %q not found", ErrInvalidSession, messageID)
	}
	if assistant, ok := entry.Message.(po.AssistantMessage); ok && len(assistant.ToolCalls()) > 0 {
		return fmt.Errorf("%w: cannot branch directly to assistant message with pending tool calls", ErrInvalidSession)
	}

	if s.journal != nil {
		if err := s.journal.SetHead(ctx, messageID); err != nil {
			return fmt.Errorf("persist session branch head: %w", err)
		}
	}
	if err := s.history.SetLeaf(messageID); err != nil {
		return err
	}
	messages, err := s.history.Messages()
	if err != nil {
		return err
	}
	s.transcript.replace(messages)
	return nil
}

// Recovery 返回上次未闭合 Run 的保守恢复状态。
func (s *Session) Recovery() (PendingRun, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pending == nil {
		return PendingRun{}, false
	}
	return *s.pending, true
}

// ResolveRecovery 表示调用方已经人工/外部检查过未知副作用窗口，并允许 Session 继续。
// Po 不替用户猜“上次 Tool 到底有没有执行”。显式 resolution 是刻意的安全边界。
func (s *Session) ResolveRecovery(ctx context.Context, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrSessionBusy
	}
	if s.pending == nil {
		return nil
	}
	if s.journal != nil {
		if err := s.journal.ResolveRun(ctx, s.pending.AttemptID, note); err != nil {
			return fmt.Errorf("persist recovery resolution: %w", err)
		}
	}
	s.pending = nil
	return nil
}

// Run 是一次“绑定到 Session 的 Agent Run”。
// 它把 po.RunHandle 的实时控制能力保留下来，但 Wait/Done 的完成语义更强：
// 只有 Agent 已结束、Session 内存 Transcript 已更新、Journal 持久化尝试也已经完成后，
// Session Run 才算真正完成。
// 这样 CLI 不会看到 Agent 已结束就立刻启动下一轮，却和上一轮落盘竞争。
type Run struct {
	handle *po.RunHandle
	done   chan struct{}

	mu     sync.Mutex
	result po.RunResult
	err    error
}

func newRun(handle *po.RunHandle) *Run {
	return &Run{
		handle: handle,
		done:   make(chan struct{}),
	}
}

func (r *Run) RunID() string {
	return r.handle.RunID()
}

func (r *Run) Done() <-chan struct{} {
	return r.done
}

func (r *Run) Wait() (po.RunResult, error) {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, r.err
}

func (r *Run) Steer(message po.UserMessage) error {
	return r.handle.Steer(message)
}

func (r *Run) FollowUp(message po.UserMessage) error {
	return r.handle.FollowUp(message)
}

func (r *Run) Abort() {
	r.handle.Abort()
}

func (r *Run) complete(result po.RunResult, err error) {
	r.mu.Lock()
	r.result = result
	r.err = err
	r.mu.Unlock()
	close(r.done)
}

// Prompt 是同步便利入口。需要 Steering / Follow-up 的调用方应该使用 Start。
func (s *Session) Prompt(ctx context.Context, agent *po.Agent, user po.UserMessage) (po.RunResult, error) {
	run, err := s.Start(ctx, agent, user)
	if err != nil {
		return po.RunResult{}, err
	}
	return run.Wait()
}

// Start 把一条新用户消息提交给 Session，并启动一个可控制的 Agent Run。
// 持久化顺序是：
//  1. 用户消息先进入 Journal；
//  2. 基于“旧 Transcript + 用户消息”启动 Agent；
//  3. Agent settle 后，把本次新增 Assistant / ToolResult / Steering / Follow-up 追加到 Journal。
//
// 这样至少保证“用户说过什么”不会因为模型调用前崩溃而消失。真正做到 Tool 副作用之后的逐步恢复
func (s *Session) Start(ctx context.Context, agent *po.Agent, user po.UserMessage) (*Run, error) {
	if agent == nil {
		return nil, fmt.Errorf("session start: agent is nil")
	}
	if err := user.Validate(); err != nil {
		return nil, fmt.Errorf("session start user message: %w", err)
	}

	baseMessages, userEntry, err := s.beginPrompt(user)
	if err != nil {
		return nil, err
	}

	// 自此之后，任何同步失败都必须释放 Session 的 single-writer 标记。
	started := false
	defer func() {
		if !started {
			s.endPrompt()
		}
	}()

	if s.journal != nil {
		if err := s.journal.AppendEntries(ctx, userEntry); err != nil {
			return nil, fmt.Errorf("persist user message: %w", err)
		}
	}
	if err := s.commitEntry(userEntry); err != nil {
		return nil, err
	}

	attempt := PendingRun{
		AttemptID:     "attempt-" + user.MessageID(),
		UserMessageID: user.MessageID(),
		StartedAt:     time.Now().UTC(),
	}
	if s.journal != nil {
		if err := s.journal.BeginRun(ctx, attempt); err != nil {
			return nil, fmt.Errorf("persist run start marker: %w", err)
		}
	}

	s.setPending(&attempt)

	input := append(baseMessages, user)
	handle, err := agent.StartMessagesWithOptions(ctx, input, po.RunOptions{ContextBuilder: s.contextBuilder})
	if err != nil {
		// Agent 根本没有启动成功。只有 run_end marker 也成功落盘后，才能清除 pending；
		// 否则磁盘仍然会在下次 Resume 时看到一个未闭合尝试。
		var markerErr error
		if s.journal != nil {
			markerErr = s.journal.EndRun(context.Background(), attempt.AttemptID, "", err)
		}
		if markerErr == nil {
			s.setPending(nil)
		}
		return nil, errors.Join(fmt.Errorf("start agent from session transcript: %w", err), markerErr)
	}

	sessionRun := newRun(handle)
	started = true

	go func() {
		result, runErr := handle.Wait()
		finalErr := s.commitRun(input, result, runErr, attempt)

		// 先允许下一次 Prompt，再关闭 Session Run 的 Done。
		// 因此调用方观察到 Done 时，Session 已经完全 settle，可以安全开始下一轮。
		s.endPrompt()
		sessionRun.complete(result, finalErr)
	}()

	return sessionRun, nil
}

func (s *Session) commitRun(input []po.Message, result po.RunResult, runErr error, attempt PendingRun) error {
	resultMessages := result.Messages()
	if len(resultMessages) < len(input) {
		invariantErr := fmt.Errorf("agent returned transcript shorter than input: got %d, input %d", len(resultMessages), len(input))
		return errors.Join(runErr, invariantErr)
	}
	newMessages := resultMessages[len(input):]
	entries, err := s.prepareEntries(newMessages)
	if err != nil {
		return errors.Join(runErr, err)
	}

	// 先把完整批次写入持久化 Journal，再推进内存中的活动分支。
	// 如果反过来先修改内存，一旦磁盘写失败，当前进程会看到一条“磁盘上从未提交”的分支；
	// 调用方随后 ResolveRecovery 并继续 Prompt，还可能写出引用不存在父节点的后代节点。
	// Durable-first 让 Session 的内存 head 不会领先于已确认持久化事实。
	var persistErr error
	if s.journal != nil && len(newMessages) > 0 {
		if err := s.journal.AppendEntries(context.Background(), entries...); err != nil {
			persistErr = fmt.Errorf("persist run messages: %w", err)
		}
	}
	if persistErr != nil {
		// run_start 已经 durable，但本次新消息没有全部确认提交，所以保留 pending。
		// 下一次恢复必须显式检查真实副作用状态，不能自动继续或重放 Tool。
		return errors.Join(runErr, persistErr)
	}

	if err := s.commitEntries(entries); err != nil {
		return errors.Join(runErr, fmt.Errorf("commit persisted run messages: %w", err))
	}

	if s.journal != nil {
		if err := s.journal.EndRun(context.Background(), attempt.AttemptID, result.RunID(), runErr); err != nil {
			return errors.Join(runErr, fmt.Errorf("persist run end marker: %w", err))
		}
	}

	s.setPending(nil)

	return runErr
}

func (s *Session) beginPrompt(user po.UserMessage) ([]po.Message, Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil, Entry{}, ErrSessionBusy
	}
	if s.pending != nil {
		return nil, Entry{}, ErrRecoveryRequired
	}
	entry, err := s.history.PrepareAppend(user, time.Now().UTC())
	if err != nil {
		return nil, Entry{}, err
	}
	s.running = true
	return s.transcript.Messages(), entry, nil
}

// prepareEntries 在不修改真实 History 的前提下，为一批连续消息确定稳定 parent chain。
// 这让 commitRun 可以先 durable-write，再一次性推进内存 branch。
func (s *Session) prepareEntries(messages []po.Message) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(messages) == 0 {
		return nil, nil
	}

	parentID := s.history.LeafID()
	seen := make(map[string]struct{}, len(messages))
	entries := make([]Entry, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			return nil, fmt.Errorf("%w: cannot append nil message", ErrInvalidSession)
		}
		entry := Entry{
			ID:        message.MessageID(),
			ParentID:  parentID,
			Timestamp: time.Now().UTC(),
			Message:   message,
		}
		if err := entry.Validate(); err != nil {
			return nil, err
		}
		if s.history.Has(entry.ID) {
			return nil, fmt.Errorf("%w: duplicate message id %q", ErrInvalidSession, entry.ID)
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate message id %q in run result", ErrInvalidSession, entry.ID)
		}
		seen[entry.ID] = struct{}{}
		entries = append(entries, entry)
		parentID = entry.ID
	}
	return entries, nil
}

func (s *Session) commitEntry(entry Entry) error {
	return s.commitEntries([]Entry{entry})
}

func (s *Session) commitEntries(entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, entry := range entries {
		if err := s.history.AppendEntry(entry); err != nil {
			return err
		}
	}
	messages, err := s.history.Messages()
	if err != nil {
		return err
	}
	s.transcript.replace(messages)
	return nil
}

func (s *Session) setPending(pending *PendingRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pending == nil {
		s.pending = nil
		return
	}
	copy := *pending
	s.pending = &copy
}

func (s *Session) endPrompt() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}
