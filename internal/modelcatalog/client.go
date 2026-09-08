package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Source 描述一个 OpenAI 兼容的模型目录端点。
type Source struct {
	BaseURL string
	APIKey  string
}

// Model 是 /models 返回的通用模型信息。
type Model struct {
	ID                     string `json:"id"`
	ContextWindow          int    `json:"context_window"`
	MaxContextWindow       int    `json:"max_context_window"`
	DefaultMaxOutputTokens int    `json:"default_max_output_tokens"`
}

func (m Model) EffectiveContextWindow() int {
	if m.ContextWindow > 0 {
		return m.ContextWindow
	}
	return m.MaxContextWindow
}

// Client 读取供应商的 /models 接口。
type Client struct {
	HTTP *http.Client
}

// List 返回按 ID 排序并去重后的模型列表。
func (c Client) List(ctx context.Context, source Source) ([]Model, error) {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	endpoint := strings.TrimRight(source.BaseURL, "/") + "/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create model discovery request: %w", err)
	}
	if key := strings.TrimSpace(source.APIKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return nil, fmt.Errorf("list models: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	var payload struct {
		Data   []Model `json:"data"`
		Models []Model `json:"models"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	models := payload.Data
	if len(models) == 0 {
		models = payload.Models
	}
	unique := make(map[string]Model, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID != "" {
			unique[model.ID] = model
		}
	}
	models = make([]Model, 0, len(unique))
	for _, model := range unique {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
