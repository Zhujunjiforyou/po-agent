package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lemonzjj/po-agent-go/project"
)

func defaultTrustPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "po", "trust.json"), nil
}
func defaultGlobalInstructions() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "po", "AGENTS.md"), nil
}

func runTrust(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: po trust [allow|deny|status] [--workspace DIR]")
		return 2
	}
	action := args[0]

	flags := flag.NewFlagSet("trust", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", ".", "project workspace")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: po trust [allow|deny|status] [--workspace DIR]")
		return 2
	}
	path, err := defaultTrustPath()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store, err := project.OpenTrustStore(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch action {
	case "allow":
		err = store.Set(*workspace, project.TrustAlways)
	case "deny":
		err = store.Set(*workspace, project.TrustNever)
	case "status":
		fmt.Fprintln(stdout, store.Decision(*workspace))
		return 0
	default:
		fmt.Fprintln(stderr, "unknown trust action")
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}
