package appconfig

import "strings"

// legacyFile 对应早期单模型配置，仅用于兼容读取。
type legacyFile struct {
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
	Vision            bool `json:"vision,omitempty"`
	StructuredOutput  bool `json:"structured_output,omitempty"`

	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            *float64       `json:"top_p,omitempty"`
	PresencePenalty *float64       `json:"presence_penalty,omitempty"`
	ExtraBody       map[string]any `json:"extra_body,omitempty"`
	IncludeUsage    bool           `json:"include_usage,omitempty"`
}

func decodeLegacy(data []byte) (Document, error) {
	var legacy legacyFile
	if err := decodeStrict(data, &legacy); err != nil {
		return Document{}, err
	}
	apiKeyEnv := strings.TrimSpace(legacy.APIKeyEnv)
	if apiKeyEnv == "" {
		apiKeyEnv = DefaultAPIKeyEnv
	}
	runtime := Runtime{
		Provider:             "default",
		BaseURL:              legacy.BaseURL,
		Model:                legacy.Model,
		APIKeyEnv:            apiKeyEnv,
		ContextWindow:        legacy.ContextWindow,
		ModelMaxOutputTokens: legacy.ModelMaxOutputTokens,
		MaxOutputTokens:      legacy.MaxOutputTokens,
		Streaming:            legacy.Streaming,
		Tools:                legacy.Tools,
		ParallelToolCalls:    legacy.ParallelToolCalls,
		Reasoning:            legacy.Reasoning,
		Vision:               legacy.Vision,
		StructuredOutput:     legacy.StructuredOutput,
		Temperature:          legacy.Temperature,
		TopP:                 legacy.TopP,
		PresencePenalty:      legacy.PresencePenalty,
		ExtraBody:            legacy.ExtraBody,
		IncludeUsage:         legacy.IncludeUsage,
	}
	if err := runtime.Validate(); err != nil {
		return Document{}, err
	}
	file := New("default", runtime.BaseURL, runtime.Model)
	file.Providers["default"] = Provider{BaseURL: runtime.BaseURL, APIKeyEnv: apiKeyEnv}
	return Document{File: file, Legacy: &runtime}, nil
}
