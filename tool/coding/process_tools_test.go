package coding

import (
	"strings"
	"testing"
)

func TestDevelopmentToolsExposeGitAndGo(t *testing.T) {
	tools := NewProcessToolkit(t.TempDir(), nil).DevelopmentTools()
	if len(tools) != 2 {
		t.Fatalf("DevelopmentTools() returned %d tools, want 2", len(tools))
	}
	if got := tools[0].Spec().Name(); got != "git" {
		t.Fatalf("first tool = %q, want git", got)
	}
	if got := tools[1].Spec().Name(); got != "go" {
		t.Fatalf("second tool = %q, want go", got)
	}
}

func TestValidateGitArgsAllowsOnlyInspectionSubcommands(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "status", args: []string{"status", "--short"}},
		{name: "branch list", args: []string{"branch", "--list"}},
		{name: "branch delete", args: []string{"branch", "--delete", "topic"}, wantErr: true},
		{name: "commit", args: []string{"commit", "-m", "message"}, wantErr: true},
		{name: "empty", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateGitArgs(test.args)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateGitArgs(%q) error = %v, wantErr %t", test.args, err, test.wantErr)
			}
		})
	}
}

func TestProcessToolDescriptionsRequireApproval(t *testing.T) {
	for _, tool := range NewProcessToolkit(t.TempDir(), nil).DevelopmentTools() {
		if description := tool.Spec().Description(); !strings.Contains(description, "requires approval") {
			t.Errorf("%s description does not disclose approval: %q", tool.Spec().Name(), description)
		}
	}
}
