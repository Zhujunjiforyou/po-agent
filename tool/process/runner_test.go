package process

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestRunnerCapturesExitStatusAndOutput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner()
	result, err := runner.Run(context.Background(), Spec{
		Name: executable,
		Args: []string{"-test.run=TestProcessHelper$"},
		Env:  append(os.Environ(), "PO_PROCESS_HELPER=exit"),
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitCode != 7 || result.Stdout != "stdout" || result.Stderr != "stderr" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunnerTruncatesCollectedOutput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{MaxOutputBytes: 32}
	result, err := runner.Run(context.Background(), Spec{
		Name: executable,
		Args: []string{"-test.run=TestProcessHelper$"},
		Env:  append(os.Environ(), "PO_PROCESS_HELPER=large"),
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Truncated || len(result.Stdout)+len(result.Stderr) != 32 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSafeEnvironmentRemovesCredentialLikeVariables(t *testing.T) {
	environment := SafeEnvironment([]string{
		"PATH=/bin",
		"PO_API_KEY=secret",
		"GITHUB_TOKEN=secret",
		"GOOGLE_APPLICATION_CREDENTIALS=/tmp/key.json",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"SAFE_VALUE=visible",
		"malformed",
	}, "PO_API_KEY")

	got := strings.Join(environment, "\n")
	if got != "PATH=/bin\nSAFE_VALUE=visible" {
		t.Fatalf("SafeEnvironment() = %q", got)
	}
}

func TestProcessHelper(t *testing.T) {
	switch os.Getenv("PO_PROCESS_HELPER") {
	case "exit":
		fmt.Fprint(os.Stdout, "stdout")
		fmt.Fprint(os.Stderr, "stderr")
		os.Exit(7)
	case "large":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 128))
		os.Exit(0)
	}
}
