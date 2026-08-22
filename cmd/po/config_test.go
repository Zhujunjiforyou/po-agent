package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppConfigRejectsUnknownAndTrailingValues(t *testing.T) {
	valid, err := os.ReadFile(writeTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(valid))
	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown field", data: []byte(strings.TrimSuffix(base, "}") + `,"unknown":true}`)},
		{name: "trailing value", data: []byte(base + "\n{}\n")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadAppConfig(path); err == nil {
				t.Fatal("loadAppConfig() unexpectedly succeeded")
			}
		})
	}
}

func writeTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeConfig(path, qwen36_27BConfig("https://example.com/v1", "model"), false); err != nil {
		t.Fatal(err)
	}
	return path
}
