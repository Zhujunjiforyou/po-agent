package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/retry"
	"github.com/lemonzjj/po-agent-go/policy"
	"github.com/lemonzjj/po-agent-go/project"
	openai "github.com/lemonzjj/po-agent-go/provider/openai"
)

// version 会在发布构建中通过以下命令替换：
//
//	go build -ldflags "-X main.version=v0.1.0" ./cmd/po
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "version":
			fmt.Fprintf(stdout, "po %s\n", version)
			return 0
		case "config":
			return runConfig(args[1:], stdout, stderr)
		case "doctor":
			return runDoctor(ctx, args[1:], stdout, stderr)
		case "trust":
			return runTrust(args[1:], stdout, stderr)
		case "session":
			return runSession(ctx, args[1:], stdout, stderr)
		}
	}

	flags := flag.NewFlagSet("po", flag.ContinueOnError)
	flags.SetOutput(stderr)
	prompt := flags.String("p", "", "run one prompt and exit; omit for interactive REPL")
	configPath := flags.String("config", "", "path to global config.json")
	baseURL := flags.String("base-url", "", "temporarily override configured base URL")
	modelID := flags.String("model", "", "temporarily override configured model")
	workspacePath := flags.String("workspace", ".", "workspace root for coding tools")
	allowWrite := flags.Bool(
		"allow-write", false,
		"enable edit/write tools; each use still requires approval unless --yes",
	)
	allowShell := flags.Bool(
		"allow-shell", false,
		"enable unrestricted shell tool; each use still requires approval unless --yes",
	)
	yes := flags.Bool("yes", false, "auto-approve every enabled risky tool for this run")
	showThinking := flags.Bool("show-thinking", false, "stream reasoning content to stderr")
	noTools := flags.Bool("no-tools", false, "disable tools for this run")
	continueSession := flags.Bool("continue", false, "resume the most recent interactive session")
	sessionPath := flags.String("session", "", "resume a specific session JSONL file")
	trustProject := flags.Bool("trust-project", false, "trust project-local .po/config.json for this run")
	showVersion := flags.Bool("version", false, "show version")
	flags.Usage = func() { printUsage(stderr) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "po %s\n", version)
		return 0
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected argument: %s\n", flags.Arg(0))
		return 2
	}

	path, err := chooseConfigPath(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	config, err := loadAppConfig(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	trustPath, err := defaultTrustPath()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	trustStore, err := project.OpenTrustStore(trustPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	trusted := *trustProject || trustStore.Decision(*workspacePath) == project.TrustAlways
	localConfigPath := filepath.Join(*workspacePath, ".po", "config.json")
	if trusted {
		if local, err := loadProjectConfig(localConfigPath); err == nil {
			config = applyProjectConfig(config, local)
		} else if !os.IsNotExist(err) {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	if *baseURL != "" {
		config.BaseURL = *baseURL
	}
	if *modelID != "" {
		config.Model = *modelID
	}
	if *noTools {
		config.Tools = false
		config.ParallelToolCalls = false
	}
	if err := config.validate(); err != nil {
		fmt.Fprintf(stderr, "config: %v\n", err)
		return 1
	}
	apiKey, err := resolveAPIKey(config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	globalInstructions, _ := defaultGlobalInstructions()
	text := strings.TrimSpace(*prompt)
	var interactiveApproval *interactiveApprover
	var approver policy.Approver
	var console replConsole
	var output modelOutput
	var printOutput *plainModelOutput
	if text == "" {
		interactiveApproval = newInteractiveApprover()
		approver = interactiveApproval
		console = newREPLConsole(stdin, stdout, stderr)
		output = console
	} else {
		approver = staticApprover(*yes)
		printOutput = newPlainModelOutput(stdout, stderr)
		output = printOutput
	}

	runtime, err := buildAppRuntime(config, apiKey, runtimeOptions{
		Workspace:          *workspacePath,
		AllowWrite:         *allowWrite,
		AllowShell:         *allowShell,
		Approver:           approver,
		ShowThinking:       *showThinking,
		GlobalInstructions: globalInstructions,
		Output:             output,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer runtime.Close()

	if text != "" {
		return runPrint(ctx, runtime.Agent, printOutput, text, stderr)
	}

	var opened *openedSession
	switch {
	case strings.TrimSpace(*sessionPath) != "":
		opened, err = openSession(*sessionPath)
	case *continueSession:
		var latest string
		latest, err = latestSessionPath()
		if err == nil && latest != "" {
			opened, err = openSession(latest)
		} else if err == nil {
			opened, err = createSession()
		}
	default:
		opened, err = createSession()
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer opened.Close()
	if pending, ok := opened.Session.Recovery(); ok {
		fmt.Fprintf(
			stderr,
			"session has an unfinished prior run %s; inspect possible side effects, then run po session recover --file %q --note %q\n",
			pending.AttemptID,
			opened.Path,
			"describe what you verified",
		)
		return 1
	}
	return runREPL(ctx, runtime.Agent, opened, interactiveApproval, console)
}

func runPrint(ctx context.Context, agent *po.Agent, output *plainModelOutput, text string, stderr io.Writer) int {
	user, err := po.NewUserTextMessage("user-cli", text)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result, runErr := agent.Run(ctx, user)
	if runErr == nil && !output.HasModelText() && result.FinalText() != "" {
		if err := output.WriteModelText(result.FinalText()); err != nil {
			fmt.Fprintf(stderr, "po: write output: %v\n", err)
			return 1
		}
	}
	finishErr := output.FinishModelMessage()
	if runErr != nil {
		fmt.Fprintf(stderr, "po: %v\n", runErr)
		return 1
	}
	if finishErr != nil {
		fmt.Fprintf(stderr, "po: finish output: %v\n", finishErr)
		return 1
	}
	return 0
}

func buildModel(config appConfig, apiKey string) (po.Model, error) {
	base, err := openai.New(config.providerConfig(apiKey))
	if err != nil {
		return nil, fmt.Errorf("create provider model: %w", err)
	}
	return retry.New(base, retry.DefaultPolicy(), nil)
}
func chooseConfigPath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	return defaultConfigPath()
}
func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Po Agent Go")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  po                         # interactive session")
	fmt.Fprintln(w, "  po --continue              # resume latest session")
	fmt.Fprintln(w, "  po --session FILE          # resume a session")
	fmt.Fprintln(w, "  po -p \"your prompt\"        # one-shot")
	fmt.Fprintln(w, "  po --allow-write --allow-shell")
	fmt.Fprintln(w, "  po doctor")
	fmt.Fprintln(w, "  po trust allow --workspace .")
	fmt.Fprintln(w, "  po session inspect --file FILE")
	fmt.Fprintln(w, "  po session recover --file FILE --note NOTE")
	fmt.Fprintln(w, "  po config init --profile qwen3.6-27b --base-url URL --model MODEL")
}
