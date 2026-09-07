package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/tool/builtin"
)

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pathFlag := flags.String("config", "", "path to config.json")
	modelFlag := flags.String("model", "", "temporarily override the configured model")
	providerFlag := flags.String("provider", "", "configured provider for the model")
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
	document, err := appconfig.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	config, err := document.Resolve(appconfig.Selection{Provider: *providerFlag, Model: *modelFlag})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	apiKey, err := appconfig.ResolveAPIKey(config, os.Getenv)
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
	fmt.Fprintf(stdout, "provider: %s\n", config.Provider)
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
