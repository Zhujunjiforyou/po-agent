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
	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/policy"
	"github.com/lemonzjj/po-agent-go/project"
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
			return runConfig(ctx, args[1:], stdout, stderr)
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
	var localConfig *appconfig.Project
	if trusted {
		if local, err := appconfig.LoadProject(localConfigPath); err == nil {
			localConfig = &local
		} else if !os.IsNotExist(err) {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	document, err := appconfig.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	selection := appconfig.Selection{Model: strings.TrimSpace(os.Getenv("PO_MODEL"))}
	if localConfig != nil {
		selection = localConfig.Select(selection)
	}
	if value := strings.TrimSpace(*modelID); value != "" {
		selection.Model = value
	}
	config, err := document.Resolve(selection)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	adjustConfig := func(candidate appconfig.Runtime) (appconfig.Runtime, error) {
		if value := strings.TrimSpace(os.Getenv("PO_BASE_URL")); value != "" {
			candidate.BaseURL = value
		}
		if localConfig != nil {
			candidate = localConfig.Apply(candidate)
		}
		if *baseURL != "" {
			candidate.BaseURL = *baseURL
		}
		if *noTools {
			candidate.Tools = false
			candidate.ParallelToolCalls = false
		}
		if err := candidate.Validate(); err != nil {
			return appconfig.Runtime{}, fmt.Errorf("config: %w", err)
		}
		return candidate, nil
	}
	config, err = adjustConfig(config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	apiKey, err := appconfig.ResolveAPIKey(config, os.Getenv)
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
	models := newREPLModelManager(path, config, runtime, adjustConfig)
	return runREPL(ctx, runtime, models, opened, interactiveApproval, console)
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

func chooseConfigPath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	return appconfig.DefaultPath()
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
	fmt.Fprintln(w, "  po config --help")
}
