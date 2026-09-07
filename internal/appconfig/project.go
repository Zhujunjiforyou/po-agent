package appconfig

import (
	"fmt"
	"os"
)

// Project 只允许可信项目覆盖非密钥的运行参数。
type Project struct {
	Provider        *string        `json:"provider,omitempty"`
	BaseURL         *string        `json:"base_url,omitempty"`
	Model           *string        `json:"model,omitempty"`
	MaxOutputTokens *int           `json:"max_output_tokens,omitempty"`
	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            *float64       `json:"top_p,omitempty"`
	PresencePenalty *float64       `json:"presence_penalty,omitempty"`
	ExtraBody       map[string]any `json:"extra_body,omitempty"`
}

// LoadProject 读取可信工作区中的项目级覆盖项。
func LoadProject(path string) (Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Project{}, err
	}
	var project Project
	if err := decodeStrict(data, &project); err != nil {
		return Project{}, fmt.Errorf("decode project config %s: %w", path, err)
	}
	return project, nil
}

func (p Project) Select(base Selection) Selection {
	if p.Provider != nil {
		base.Provider = *p.Provider
	}
	if p.Model != nil {
		base.Model = *p.Model
	}
	return base
}

func (p Project) Apply(runtime Runtime) Runtime {
	if p.BaseURL != nil {
		runtime.BaseURL = *p.BaseURL
	}
	if p.MaxOutputTokens != nil {
		runtime.MaxOutputTokens = *p.MaxOutputTokens
	}
	if p.Temperature != nil {
		value := *p.Temperature
		runtime.Temperature = &value
	}
	if p.TopP != nil {
		value := *p.TopP
		runtime.TopP = &value
	}
	if p.PresencePenalty != nil {
		value := *p.PresencePenalty
		runtime.PresencePenalty = &value
	}
	if p.ExtraBody != nil {
		runtime.ExtraBody = make(map[string]any, len(p.ExtraBody))
		for key, value := range p.ExtraBody {
			runtime.ExtraBody[key] = value
		}
	}
	return runtime
}
