package main

import (
	"fmt"
	"strings"
)

type replCommand struct {
	name        string
	usage       string
	description string
}

// replCommands 是公开交互命令的唯一目录，欢迎信息、/help 和输入补全都从这里生成。
var replCommands = []replCommand{
	{name: "/session", usage: "/session", description: "show session details"},
	{name: "/models", usage: "/models", description: "choose a model from configured providers"},
	{name: "/abort", usage: "/abort", description: "stop the active run"},
	{name: "/steer", usage: "/steer MESSAGE", description: "steer at the next safe turn boundary"},
	{name: "/queue", usage: "/queue MESSAGE", description: "queue a follow-up for the natural stopping point"},
	{name: "/clear", usage: "/clear", description: "clear the visible transcript; session data is retained"},
	{name: "/help", usage: "/help", description: "show command help"},
	{name: "/quit", usage: "/quit", description: "exit when no run is active"},
}

func replWelcome(sessionID string) string {
	return fmt.Sprintf("Po interactive session %s\n\n%s", sessionID, formatREPLCommands(replCommands))
}

func replHelp() string {
	return formatREPLCommands(replCommands) +
		"\n\nWhile running, Enter uses the mode shown in the input box; " +
		"Tab switches between Steer and Queue."
}

func formatREPLCommands(commands []replCommand) string {
	width := 0
	for _, command := range commands {
		width = max(width, len(command.usage))
	}

	var output strings.Builder
	for index, command := range commands {
		if index > 0 {
			output.WriteByte('\n')
		}
		fmt.Fprintf(&output, "%-*s  %s", width, command.usage, command.description)
	}
	return output.String()
}

func matchingREPLCommands(input string) []replCommand {
	input = strings.ToLower(input)
	if !strings.HasPrefix(input, "/") || strings.IndexAny(input, " \t\r\n") >= 0 {
		return nil
	}

	matches := make([]replCommand, 0, len(replCommands))
	for _, command := range replCommands {
		if strings.HasPrefix(command.name, input) {
			matches = append(matches, command)
		}
	}
	return matches
}
