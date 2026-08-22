package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// TrustDecision 表示是否可以加载项目内可能影响执行的配置。
type TrustDecision string

const (
	// TrustUnknown 表示尚未记录当前路径或其祖先路径的决策。
	TrustUnknown TrustDecision = "unknown"
	// TrustAlways 允许当前路径及其后代路径加载项目配置。
	TrustAlways TrustDecision = "always"
	// TrustNever 禁止当前路径及其后代路径加载项目配置。
	TrustNever TrustDecision = "never"
)

// TrustStore 将规范化的项目信任决策持久化到私有 JSON 文件中。
type TrustStore struct {
	path      string
	mu        sync.Mutex
	decisions map[string]TrustDecision
}

// OpenTrustStore 打开已有的信任存储；文件不存在时返回空存储。
func OpenTrustStore(path string) (*TrustStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("trust store path is required")
	}
	store := &TrustStore{path: path, decisions: map[string]TrustDecision{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, fmt.Errorf("read trust store: %w", err)
	}
	var stored struct {
		Decisions map[string]TrustDecision `json:"decisions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return nil, fmt.Errorf("decode trust store: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("expected exactly one JSON value")
		}
		return nil, fmt.Errorf("decode trust store: %w", err)
	}
	for projectPath, decision := range stored.Decisions {
		if decision != TrustAlways && decision != TrustNever {
			return nil, fmt.Errorf("decode trust store: invalid decision %q for %q", decision, projectPath)
		}
		store.decisions[projectPath] = decision
	}
	return store, nil
}

// Decision 返回当前路径或其祖先路径中距离最近的已记录决策。
func (s *TrustStore) Decision(path string) TrustDecision {
	if s == nil {
		return TrustUnknown
	}
	canonical, err := canonicalPath(path)
	if err != nil {
		return TrustUnknown
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := canonical
	for {
		if d, ok := s.decisions[current]; ok {
			return d
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return TrustUnknown
}

// Set 记录并可靠持久化指定路径的决策。
func (s *TrustStore) Set(path string, decision TrustDecision) error {
	if s == nil {
		return fmt.Errorf("trust store is nil")
	}
	if decision != TrustAlways && decision != TrustNever {
		return fmt.Errorf("invalid trust decision %q", decision)
	}
	canonical, err := canonicalPath(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.decisions[canonical]
	s.decisions[canonical] = decision
	if err := s.saveLocked(); err != nil {
		if existed {
			s.decisions[canonical] = previous
		} else {
			delete(s.decisions, canonical)
		}
		return err
	}
	return nil
}

func (s *TrustStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create trust directory: %w", err)
	}
	data, err := json.MarshalIndent(struct {
		Decisions map[string]TrustDecision `json:"decisions"`
	}{Decisions: s.decisions}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write trust store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace trust store: %w", err)
	}
	return nil
}

func canonicalPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("project path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}
