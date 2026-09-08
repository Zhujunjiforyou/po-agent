package main

import (
	"context"
	"fmt"
	"path/filepath"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/codingprompt"
	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/policy"
	"github.com/lemonzjj/po-agent-go/project"
	"github.com/lemonzjj/po-agent-go/schema/basic"
	"github.com/lemonzjj/po-agent-go/tool/builtin"
	"github.com/lemonzjj/po-agent-go/tool/coding"
	procrun "github.com/lemonzjj/po-agent-go/tool/process"
	"github.com/lemonzjj/po-agent-go/workspace"
)

type runtimeOptions struct {
	Workspace          string
	AllowWrite         bool
	AllowShell         bool
	Approver           policy.Approver
	ShowThinking       bool
	GlobalInstructions string
	Output             modelOutput
}

type appRuntime struct {
	Agent     *po.Agent
	Workspace *workspace.Workspace

	tools          *po.ToolRegistry
	systemPrompt   string
	beforeToolCall po.BeforeToolCallHook
	afterToolCall  po.AfterToolCallHook
	emitDelta      po.DeltaEmitter
}

func buildAppRuntime(config appconfig.Runtime, apiKey string, opts runtimeOptions) (*appRuntime, error) {
	ws, err := workspace.Open(opts.Workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	fail := func(err error) (*appRuntime, error) { _ = ws.Close(); return nil, err }
	if opts.Output == nil {
		return fail(fmt.Errorf("model output is required"))
	}

	registry := po.NewToolRegistry()
	toolkit := coding.NewToolkit(ws)
	tools := toolkit.ReadOnlyTools()
	if opts.AllowWrite {
		tools, err = toolkit.CodingTools()
		if err != nil {
			return fail(err)
		}
	}
	processTools := coding.NewProcessToolkit(ws.Name(), procrun.NewRunner())
	tools = append(tools, processTools.DevelopmentTools()...)
	if opts.AllowShell {
		tools = append(tools, processTools.ShellTool())
	}
	tools = append(tools, builtin.NewCalculator())
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			return fail(fmt.Errorf("register tool: %w", err))
		}
	}

	instructions, err := project.LoadInstructions(project.InstructionOptions{
		WorkspaceRoot: opts.Workspace,
		GlobalFile:    opts.GlobalInstructions,
	})
	if err != nil {
		return fail(err)
	}
	prompt := codingprompt.Build(codingprompt.Options{
		Workspace:    filepath.Clean(opts.Workspace),
		Writable:     opts.AllowWrite,
		ShellEnabled: opts.AllowShell,
		Instructions: instructions,
	})

	approval := policy.NewApproval("interactive-approval", riskyToolReasons(), opts.Approver)
	pipeline := policy.New(approval)

	runtime := &appRuntime{
		Workspace:      ws,
		tools:          registry,
		systemPrompt:   prompt,
		beforeToolCall: pipeline.BeforeToolCall,
		afterToolCall:  pipeline.AfterToolCall,
		emitDelta: func(ctx context.Context, delta po.ModelDelta) error {
			switch delta.Kind {
			case po.ModelDeltaText:
				return opts.Output.WriteModelText(delta.Text)
			case po.ModelDeltaThinking:
				if opts.ShowThinking {
					return opts.Output.WriteThinking(delta.Text)
				}
			}
			return nil
		},
	}
	if err := runtime.SwitchModel(config, apiKey); err != nil {
		return fail(err)
	}
	return runtime, nil
}

// SwitchModel 重建只持有模型状态的 Agent。工作区、工具注册表和审批管线都保持
// 不变，因此交互会话可以在不丢失 Transcript 的情况下切换模型。调用方必须
// 保证当前没有正在执行的 Run。
func (r *appRuntime) SwitchModel(config appconfig.Runtime, apiKey string) error {
	agent, err := r.prepareAgent(config, apiKey)
	if err != nil {
		return err
	}
	r.Agent = agent
	return nil
}

func (r *appRuntime) prepareAgent(config appconfig.Runtime, apiKey string) (*po.Agent, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	model, err := buildModel(config, apiKey)
	if err != nil {
		return nil, err
	}
	tools := r.tools
	if !config.Tools {
		tools = po.NewToolRegistry()
	}
	agent, err := po.NewAgent(po.AgentConfig{
		Model:            model,
		Tools:            tools,
		Validator:        basic.New(),
		SystemPrompt:     r.systemPrompt,
		MaxOutputTokens:  config.MaxOutputTokens,
		ResourceResolver: appResourceClaims,
		BeforeToolCall:   r.beforeToolCall,
		AfterToolCall:    r.afterToolCall,
		EmitModelDelta:   r.emitDelta,
	})
	if err != nil {
		return nil, err
	}
	return agent, nil
}

func appResourceClaims(call po.ToolCall) ([]po.ResourceClaim, error) {
	if call.Name == "calculator" {
		return nil, nil
	}
	return coding.ResolveResources(call)
}

func riskyToolReasons() map[string]string {
	return map[string]string{
		"edit":  "modify an existing workspace file",
		"git":   "run Git, which may execute project-configured programs",
		"go":    "run Go, which may execute project code or change Go configuration",
		"shell": "run an unrestricted shell command",
		"write": "create or replace a workspace file",
	}
}

func (r *appRuntime) Close() error {
	if r == nil || r.Workspace == nil {
		return nil
	}
	return r.Workspace.Close()
}
