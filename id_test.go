package po

import "testing"

func TestNewAtomicIDGeneratorSeparatesInstances(t *testing.T) {
	first := NewAtomicIDGenerator()
	second := NewAtomicIDGenerator()

	if got, want := first.NewID("tool-result"), second.NewID("tool-result"); got == want {
		t.Fatalf("namespaced generators returned duplicate ID %q", got)
	}
}
