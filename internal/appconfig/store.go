package appconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Document 表示从磁盘读取的配置。旧版运行参数只在内存中保留，不进入新版格式。
type Document struct {
	File   File
	Legacy *Runtime
}

func (d Document) IsLegacy() bool { return d.Legacy != nil }

func (d Document) Resolve(selection Selection) (Runtime, error) {
	if d.Legacy != nil {
		provider := strings.TrimSpace(selection.Provider)
		model := strings.TrimSpace(selection.Model)
		if (provider == "" || provider == d.File.Current.Provider) &&
			(model == "" || model == d.File.Current.Model) {
			return cloneRuntime(*d.Legacy), nil
		}
	}
	return d.File.Resolve(selection)
}

// DefaultPath 返回全局配置路径；PO_CONFIG 可以覆盖默认位置。
func DefaultPath() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("PO_CONFIG")); explicit != "" {
		return explicit, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(dir, "po", "config.json"), nil
}

// Load 读取新版配置，并兼容早期单模型格式。
func Load(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("read config %s: %w", path, err)
	}
	modern, err := isModern(data)
	if err != nil {
		return Document{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	if !modern {
		document, err := decodeLegacy(data)
		if err != nil {
			return Document{}, fmt.Errorf("decode config %s: %w", path, err)
		}
		return document, nil
	}

	var file File
	if err := decodeStrict(data, &file); err != nil {
		return Document{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := file.Validate(); err != nil {
		return Document{}, fmt.Errorf("validate config: %w", err)
	}
	return Document{File: file}, nil
}

// LoadFile 只接受可以安全改写的新版配置。
func LoadFile(path string) (File, error) {
	document, err := Load(path)
	if err != nil {
		return File{}, err
	}
	if document.IsLegacy() {
		return File{}, errors.New("this command requires the versioned config format; run po config migrate first")
	}
	return document.File, nil
}

func isModern(data []byte) (bool, error) {
	var root map[string]json.RawMessage
	if err := decodeStrict(data, &root); err != nil {
		return false, err
	}
	_, versioned := root["version"]
	_, hasCurrent := root["current"]
	_, hasProviders := root["providers"]
	return versioned || hasCurrent || hasProviders, nil
}

// Write 校验配置后通过同目录临时文件原子替换目标文件。
func Write(path string, file File, force bool) error {
	if err := file.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	return writeJSON(path, file, force)
}

func writeJSON(path string, value any, force bool) error {
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
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
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

func cloneRuntime(runtime Runtime) Runtime {
	runtime.Temperature = cloneFloat(runtime.Temperature)
	runtime.TopP = cloneFloat(runtime.TopP)
	runtime.PresencePenalty = cloneFloat(runtime.PresencePenalty)
	if runtime.ExtraBody != nil {
		extraBody := make(map[string]any, len(runtime.ExtraBody))
		for key, value := range runtime.ExtraBody {
			extraBody[key] = value
		}
		runtime.ExtraBody = extraBody
	}
	return runtime
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
