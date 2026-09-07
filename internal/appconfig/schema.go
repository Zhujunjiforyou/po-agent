package appconfig

import (
	"errors"
	"fmt"
	"strings"
)

const (
	Version                       = 1
	DefaultAPIKeyEnv              = "PO_API_KEY"
	DefaultContextWindow          = 131072
	DefaultModelMaxOutputTokens   = 32768
	DefaultRequestMaxOutputTokens = 16384
)

// Selection 唯一标识一次模型选择。模型 ID 只在所属供应商内解释。
type Selection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Provider 保存 OpenAI 兼容服务的连接信息，不保存密钥本身。
type Provider struct {
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

// File 是用户需要维护的完整全局配置。
type File struct {
	Version   int                 `json:"version"`
	Current   Selection           `json:"current"`
	Providers map[string]Provider `json:"providers"`
}

// New 创建只包含一个供应商和当前模型的最小配置。
func New(providerName, baseURL, model string) File {
	providerName = strings.TrimSpace(providerName)
	return File{
		Version: Version,
		Current: Selection{Provider: providerName, Model: strings.TrimSpace(model)},
		Providers: map[string]Provider{
			providerName: {
				BaseURL:   strings.TrimSpace(baseURL),
				APIKeyEnv: DefaultAPIKeyEnv,
			},
		},
	}
}

func (f File) Validate() error {
	if f.Version != Version {
		return fmt.Errorf("unsupported config version %d", f.Version)
	}
	if len(f.Providers) == 0 {
		return errors.New("providers must not be empty")
	}
	for name, provider := range f.Providers {
		if err := validateName("provider", name); err != nil {
			return err
		}
		if strings.TrimSpace(provider.BaseURL) == "" {
			return fmt.Errorf("provider %q base_url is required", name)
		}
	}
	if strings.TrimSpace(f.Current.Provider) == "" {
		return errors.New("current.provider is required")
	}
	if _, ok := f.Providers[f.Current.Provider]; !ok {
		return fmt.Errorf("current provider %q is not configured", f.Current.Provider)
	}
	if strings.TrimSpace(f.Current.Model) == "" {
		return errors.New("current.model is required")
	}
	return nil
}

// Resolve 把持久化选择转换成一次运行所需的完整配置。
func (f File) Resolve(selection Selection) (Runtime, error) {
	if err := f.Validate(); err != nil {
		return Runtime{}, err
	}
	providerName := strings.TrimSpace(selection.Provider)
	if providerName == "" {
		providerName = f.Current.Provider
	}
	model := strings.TrimSpace(selection.Model)
	if model == "" && providerName == f.Current.Provider {
		model = f.Current.Model
	}
	if model == "" {
		return Runtime{}, errors.New("model is required when selecting another provider")
	}
	provider, ok := f.Providers[providerName]
	if !ok {
		return Runtime{}, fmt.Errorf("provider %q is not configured", providerName)
	}
	return DefaultRuntime(providerName, provider, model), nil
}

func validateName(kind, name string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\t\r\n") {
		return fmt.Errorf("%s name %q is invalid", kind, name)
	}
	return nil
}

// Runtime 只存在于进程内。用户无需了解模型能力默认值和请求参数。
type Runtime struct {
	Provider  string
	BaseURL   string
	Model     string
	APIKeyEnv string

	ContextWindow        int
	ModelMaxOutputTokens int
	MaxOutputTokens      int

	Streaming         bool
	Tools             bool
	ParallelToolCalls bool
	Reasoning         bool
	Vision            bool
	StructuredOutput  bool

	Temperature     *float64
	TopP            *float64
	PresencePenalty *float64
	ExtraBody       map[string]any
	IncludeUsage    bool
}

// DefaultRuntime 为未返回能力信息的模型提供统一的运行参数。
func DefaultRuntime(providerName string, provider Provider, model string) Runtime {
	apiKeyEnv := strings.TrimSpace(provider.APIKeyEnv)
	if apiKeyEnv == "" {
		apiKeyEnv = DefaultAPIKeyEnv
	}
	return Runtime{
		Provider:             strings.TrimSpace(providerName),
		BaseURL:              strings.TrimSpace(provider.BaseURL),
		Model:                strings.TrimSpace(model),
		APIKeyEnv:            apiKeyEnv,
		ContextWindow:        DefaultContextWindow,
		ModelMaxOutputTokens: DefaultModelMaxOutputTokens,
		MaxOutputTokens:      DefaultRequestMaxOutputTokens,
		Streaming:            true,
		Tools:                true,
		ParallelToolCalls:    true,
	}
}

func (r Runtime) Validate() error {
	if strings.TrimSpace(r.BaseURL) == "" {
		return errors.New("base_url is required")
	}
	if strings.TrimSpace(r.Model) == "" {
		return errors.New("model is required")
	}
	if r.ContextWindow <= 0 {
		return errors.New("context_window must be positive")
	}
	if r.ModelMaxOutputTokens <= 0 {
		return errors.New("model_max_output_tokens must be positive")
	}
	if r.MaxOutputTokens <= 0 || r.MaxOutputTokens > r.ModelMaxOutputTokens {
		return errors.New("max_output_tokens must be positive and <= model_max_output_tokens")
	}
	if r.ParallelToolCalls && !r.Tools {
		return errors.New("parallel_tool_calls requires tools=true")
	}
	return nil
}

// ResolveAPIKey 从供应商指定的环境变量读取密钥。
func ResolveAPIKey(runtime Runtime, lookup func(string) string) (string, error) {
	if lookup == nil {
		return "", errors.New("environment lookup is required")
	}
	envName := strings.TrimSpace(runtime.APIKeyEnv)
	if envName == "" {
		envName = DefaultAPIKeyEnv
	}
	value := strings.TrimSpace(lookup(envName))
	if value == "" {
		return "", fmt.Errorf("API key is missing: set environment variable %s", envName)
	}
	return value, nil
}
