package coding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
)

var (
	// ErrFileChanged 表示模型的修改建立在过期文件版本上。
	// 这是正常的 optimistic-concurrency conflict，不应该“反正再覆盖一次”。
	ErrFileChanged = errors.New("file changed since it was read")

	// ErrVersionRequired 阻止模型在不知道当前版本的情况下整文件覆盖已有文件。
	// 调用方必须先 read，并证明自己准备替换的是哪个具体版本。
	ErrVersionRequired = errors.New("existing file requires expected_sha256")
)

type mutationEntry struct {
	token chan struct{}
	refs  int
}

// mutationQueue 只串行化“同一路径”的修改；不同文件仍可并发。
//
// 它解决的是 Po 进程内多个 ToolCall 的竞争，不是跨进程文件锁。
type mutationQueue struct {
	mu      sync.Mutex
	entries map[string]*mutationEntry
}

func newMutationQueue() *mutationQueue {
	return &mutationQueue{entries: make(map[string]*mutationEntry)}
}

// acquire 等待某个路径的 mutation token，同时必须响应 Run Context。
// refs 同时统计当前 holder 与等待者，因此最后一个引用离开时才能安全移除 entry，
// 避免同一路径短暂出现两把彼此不知道的锁。
func (q *mutationQueue) acquire(ctx context.Context, path string) (func(), error) {
	q.mu.Lock()
	entry := q.entries[path]
	if entry == nil {
		entry = &mutationEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		q.entries[path] = entry
	}
	entry.refs++
	q.mu.Unlock()

	select {
	case <-ctx.Done():
		q.releaseRef(path, entry)
		return nil, contextCause(ctx)
	case <-entry.token:
	}

	// 如果 Context 取消和拿到 token 恰好竞态，不能再进入修改临界区；先把 token
	// 还回去，再返回真正的 cancel cause，避免后续 waiter 永久被卡住。
	if err := ctx.Err(); err != nil {
		entry.token <- struct{}{}
		q.releaseRef(path, entry)
		return nil, contextCause(ctx)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.token <- struct{}{}
			q.releaseRef(path, entry)
		})
	}, nil
}

func (q *mutationQueue) releaseRef(path string, entry *mutationEntry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry.refs--
	if entry.refs == 0 && q.entries[path] == entry {
		delete(q.entries, path)
	}
}

func contextCause(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

type replaceCondition struct {
	expectedSHA256 string
	requireAbsent  bool
}

func atomicReplace(
	ctx context.Context,
	files WritableFileSystem,
	name string,
	data []byte,
	perm fs.FileMode,
	condition replaceCondition,
) error {
	if err := ctx.Err(); err != nil {
		return contextCause(ctx)
	}
	tempName, err := tempSibling(name)
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = files.Remove(tempName)
		}
	}()

	if err := files.WriteFile(tempName, data, perm.Perm()); err != nil {
		return fmt.Errorf("write temporary file %s: %w", displayPath(tempName), err)
	}
	if err := ctx.Err(); err != nil {
		return contextCause(ctx)
	}

	// 在可移植文件接口允许的范围内，把 optimistic precondition 的最终检查尽量靠近
	// Rename 提交点。这样能捕获“计算新内容/写临时文件期间”发生的外部修改。
	// 但它仍不是真正跨进程 CAS：另一个进程仍可能在本次检查之后、Rename 之前竞态。
	if condition.expectedSHA256 != "" {
		current, err := files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("recheck %s before commit: %w", displayPath(name), err)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(current))
		if digest != condition.expectedSHA256 {
			return fmt.Errorf(
				"%w: %s changed again before commit (current sha256=%s)",
				ErrFileChanged,
				displayPath(name),
				digest,
			)
		}
	} else if condition.requireAbsent {
		if _, err := files.Stat(name); err == nil {
			return fmt.Errorf("%w: %s was created by another writer before commit", ErrFileChanged, displayPath(name))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("recheck %s before create: %w", displayPath(name), err)
		}
	}

	// Rename 是本次修改的提交点。Rename 一旦成功，外部世界已经改变，因此这里
	// 故意不再因为“随后 Context 恰好取消”而返回失败，否则调用方会收到错误，
	// 但文件实际上已经变更，制造最危险的 outcome ambiguity。
	if err := files.Rename(tempName, name); err != nil {
		return fmt.Errorf("replace %s: %w", displayPath(name), err)
	}
	removeTemp = false
	return nil
}

func tempSibling(name string) (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create temporary file name: %w", err)
	}
	dir := filepath.Dir(name)
	base := filepath.Base(name)
	temp := "." + base + ".po-" + hex.EncodeToString(random[:]) + ".tmp"
	if dir == "." {
		return temp, nil
	}
	return filepath.Join(dir, temp), nil
}

func normalizeDigest(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.TrimPrefix(value, "sha256:")
}
