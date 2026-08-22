package main

import (
	"context"
	"fmt"
	"log"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	"github.com/lemonzjj/po-agent-go/schema/basic"
	"github.com/lemonzjj/po-agent-go/tool/builtin"
)

func main() {
	model := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.CallTool(
			"assistant-1", "call-1", "echo", builtin.EchoArgs{Message: "hello from tool"}, po.Usage{},
		)),
		scripted.WithCheck(
			scripted.Must(scripted.Reply("assistant-2", "Tool result received.", po.Usage{})),
			scripted.ExpectLastToolResult("call-1"),
		),
	)

	registry := po.NewToolRegistry()
	must(registry.Register(builtin.NewEcho()))

	agent, err := po.NewAgent(po.AgentConfig{
		Model:     model,
		Tools:     registry,
		Validator: basic.New(),
	})
	must(err)

	user, err := po.NewUserTextMessage("user-1", "Call echo, then tell me you received the result.")
	must(err)

	result, err := agent.Run(context.Background(), user)
	must(err)
	fmt.Println(result.FinalText())
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
