// Package process 提供 Coding Tool 使用的最小子进程运行时。
//
// 它只负责进程生命周期、输出收集、取消与环境边界；具体哪些命令允许执行，
// 属于 shell/git/go Tool 或更高层 Policy，而不是 Runner 自己的职责。
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const DefaultMaxOutputBytes = 256 * 1024

// Spec 描述一次不经过 shell 解析的进程调用。
// Name 是可执行文件，Args 是 argv，Dir 是工作目录。
type Spec struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

// Result 保存一次真实子进程的最终事实。
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Duration  time.Duration
	Truncated bool
}

// UpdateFunc 接收过程输出片段。它主要服务 Tool progress/UI，不进入最终 Transcript。
type UpdateFunc func(context.Context, string) error

// Runner 是可复用的子进程执行器。
type Runner struct {
	MaxOutputBytes int
}

func NewRunner() *Runner { return &Runner{MaxOutputBytes: DefaultMaxOutputBytes} }

// Run 启动一个进程，并保证 Context 取消时终止整个由它创建的进程组。
func (r *Runner) Run(ctx context.Context, spec Spec, update UpdateFunc) (Result, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return Result{}, fmt.Errorf("process name is required")
	}

	cmd := exec.Command(spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append([]string(nil), spec.Env...)
	configureProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("open stderr pipe: %w", err)
	}

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start %s: %w", spec.Name, err)
	}

	limit := r.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}
	collector := newCollector(limit, update)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		collector.copy(ctx, "stdout", stdout)
	}()
	go func() {
		defer wg.Done()
		collector.copy(ctx, "stderr", stderr)
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-ctx.Done():
		// exec.Cmd 只知道直接 child；Coding Tool 常通过 shell 再派生 test/build 子进程。
		// 因此这里杀的是本 Runner 创建的 process group，而不是只 kill shell 自己。
		_ = killProcessTree(cmd)
		waitErr = <-waitDone
	}
	wg.Wait()

	result := collector.result()
	result.Duration = time.Since(started)
	result.ExitCode = exitCode(waitErr)

	if cause := context.Cause(ctx); cause != nil {
		return result, cause
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			// 非零退出码是进程的业务结果，不是 Runner 自己的基础设施故障。
			return result, nil
		}
		return result, fmt.Errorf("wait %s: %w", spec.Name, waitErr)
	}
	return result, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type collector struct {
	mu        sync.Mutex
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	maxBytes  int
	usedBytes int
	truncated bool
	update    UpdateFunc
}

func newCollector(maxBytes int, update UpdateFunc) *collector {
	return &collector{maxBytes: maxBytes, update: update}
}

func (c *collector) copy(ctx context.Context, stream string, src io.Reader) {
	buf := make([]byte, 8*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			c.append(stream, chunk)
			if c.update != nil {
				_ = c.update(ctx, fmt.Sprintf("[%s] %s", stream, string(chunk)))
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *collector) append(stream string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	remain := c.maxBytes - c.usedBytes
	if remain <= 0 {
		c.truncated = true
		return
	}
	if len(data) > remain {
		data = data[:remain]
		c.truncated = true
	}
	c.usedBytes += len(data)
	if stream == "stderr" {
		_, _ = c.stderr.Write(data)
	} else {
		_, _ = c.stdout.Write(data)
	}
}

func (c *collector) result() Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Result{Stdout: c.stdout.String(), Stderr: c.stderr.String(), Truncated: c.truncated}
}

// SafeEnvironment 继承普通开发环境变量，但主动过滤常见 Secret。
// 这是 defense-in-depth：Shell 仍然不是安全沙箱，真正敏感环境应由产品显式控制。
func SafeEnvironment(base []string, denyNames ...string) []string {
	denied := map[string]struct{}{}
	for _, name := range denyNames {
		denied[strings.ToUpper(strings.TrimSpace(name))] = struct{}{}
	}
	out := make([]string, 0, len(base))
	for _, item := range base {
		name, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(name)
		if _, blocked := denied[upper]; blocked {
			continue
		}
		if looksSecretName(upper) {
			continue
		}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func looksSecretName(name string) bool {
	markers := []string{
		"ACCESS_KEY",
		"API_KEY",
		"APIKEY",
		"AUTH",
		"COOKIE",
		"CREDENTIAL",
		"PASSWORD",
		"PASSWD",
		"PRIVATE_KEY",
		"SECRET",
		"TOKEN",
	}
	for _, marker := range markers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func DefaultSafeEnvironment() []string {
	return SafeEnvironment(
		os.Environ(),
		"AWS_PROFILE",
		"AWS_SHARED_CREDENTIALS_FILE",
		"DOCKER_CONFIG",
		"GIT_ASKPASS",
		"KUBECONFIG",
		"PO_API_KEY",
		"SSH_ASKPASS",
	)
}
