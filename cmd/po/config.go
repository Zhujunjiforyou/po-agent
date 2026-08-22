package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
	openai "github.com/lemonzjj/po-agent-go/provider/openai"
)

const defaultAPIKeyEnv = "PO_API_KEY"

// appConfig 有意作为产品层配置。模型服务细节位于 package po 之外，以维持核心到扩展的
// 依赖边界。
type appConfig struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`

	APIKeyEnv string `json:"api_key_env,omitempty"`

	ContextWindow        int `json:"context_window"`
	ModelMaxOutputTokens int `json:"model_max_output_tokens"`
	MaxOutputTokens      int `json:"max_output_tokens"`

	Streaming         bool `json:"streaming"`
	Tools             bool `json:"tools"`
	ParallelToolCalls bool `json:"parallel_tool_calls"`
	Reasoning         bool `json:"reasoning"`

	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	PresencePenalty *float64 `json:"presence_penalty,omitempty"`

	ExtraBody map[string]any `json:"extra_body,omitempty"`

	IncludeUsage bool `json:"include_usage,omitempty"`
}

func (c appConfig) validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("base_url is required")
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("model is required")
	}
	if c.ContextWindow <= 0 {
		return errors.New("context_window must be positive")
	}
	if c.ModelMaxOutputTokens <= 0 {
		return errors.New("model_max_output_tokens must be positive")
	}
	if c.MaxOutputTokens <= 0 || c.MaxOutputTokens > c.ModelMaxOutputTokens {
		return errors.New("max_output_tokens must be positive and <= model_max_output_tokens")
	}
	if c.ParallelToolCalls && !c.Tools {
		return errors.New("parallel_tool_calls requires tools=true")
	}
	return nil
}

func (c appConfig) providerConfig(apiKey string) openai.Config {
	return openai.Config{
		Provider: "openai-compatible",
		BaseURL:  c.BaseURL,
		APIKey:   apiKey,
		Model:    c.Model,
		Capabilities: po.ModelCapabilities{
			Streaming:         c.Streaming,
			Tools:             c.Tools,
			ParallelToolCalls: c.ParallelToolCalls,
			Reasoning:         c.Reasoning,

			// Qwen3.6 本身支持视觉输入，但 Po 协议 v1 目前只接受纯文本 UserMessage。
			// 此处声明适配器实际支持的能力，而不是模型权重本身的能力。
			Vision:           false,
			StructuredOutput: false,
		},
		Limits: po.ModelLimits{
			ContextWindow:   c.ContextWindow,
			MaxOutputTokens: c.ModelMaxOutputTokens,
		},
		Temperature:     c.Temperature,
		TopP:            c.TopP,
		PresencePenalty: c.PresencePenalty,
		ExtraBody:       c.ExtraBody,
		IncludeUsage:    c.IncludeUsage,
	}
}

func defaultConfigPath() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("PO_CONFIG")); explicit != "" {
		return explicit, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(dir, "po", "config.json"), nil
}

func loadAppConfig(path string) (appConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return appConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var config appConfig
	if err := decodeStrictJSON(data, &config); err != nil {
		return appConfig{}, fmt.Errorf("decode config %s: %w", path, err)
	}

	// 环境变量有意覆盖不涉及密钥的路由字段，让用户无需修改配置文件就能临时切换端点
	// 或模型。
	if value := strings.TrimSpace(os.Getenv("PO_BASE_URL")); value != "" {
		config.BaseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("PO_MODEL")); value != "" {
		config.Model = value
	}
	if config.APIKeyEnv == "" {
		config.APIKeyEnv = defaultAPIKeyEnv
	}
	if err := config.validate(); err != nil {
		return appConfig{}, fmt.Errorf("validate config: %w", err)
	}
	return config, nil
}

func resolveAPIKey(config appConfig) (string, error) {
	// PO_API_KEY 始终具有最高优先级，因此即使配置文件来自另一个环境，常规使用路径仍然
	// 保持简单。
	if value := strings.TrimSpace(os.Getenv(defaultAPIKeyEnv)); value != "" {
		return value, nil
	}
	envName := config.APIKeyEnv
	if envName == "" {
		envName = defaultAPIKeyEnv
	}
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return "", fmt.Errorf("API key is missing: set environment variable %s", envName)
	}
	return value, nil
}

func qwen36_27BConfig(baseURL, model string) appConfig {
	temperature := 0.6
	topP := 0.95
	presencePenalty := 0.0
	return appConfig{
		BaseURL:              baseURL,
		Model:                model,
		APIKeyEnv:            defaultAPIKeyEnv,
		ContextWindow:        262144,
		ModelMaxOutputTokens: 81920,
		MaxOutputTokens:      32768,
		Streaming:            true,
		Tools:                true,
		ParallelToolCalls:    true,
		Reasoning:            true,
		Temperature:          &temperature,
		TopP:                 &topP,
		PresencePenalty:      &presencePenalty,
	}
}

func writeConfig(path string, config appConfig, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("config already exists: %s (use --force to overwrite)", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// projectConfig 只允许项目覆盖非 Secret 的运行策略。
// 它本身仍然是可执行语义的一部分（例如能切换 endpoint/model），因此只有项目被信任时才加载。
type projectConfig struct {
	BaseURL         *string        `json:"base_url,omitempty"`
	Model           *string        `json:"model,omitempty"`
	MaxOutputTokens *int           `json:"max_output_tokens,omitempty"`
	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            *float64       `json:"top_p,omitempty"`
	PresencePenalty *float64       `json:"presence_penalty,omitempty"`
	ExtraBody       map[string]any `json:"extra_body,omitempty"`
}

func loadProjectConfig(path string) (projectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return projectConfig{}, err
	}
	var local projectConfig
	if err := decodeStrictJSON(data, &local); err != nil {
		return projectConfig{}, fmt.Errorf("decode project config %s: %w", path, err)
	}
	return local, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("expected exactly one JSON value")
		}
		return err
	}
	return nil
}

func applyProjectConfig(base appConfig, local projectConfig) appConfig {
	if local.BaseURL != nil {
		base.BaseURL = *local.BaseURL
	}
	if local.Model != nil {
		base.Model = *local.Model
	}
	if local.MaxOutputTokens != nil {
		base.MaxOutputTokens = *local.MaxOutputTokens
	}
	if local.Temperature != nil {
		v := *local.Temperature
		base.Temperature = &v
	}
	if local.TopP != nil {
		v := *local.TopP
		base.TopP = &v
	}
	if local.PresencePenalty != nil {
		v := *local.PresencePenalty
		base.PresencePenalty = &v
	}
	if local.ExtraBody != nil {
		base.ExtraBody = local.ExtraBody
	}
	return base
}
