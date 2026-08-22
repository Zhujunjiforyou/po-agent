package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

func runSession(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: po session inspect|recover --file FILE")
		return 2
	}

	switch args[0] {
	case "inspect":
		return runSessionInspect(args[1:], stdout, stderr)
	case "recover":
		return runSessionRecover(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown session command: %s\n", args[0])
		return 2
	}
}

func runSessionInspect(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po session inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("file", "", "session JSONL file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*path) == "" {
		fmt.Fprintln(stderr, "usage: po session inspect --file FILE")
		return 2
	}

	opened, err := openSession(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer opened.Close()

	state := opened.Session.State()
	fmt.Fprintf(stdout, "session=%s file=%s messages=%d\n", state.ID, opened.Path, len(state.Messages))
	if pending, ok := opened.Session.Recovery(); ok {
		fmt.Fprintf(
			stdout,
			"recovery=required attempt=%s user_message=%s started=%s\n",
			pending.AttemptID,
			pending.UserMessageID,
			pending.StartedAt.Format(time.RFC3339Nano),
		)
	} else {
		fmt.Fprintln(stdout, "recovery=none")
	}
	return 0
}

func runSessionRecover(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("po session recover", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("file", "", "session JSONL file")
	note := flags.String("note", "", "description of the external checks performed")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*path) == "" || strings.TrimSpace(*note) == "" {
		fmt.Fprintln(stderr, "usage: po session recover --file FILE --note NOTE")
		return 2
	}

	opened, err := openSession(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer opened.Close()

	pending, ok := opened.Session.Recovery()
	if !ok {
		fmt.Fprintln(stdout, "session does not require recovery")
		return 0
	}
	if err := opened.Session.ResolveRecovery(ctx, strings.TrimSpace(*note)); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "resolved attempt %s; the session can now be resumed\n", pending.AttemptID)
	return 0
}
