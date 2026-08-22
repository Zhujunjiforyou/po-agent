package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/tool/builtin"
)

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: po config init|path")
		return 2
	}
	switch args[0] {
	case "path":
		path, err := defaultConfigPath()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, path)
		return 0
	case "init":
		flags := flag.NewFlagSet("po config init", flag.ContinueOnError)
		flags.SetOutput(stderr)
		profile := flags.String("profile", "qwen3.6-27b", "configuration profile")
		baseURL := flags.String("base-url", "", "OpenAI-compatible API base URL")
		model := flags.String("model", "", "model identifier")
		path := flags.String("config", "", "output config path")
		force := flags.Bool("force", false, "overwrite existing config")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if flags.NArg() != 0 {
			fmt.Fprintf(stderr, "unexpected argument: %s\n", flags.Arg(0))
			return 2
		}
		if *profile != "qwen3.6-27b" {
			fmt.Fprintf(stderr, "unsupported profile %q\n", *profile)
			return 2
		}
		if strings.TrimSpace(*baseURL) == "" || strings.TrimSpace(*model) == "" {
			fmt.Fprintln(stderr, "--base-url and --model are required")
			return 2
		}
		output, err := chooseConfigPath(*path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := writeConfig(output, qwen36_27BConfig(*baseURL, *model), *force); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s\n", output)
		fmt.Fprintf(stdout, "set %s in your environment before running Po\n", defaultAPIKeyEnv)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown config command: %s\n", args[0])
		return 2
	}
}

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pathFlag := flags.String("config", "", "path to config.json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected argument: %s\n", flags.Arg(0))
		return 2
	}

	path, err := chooseConfigPath(*pathFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	config, err := loadAppConfig(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	apiKey, err := resolveAPIKey(config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	model, err := buildModel(config, apiKey)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintf(stdout, "config: %s\n", path)
	fmt.Fprintf(stdout, "endpoint: %s\n", config.BaseURL)
	fmt.Fprintf(stdout, "model: %s\n", config.Model)

	user, _ := po.NewUserTextMessage("doctor-user", "Reply with exactly PO_OK and nothing else.")
	request, _ := po.NewModelRequest("You are a protocol connectivity test.", []po.Message{user}, nil, 1024)
	response, err := model.Generate(ctx, request, nil)
	if err != nil {
		fmt.Fprintf(stderr, "text probe failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "text: ok (stop=%s)\n", response.StopReason())

	if !config.Tools {
		fmt.Fprintln(stdout, "tools: skipped (disabled by profile)")
		return 0
	}

	calculator := builtin.NewCalculator().Spec()
	toolUser, _ := po.NewUserTextMessage(
		"doctor-tool-user",
		"You must call the calculator tool exactly once to multiply 17 by 19. Do not calculate it yourself.",
	)
	toolRequest, _ := po.NewModelRequest(
		"You are a tool-calling protocol test. Use the provided tool.",
		[]po.Message{toolUser},
		[]po.ToolSpec{calculator},
		2048,
	)
	toolResponse, err := model.Generate(ctx, toolRequest, nil)
	if err != nil {
		fmt.Fprintf(stderr, "tool probe failed: %v\n", err)
		return 1
	}
	calls := toolResponse.Message().ToolCalls()
	if toolResponse.StopReason() != po.ModelStopToolCall || len(calls) == 0 {
		fmt.Fprintf(stdout, "tools: WARNING - endpoint returned no structured tool call (stop=%s)\n", toolResponse.StopReason())
		fmt.Fprintln(stdout, "       the model may support tools while the serving endpoint/tool parser does not")
		return 0
	}
	fmt.Fprintf(stdout, "tools: ok (%s)\n", calls[0].Name)
	return 0
}
