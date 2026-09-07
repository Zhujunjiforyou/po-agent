package main

import (
	"fmt"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/model/retry"
	openai "github.com/lemonzjj/po-agent-go/provider/openai"
)

func buildModel(config appconfig.Runtime, apiKey string) (po.Model, error) {
	provider := config.Provider
	if provider == "" {
		provider = "openai-compatible"
	}
	base, err := openai.New(openai.Config{
		Provider: provider,
		BaseURL:  config.BaseURL,
		APIKey:   apiKey,
		Model:    config.Model,
		Capabilities: po.ModelCapabilities{
			Streaming:         config.Streaming,
			Tools:             config.Tools,
			ParallelToolCalls: config.ParallelToolCalls,
			Reasoning:         config.Reasoning,
			Vision:            config.Vision,
			StructuredOutput:  config.StructuredOutput,
		},
		Limits: po.ModelLimits{
			ContextWindow:   config.ContextWindow,
			MaxOutputTokens: config.ModelMaxOutputTokens,
		},
		Temperature:     config.Temperature,
		TopP:            config.TopP,
		PresencePenalty: config.PresencePenalty,
		ExtraBody:       config.ExtraBody,
		IncludeUsage:    config.IncludeUsage,
	})
	if err != nil {
		return nil, fmt.Errorf("create provider model: %w", err)
	}
	return retry.New(base, retry.DefaultPolicy(), nil)
}
