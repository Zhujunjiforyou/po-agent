package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/internal/modelcatalog"
)

var catalogClient = modelcatalog.Client{}

func runConfigShow(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po config show", flag.ContinueOnError)
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
	document, err := appconfig.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runtime, err := document.Resolve(appconfig.Selection{})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	format := "versioned"
	if document.IsLegacy() {
		format = "legacy"
	}
	fmt.Fprintf(stdout, "config: %s\n", path)
	fmt.Fprintf(stdout, "format: %s\n", format)
	providerNames := make([]string, 0, len(document.File.Providers))
	for name := range document.File.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	fmt.Fprintf(stdout, "providers: %s\n", strings.Join(providerNames, ", "))
	fmt.Fprintf(stdout, "provider: %s\n", runtime.Provider)
	fmt.Fprintf(stdout, "endpoint: %s\n", runtime.BaseURL)
	fmt.Fprintf(stdout, "model: %s\n", runtime.Model)
	fmt.Fprintf(stdout, "api_key_env: %s\n", runtime.APIKeyEnv)
	fmt.Fprintf(stdout, "context_window: %d\n", runtime.ContextWindow)
	fmt.Fprintf(stdout, "model_max_output_tokens: %d\n", runtime.ModelMaxOutputTokens)
	fmt.Fprintf(stdout, "max_output_tokens: %d\n", runtime.MaxOutputTokens)
	fmt.Fprintf(
		stdout,
		"capabilities: streaming=%t tools=%t parallel_tool_calls=%t reasoning=%t vision=%t structured_output=%t\n",
		runtime.Streaming,
		runtime.Tools,
		runtime.ParallelToolCalls,
		runtime.Reasoning,
		runtime.Vision,
		runtime.StructuredOutput,
	)
	return 0
}

func runConfigModels(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po config models", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pathFlag := flags.String("config", "", "path to config.json")
	providerFlag := flags.String("provider", "", "configured provider to query")
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
	providerName := strings.TrimSpace(*providerFlag)
	if providerName == "" {
		providerName = document.File.Current.Provider
	}
	provider, ok := document.File.Providers[providerName]
	if !ok {
		fmt.Fprintf(stderr, "provider %q is not configured\n", providerName)
		return 1
	}
	models, err := catalogClient.List(ctx, catalogSource(provider))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "MODEL\tCONTEXT\tDEFAULT OUTPUT\tCURRENT")
	for _, model := range models {
		current := ""
		if providerName == document.File.Current.Provider && model.ID == document.File.Current.Model {
			current = "*"
		}
		_, _ = fmt.Fprintf(
			w,
			"%s\t%s\t%s\t%s\n",
			model.ID,
			optionalInt(model.EffectiveContextWindow()),
			optionalInt(model.DefaultMaxOutputTokens),
			current,
		)
	}
	_ = w.Flush()
	return 0
}

func catalogSource(provider appconfig.Provider) modelcatalog.Source {
	envName := strings.TrimSpace(provider.APIKeyEnv)
	if envName == "" {
		envName = appconfig.DefaultAPIKeyEnv
	}
	return modelcatalog.Source{
		BaseURL: provider.BaseURL,
		APIKey:  strings.TrimSpace(os.Getenv(envName)),
	}
}

func optionalInt(value int) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", value)
}

func runConfigUse(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po config use", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pathFlag := flags.String("config", "", "path to config.json")
	providerFlag := flags.String("provider", "", "configured provider")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: po config use [--config FILE] [--provider NAME] MODEL")
		return 2
	}
	path, err := chooseConfigPath(*pathFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	file, err := appconfig.LoadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	provider := strings.TrimSpace(*providerFlag)
	if provider == "" {
		provider = file.Current.Provider
	}
	if _, ok := file.Providers[provider]; !ok {
		fmt.Fprintf(stderr, "provider %q is not configured\n", provider)
		return 1
	}
	file.Current = appconfig.Selection{Provider: provider, Model: strings.TrimSpace(flags.Arg(0))}
	if err := appconfig.Write(path, file, true); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "current model: %s:%s\n", provider, file.Current.Model)
	return 0
}

func runConfigAddProvider(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po config add-provider", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pathFlag := flags.String("config", "", "path to config.json")
	name := flags.String("name", "", "provider name")
	baseURL := flags.String("base-url", "", "OpenAI-compatible API base URL")
	apiKeyEnv := flags.String("api-key-env", appconfig.DefaultAPIKeyEnv, "environment variable containing the API key")
	force := flags.Bool("force", false, "replace an existing provider")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected argument: %s\n", flags.Arg(0))
		return 2
	}
	if strings.TrimSpace(*name) == "" || strings.TrimSpace(*baseURL) == "" {
		fmt.Fprintln(stderr, "--name and --base-url are required")
		return 2
	}
	path, err := chooseConfigPath(*pathFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	file, err := appconfig.LoadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	providerName := strings.TrimSpace(*name)
	if _, exists := file.Providers[providerName]; exists && !*force {
		fmt.Fprintf(stderr, "provider %q already exists (use --force to replace it)\n", providerName)
		return 1
	}
	file.Providers[providerName] = appconfig.Provider{
		BaseURL:   strings.TrimSpace(*baseURL),
		APIKeyEnv: strings.TrimSpace(*apiKeyEnv),
	}
	if err := appconfig.Write(path, file, true); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "added provider %s\n", providerName)
	return 0
}

func runConfigMigrate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po config migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pathFlag := flags.String("config", "", "path to config.json")
	providerName := flags.String("provider", "default", "provider name in the migrated config")
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
	if !document.IsLegacy() {
		fmt.Fprintln(stdout, "config is already versioned")
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	name := strings.TrimSpace(*providerName)
	legacyProvider := document.File.Providers[document.File.Current.Provider]
	file := appconfig.New(name, legacyProvider.BaseURL, document.File.Current.Model)
	file.Providers[name] = appconfig.Provider{
		BaseURL:   legacyProvider.BaseURL,
		APIKeyEnv: legacyProvider.APIKeyEnv,
	}
	if err := file.Validate(); err != nil {
		fmt.Fprintf(stderr, "validate migrated config: %v\n", err)
		return 1
	}
	backup := path + ".legacy.bak"
	if err := writeMigrationBackup(backup, data); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := appconfig.Write(path, file, true); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "migrated %s\nbackup: %s\n", path, backup)
	fmt.Fprintln(stdout, "legacy runtime tuning remains in the backup; the new config uses built-in defaults")
	return 0
}

func writeMigrationBackup(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create backup %s: %w", path, err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write backup %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync backup %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close backup %s: %w", path, err)
	}
	complete = true
	return nil
}
