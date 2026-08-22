package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/session"
)

type runDone struct {
	run *session.Run
	err error
}

const replWelcome = `Po interactive session %s

/session  show session
/abort    abort active run
/queue X  queue follow-up while running
/clear    clear visible transcript
/help     show commands
/quit     exit`

const replWelcomeHelp = `/session       show session details
/abort         stop the active run
/steer X       steer at the next safe turn boundary
/queue X       queue a follow-up for the natural stopping point
/follow X      alias for /queue
/clear         clear the visible transcript; session data is retained
/quit          exit when no run is active

While running, Enter uses the mode shown in the input box; Tab switches between Steer and Queue.`

func runREPL(
	ctx context.Context,
	agent *po.Agent,
	opened *openedSession,
	approver *interactiveApprover,
	console replConsole,
) int {
	if err := console.Start(); err != nil {
		_ = console.Print(fmt.Sprintf("terminal: %v", err))
		return 1
	}
	defer func() { _ = console.Close() }()

	prompt := newPromptController(console)
	activity := newActivityReporter(prompt, console)
	unsubscribe := agent.Subscribe(activity.Observe)
	defer unsubscribe()

	_ = console.Print(fmt.Sprintf(replWelcome, opened.Session.ID()))
	prompt.SetRuntime("", false)
	prompt.Show()

	inputs := readConsoleInput(console)
	done := make(chan runDone, 1)
	inputIDs := po.NewAtomicIDGenerator()
	var active *session.Run
	var pendingApproval *approvalPrompt

	for {
		select {
		case <-ctx.Done():
			if active != nil {
				active.Abort()
				_, _ = active.Wait()
			}
			return 130

		case request := <-approver.requests:
			pendingApproval = &request
			prompt.SetApproval(request.Request.ToolName)
			_ = console.Print(fmt.Sprintf(
				"[approval] tool=%s\nreason: %s\narguments: %s",
				request.Request.ToolName,
				request.Request.Reason,
				strings.TrimSpace(string(request.Request.Call.Arguments)),
			))
			prompt.Show()

		case completed := <-done:
			if active != completed.run {
				continue
			}
			active = nil
			if pendingApproval != nil {
				pendingApproval.Reply <- false
				pendingApproval = nil
				prompt.SetApproval("")
			}
			_ = console.FinishModelMessage()
			_ = console.EndResponse()
			if completed.err != nil {
				_ = console.Print(fmt.Sprintf("[run] %v", completed.err))
			}
			activity.Reset()
			prompt.Show()

		case input := <-inputs:
			if input.err != nil {
				if errors.Is(input.err, errConsoleInterrupted) {
					if pendingApproval != nil {
						pendingApproval.Reply <- false
						pendingApproval = nil
						prompt.SetApproval("")
						inputs = readConsoleInput(console)
						continue
					}
					if active != nil {
						active.Abort()
						_ = console.Print("[abort requested]")
						inputs = readConsoleInput(console)
						continue
					}
					return 130
				}
				if pendingApproval != nil {
					pendingApproval.Reply <- false
				}
				if active != nil {
					active.Abort()
					_, _ = active.Wait()
				}
				if errors.Is(input.err, io.EOF) {
					return 0
				}
				_ = console.Print(fmt.Sprintf("stdin: %v", input.err))
				return 1
			}

			text := strings.TrimSpace(input.line)
			if pendingApproval != nil {
				allowed := strings.EqualFold(text, "y") || strings.EqualFold(text, "yes")
				pendingApproval.Reply <- allowed
				pendingApproval = nil
				prompt.SetApproval("")
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			}
			if text == "" {
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			}

			switch text {
			case "/help":
				_ = console.Print(replWelcomeHelp)
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			case "/clear":
				_ = console.ClearTranscript()
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			case "/session":
				_ = console.Print(fmt.Sprintf(
					"session=%s file=%s messages=%d",
					opened.Session.ID(),
					opened.Path,
					len(opened.Session.Transcript().Messages()),
				))
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			}

			if active != nil {
				handleActiveInput(active, inputIDs, text, input.mode, console)
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			}

			switch text {
			case "/quit", "/exit":
				return 0
			case "/abort":
				_ = console.Print("no active run")
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			}
			if isRunControlCommand(text) {
				_ = console.Print("no active run; send the message normally to start one")
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			}

			message, err := po.NewUserTextMessage(inputIDs.NewID("user"), text)
			if err != nil {
				_ = console.Print(err.Error())
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			}
			_ = console.PresentInput(consoleInputConversation, text)
			run, err := opened.Session.Start(ctx, agent, message)
			if err != nil {
				_ = console.Print(err.Error())
				prompt.Show()
				inputs = readConsoleInput(console)
				continue
			}
			active = run
			go func(current *session.Run) {
				_, err := current.Wait()
				done <- runDone{run: current, err: err}
			}(run)
			prompt.Show()
			inputs = readConsoleInput(console)
		}
	}
}

func isRunControlCommand(text string) bool {
	command, _, _ := strings.Cut(text, " ")
	return command == "/steer" || command == "/queue" || command == "/follow"
}

func readConsoleInput(console replConsole) <-chan consoleInput {
	inputs := make(chan consoleInput, 1)
	go func() {
		inputs <- console.ReadInput()
	}()
	return inputs
}

func handleActiveInput(
	active *session.Run,
	ids po.IDGenerator,
	text string,
	mode consoleInputMode,
	console replConsole,
) {
	switch {
	case text == "/abort":
		active.Abort()
		_ = console.Print("[abort requested]")
		return
	case text == "/quit" || text == "/exit":
		_ = console.Print("run active; use /abort or Ctrl+C before exiting")
		return
	}

	payload := text
	switch {
	case text == "/queue" || text == "/follow":
		_ = console.Print("usage: /queue MESSAGE")
		return
	case strings.HasPrefix(text, "/queue "):
		mode = consoleInputFollowUp
		payload = strings.TrimSpace(strings.TrimPrefix(text, "/queue "))
	case strings.HasPrefix(text, "/follow "):
		mode = consoleInputFollowUp
		payload = strings.TrimSpace(strings.TrimPrefix(text, "/follow "))
	case text == "/steer":
		_ = console.Print("usage: /steer MESSAGE")
		return
	case strings.HasPrefix(text, "/steer "):
		mode = consoleInputSteering
		payload = strings.TrimSpace(strings.TrimPrefix(text, "/steer "))
	case mode == consoleInputConversation:
		mode = consoleInputSteering
	}

	idPrefix := "steer"
	if mode == consoleInputFollowUp {
		idPrefix = "follow"
	}
	message, err := po.NewUserTextMessage(ids.NewID(idPrefix), payload)
	if err != nil {
		_ = console.Print(err.Error())
		return
	}
	if mode == consoleInputFollowUp {
		err = active.FollowUp(message)
	} else {
		err = active.Steer(message)
	}
	if err != nil {
		_ = console.Print(err.Error())
		return
	}
	_ = console.PresentInput(mode, payload)
}
