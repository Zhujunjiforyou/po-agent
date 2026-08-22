package po

import "testing"

func TestToolResultCloneDoesNotShareContentSlice(t *testing.T) {
	result, err := NewTextToolResult("original", nil, false)
	if err != nil {
		t.Fatal(err)
	}

	clone := result.Clone()
	clone.content[0].Text = "mutated"
	if result.content[0].Text != "original" {
		t.Fatalf("original content changed through clone: %q", result.content[0].Text)
	}
}
