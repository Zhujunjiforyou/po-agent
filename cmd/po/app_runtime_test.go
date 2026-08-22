package main

import "testing"

func TestRiskyToolReasonsCoverEveryProcessAndMutationTool(t *testing.T) {
	reasons := riskyToolReasons()
	for _, name := range []string{"edit", "git", "go", "shell", "write"} {
		if reasons[name] == "" {
			t.Errorf("tool %q has no approval reason", name)
		}
	}
}
