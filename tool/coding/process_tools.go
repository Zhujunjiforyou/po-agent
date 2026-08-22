package coding

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
	procrun "github.com/lemonzjj/po-agent-go/tool/process"
)

type ProcessToolkit struct {
	runner *procrun.Runner
	dir    string
	env    []string
}

const processArgsSchema = `{
	"type":"object",
	"properties":{"args":{"type":"array","items":{"type":"string"},"minItems":1}},
	"required":["args"],
	"additionalProperties":false
}`

func NewProcessToolkit(dir string, runner *procrun.Runner) *ProcessToolkit {
	if runner == nil {
		runner = procrun.NewRunner()
	}
	return &ProcessToolkit{runner: runner, dir: dir, env: procrun.DefaultSafeEnvironment()}
}

// DevelopmentTools 返回基于进程执行的开发工具。这些工具与 ShellTool 分开提供，但它们
// 并不是沙箱：Go 和 Git 都可能执行项目配置的程序。产品应在其外部设置审批策略或操作
// 系统隔离边界。
func (t *ProcessToolkit) DevelopmentTools() []po.Tool {
	return []po.Tool{t.newGitTool(), t.newGoTool()}
}

func (t *ProcessToolkit) ShellTool() po.Tool { return t.newShellTool() }

func (t *ProcessToolkit) run(ctx context.Context, name string, args []string, emit po.ToolUpdateEmitter) (procrun.Result, error) {
	return t.runner.Run(ctx, procrun.Spec{Name: name, Args: args, Dir: t.dir, Env: t.env}, func(ctx context.Context, chunk string) error {
		if strings.TrimSpace(chunk) == "" {
			return nil
		}
		return po.EmitToolUpdate(ctx, emit, po.NewToolUpdate(chunk))
	})
}

func processResult(command string, result procrun.Result) (po.ToolResult, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "$ %s\nexit_code: %d\n", command, result.ExitCode)
	if result.Stdout != "" {
		b.WriteString("\nstdout:\n")
		b.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		b.WriteString("\nstderr:\n")
		b.WriteString(result.Stderr)
	}
	if result.Truncated {
		b.WriteString("\n[output truncated by process runner]\n")
	}
	return po.NewTextToolResult(b.String(), map[string]any{
		"exit_code":   result.ExitCode,
		"duration_ms": result.Duration.Milliseconds(),
		"truncated":   result.Truncated,
	}, false)
}

func (t *ProcessToolkit) newShellTool() po.Tool {
	type args struct {
		Command string `json:"command"`
	}
	spec := mustSpec(
		"shell",
		"Run a shell command in the workspace. This capability may modify files, run programs, "+
			"or access resources outside the workspace according to OS permissions. Use it only "+
			"when dedicated tools are insufficient.",
		`{"type":"object","properties":{"command":{"type":"string","minLength":1}},"required":["command"],"additionalProperties":false}`,
	)
	tool, err := po.NewTypedTool(spec, func(a args) error {
		if strings.TrimSpace(a.Command) == "" {
			return fmt.Errorf("command is required")
		}
		return nil
	}, func(ctx context.Context, a args, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		name, argv := shellCommand(a.Command)
		result, err := t.run(ctx, name, argv, emit)
		if err != nil {
			return po.ToolResult{}, err
		}
		return processResult(a.Command, result)
	})
	if err != nil {
		panic(err)
	}
	return tool
}

func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/C", command}
	}
	return "/bin/sh", []string{"-lc", command}
}

func (t *ProcessToolkit) newGitTool() po.Tool {
	type args struct {
		Args []string `json:"args"`
	}
	spec := mustSpec(
		"git",
		"Run a Git inspection command in the workspace. Allowed subcommands: status, diff, "+
			"log, show, branch (listing only), and rev-parse. Git configuration and command "+
			"flags may invoke external programs, so every call requires approval.",
		processArgsSchema,
	)
	tool, err := po.NewTypedTool(
		spec,
		func(a args) error { return validateGitArgs(a.Args) },
		func(ctx context.Context, a args, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
			result, err := t.run(ctx, "git", a.Args, emit)
			if err != nil {
				return po.ToolResult{}, err
			}
			return processResult("git "+strings.Join(a.Args, " "), result)
		},
	)
	if err != nil {
		panic(err)
	}
	return tool
}

func validateGitArgs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("git args are required")
	}
	allowed := map[string]bool{"status": true, "diff": true, "log": true, "show": true, "rev-parse": true}
	if allowed[args[0]] {
		return nil
	}
	if args[0] == "branch" {
		for _, v := range args[1:] {
			if v == "-d" || v == "-D" || v == "-m" || v == "-M" || v == "--delete" || v == "--move" {
				return fmt.Errorf("write-oriented git branch operation is not allowed")
			}
		}
		return nil
	}
	return fmt.Errorf("git subcommand %q is not in the safe tool allowlist; use shell with explicit approval if truly required", args[0])
}

func (t *ProcessToolkit) newGoTool() po.Tool {
	type args struct {
		Args []string `json:"args"`
	}
	spec := mustSpec(
		"go",
		"Run a Go development command in the workspace. Allowed subcommands: test, vet, list, "+
			"build, env, and version. Go commands may download modules, execute project code, "+
			"or change Go configuration, so every call requires approval.",
		processArgsSchema,
	)
	tool, err := po.NewTypedTool(spec, func(a args) error {
		if len(a.Args) == 0 {
			return fmt.Errorf("go args are required")
		}
		allowed := map[string]bool{"test": true, "vet": true, "list": true, "build": true, "env": true, "version": true}
		if !allowed[a.Args[0]] {
			return fmt.Errorf("go subcommand %q is not allowed by the dedicated tool", a.Args[0])
		}
		return nil
	}, func(ctx context.Context, a args, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		result, err := t.run(ctx, "go", a.Args, emit)
		if err != nil {
			return po.ToolResult{}, err
		}
		return processResult("go "+strings.Join(a.Args, " "), result)
	})
	if err != nil {
		panic(err)
	}
	return tool
}
