package coding

import (
	"errors"
	"reflect"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

func TestResolveResources(t *testing.T) {
	sharedWorkspace := []po.ResourceClaim{{Key: "workspace", Mode: po.ResourceAccessShared}}
	exclusiveWorkspace := []po.ResourceClaim{{Key: "workspace", Mode: po.ResourceAccessExclusive}}
	tests := []struct {
		name string
		args any
		want []po.ResourceClaim
	}{
		{
			name: "read",
			args: map[string]any{"path": "src/../main.go"},
			want: []po.ResourceClaim{
				{Key: "workspace", Mode: po.ResourceAccessShared},
				{Key: "file:main.go", Mode: po.ResourceAccessShared},
			},
		},
		{
			name: "edit",
			args: map[string]any{"path": "main.go"},
			want: []po.ResourceClaim{
				{Key: "workspace", Mode: po.ResourceAccessShared},
				{Key: "file:main.go", Mode: po.ResourceAccessExclusive},
			},
		},
		{
			name: "write",
			args: map[string]any{"path": "main.go"},
			want: []po.ResourceClaim{
				{Key: "workspace", Mode: po.ResourceAccessShared},
				{Key: "file:main.go", Mode: po.ResourceAccessExclusive},
			},
		},
		{name: "grep", args: map[string]any{"pattern": "TODO"}, want: sharedWorkspace},
		{name: "find", args: map[string]any{"pattern": "*.go"}, want: sharedWorkspace},
		{name: "ls", args: map[string]any{}, want: sharedWorkspace},
		{name: "git", args: map[string]any{"args": []string{"status"}}, want: sharedWorkspace},
		{name: "go", args: map[string]any{"args": []string{"test", "./..."}}, want: exclusiveWorkspace},
		{name: "shell", args: map[string]any{"command": "true"}, want: exclusiveWorkspace},
		{name: "unknown", args: map[string]any{}, want: exclusiveWorkspace},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call, err := po.NewToolCall("call-1", test.name, test.args)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ResolveResources(call)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveResources() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestResolveResourcesRejectsEscapingPath(t *testing.T) {
	call, err := po.NewToolCall("call-1", "read", map[string]any{"path": "../outside.txt"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveResources(call)
	if !errors.Is(err, po.ErrInvalidToolArguments) {
		t.Fatalf("error = %v, want ErrInvalidToolArguments", err)
	}
}
