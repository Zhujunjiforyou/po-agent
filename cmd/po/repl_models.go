package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/internal/modelcatalog"
)

// modelChoice 同时保存选择器需要展示的信息和切换所需的运行配置。
type modelChoice struct {
	Provider        string
	Model           string
	ContextWindow   int
	MaxOutputTokens int
	Current         bool

	config appconfig.Runtime
}

type modelCatalog struct {
	Choices  []modelChoice
	Warnings []string
}

type modelSource struct {
	name     string
	provider appconfig.Provider
	current  *modelChoice
}

type replModelManager struct {
	configPath string
	runtime    *appRuntime
	adjust     func(appconfig.Runtime) (appconfig.Runtime, error)
	client     modelcatalog.Client

	current appconfig.Runtime
}

func newREPLModelManager(
	configPath string,
	current appconfig.Runtime,
	runtime *appRuntime,
	adjust func(appconfig.Runtime) (appconfig.Runtime, error),
) *replModelManager {
	return &replModelManager{
		configPath: configPath,
		runtime:    runtime,
		adjust:     adjust,
		client:     catalogClient,
		current:    current,
	}
}

func (m *replModelManager) Current() appconfig.Runtime { return m.current }

func (m *replModelManager) Catalog(ctx context.Context) (modelCatalog, error) {
	sources, err := loadModelSources(m.configPath, m.current)
	if err != nil {
		return modelCatalog{}, err
	}

	type sourceResult struct {
		source modelSource
		models []modelcatalog.Model
		err    error
	}
	results := make(chan sourceResult, len(sources))
	var group sync.WaitGroup
	for _, source := range sources {
		group.Add(1)
		go func(source modelSource) {
			defer group.Done()
			models, err := m.client.List(ctx, catalogSource(source.provider))
			results <- sourceResult{source: source, models: models, err: err}
		}(source)
	}
	group.Wait()
	close(results)

	catalog := modelCatalog{}
	for result := range results {
		choices := make(map[string]modelChoice, len(result.models)+1)
		if result.source.current != nil {
			choices[result.source.current.Model] = *result.source.current
		}
		if result.err != nil {
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("%s: %v", result.source.name, result.err))
		}
		for _, remote := range result.models {
			choice, exists := choices[remote.ID]
			if !exists {
				runtime := appconfig.DefaultRuntime(result.source.name, result.source.provider, remote.ID)
				choice = modelChoice{Provider: result.source.name, Model: remote.ID, config: runtime}
			}
			choice.ContextWindow = remote.EffectiveContextWindow()
			choice.MaxOutputTokens = remote.DefaultMaxOutputTokens
			if !exists {
				choice.config = applyDiscoveredLimits(choice.config, choice)
			}
			choice.Current = sameSelectedModel(choice.config, m.current)
			choices[remote.ID] = choice
		}
		for _, choice := range choices {
			choice.Current = sameSelectedModel(choice.config, m.current)
			catalog.Choices = append(catalog.Choices, choice)
		}
	}

	sort.Slice(catalog.Choices, func(i, j int) bool {
		left, right := catalog.Choices[i], catalog.Choices[j]
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.Model < right.Model
	})
	sort.Strings(catalog.Warnings)
	if len(catalog.Choices) == 0 {
		return modelCatalog{}, fmt.Errorf("no models are available from configured providers")
	}
	return catalog, nil
}

func (m *replModelManager) Switch(choice modelChoice) error {
	config := choice.config
	if m.adjust != nil {
		var err error
		config, err = m.adjust(config)
		if err != nil {
			return err
		}
	}
	apiKey, err := appconfig.ResolveAPIKey(config, os.Getenv)
	if err != nil {
		return err
	}
	agent, model, err := m.runtime.prepareAgent(config, apiKey)
	if err != nil {
		return err
	}
	if err := m.persistSelection(appconfig.Selection{Provider: choice.Provider, Model: choice.Model}); err != nil {
		return err
	}
	m.runtime.Agent = agent
	m.runtime.model = model
	m.runtime.modelConfig = config
	m.current = config
	return nil
}

func (m *replModelManager) persistSelection(selection appconfig.Selection) error {
	document, err := appconfig.Load(m.configPath)
	if err != nil {
		return err
	}
	// 旧版配置继续支持本次会话切换，但不会被交互命令静默改写。
	if document.IsLegacy() {
		return nil
	}
	document.File.Current = selection
	return appconfig.Write(m.configPath, document.File, true)
}

func loadModelSources(path string, current appconfig.Runtime) ([]modelSource, error) {
	document, err := appconfig.Load(path)
	if err != nil {
		return nil, err
	}
	sources := make([]modelSource, 0, len(document.File.Providers))
	for name, provider := range document.File.Providers {
		var currentChoice *modelChoice
		if name == current.Provider {
			provider.BaseURL = current.BaseURL
			provider.APIKeyEnv = current.APIKeyEnv
			choice := modelChoice{Provider: name, Model: current.Model, Current: true, config: current}
			currentChoice = &choice
		}
		sources = append(sources, modelSource{name: name, provider: provider, current: currentChoice})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].name < sources[j].name })
	return sources, nil
}

func applyDiscoveredLimits(config appconfig.Runtime, choice modelChoice) appconfig.Runtime {
	if choice.ContextWindow > 0 {
		config.ContextWindow = choice.ContextWindow
	}
	if choice.MaxOutputTokens > 0 {
		config.ModelMaxOutputTokens = choice.MaxOutputTokens
		config.MaxOutputTokens = min(config.MaxOutputTokens, choice.MaxOutputTokens)
	}
	return config
}

func sameSelectedModel(left, right appconfig.Runtime) bool {
	if left.Model != right.Model {
		return false
	}
	if left.Provider != "" || right.Provider != "" {
		return left.Provider == right.Provider
	}
	return strings.TrimRight(left.BaseURL, "/") == strings.TrimRight(right.BaseURL, "/")
}

func replModelLabel(config appconfig.Runtime) string {
	provider := config.Provider
	if provider == "" {
		provider = "default"
	}
	return provider + " · " + config.Model
}

func isModelCommand(text string) bool {
	command, _, _ := strings.Cut(text, " ")
	return command == "/models"
}

func chooseREPLModel(
	ctx context.Context,
	command string,
	models *replModelManager,
	console replConsole,
) (modelChoice, bool, error) {
	if command != "/models" {
		return modelChoice{}, false, fmt.Errorf("usage: /models")
	}
	catalog, err := models.Catalog(ctx)
	if err != nil {
		return modelChoice{}, false, err
	}
	for _, warning := range catalog.Warnings {
		_ = console.Print("[models] " + warning)
	}
	choices := make([]consoleChoice, 0, len(catalog.Choices))
	for index, choice := range catalog.Choices {
		choices = append(choices, consoleChoice{
			ID:          fmt.Sprintf("%d", index),
			Group:       choice.Provider,
			Label:       choice.Model,
			Description: modelChoiceDescription(choice),
			Current:     choice.Current,
		})
	}
	selectedID, selected, err := console.SelectChoice("Select model", choices)
	if err != nil || !selected {
		return modelChoice{}, false, err
	}
	selectedIndex, err := strconv.Atoi(selectedID)
	if err != nil || selectedIndex < 0 || selectedIndex >= len(catalog.Choices) {
		return modelChoice{}, false, fmt.Errorf("invalid selector result %q", selectedID)
	}
	return catalog.Choices[selectedIndex], true, nil
}

func modelChoiceDescription(choice modelChoice) string {
	details := make([]string, 0, 2)
	if choice.ContextWindow > 0 {
		details = append(details, fmt.Sprintf("context %d", choice.ContextWindow))
	}
	if choice.MaxOutputTokens > 0 {
		details = append(details, fmt.Sprintf("output %d", choice.MaxOutputTokens))
	}
	return strings.Join(details, " · ")
}
