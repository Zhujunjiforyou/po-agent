package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/lemonzjj/po-agent-go/internal/appconfig"
)

func runConfig(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printConfigUsage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		printConfigUsage(stdout)
		return 0
	case "path":
		path, err := appconfig.DefaultPath()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, path)
		return 0
	case "init":
		return runConfigInit(args[1:], stdout, stderr)
	case "show":
		return runConfigShow(args[1:], stdout, stderr)
	case "models":
		return runConfigModels(ctx, args[1:], stdout, stderr)
	case "use":
		return runConfigUse(args[1:], stdout, stderr)
	case "add-provider":
		return runConfigAddProvider(args[1:], stdout, stderr)
	case "migrate":
		return runConfigMigrate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown config command: %s\n", args[0])
		fmt.Fprintln(stderr, "run 'po config --help' to see available commands")
		return 2
	}
}

func printConfigUsage(w io.Writer) {
	fmt.Fprintln(w, "Po configuration")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  po config init [--provider NAME] --base-url URL --model MODEL")
	fmt.Fprintln(w, "  po config add-provider --name NAME --base-url URL [--api-key-env ENV]")
	fmt.Fprintln(w, "  po config models [--provider NAME]")
	fmt.Fprintln(w, "  po config use [--provider NAME] MODEL")
	fmt.Fprintln(w, "  po config show")
	fmt.Fprintln(w, "  po config path")
	fmt.Fprintln(w, "  po config migrate [--provider NAME]")
}

func runConfigInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po config init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baseURL := flags.String("base-url", "", "OpenAI-compatible API base URL")
	model := flags.String("model", "", "model identifier")
	provider := flags.String("provider", "default", "provider name")
	path := flags.String("config", "", "output config path")
	force := flags.Bool("force", false, "overwrite existing config")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected argument: %s\n", flags.Arg(0))
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
	file := appconfig.New(*provider, *baseURL, *model)
	if err := appconfig.Write(output, file, *force); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s\n", output)
	fmt.Fprintf(stdout, "set %s in your environment before running Po\n", appconfig.DefaultAPIKeyEnv)
	return 0
}
