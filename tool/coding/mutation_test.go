package coding

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
)

func digestText(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}

func codingToolByName(t *testing.T, toolkit *Toolkit, name string) po.Tool {
	t.Helper()
	tools, err := toolkit.CodingTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Spec().Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func TestEditUsesVersionCASAndPreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	original := "package demo\r\n\r\nfunc answer() int {\r\n\treturn 41\r\n}\r\n"
	if err := os.WriteFile(filepath.Join(dir, "answer.go"), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	edit := codingToolByName(t, NewToolkit(dirFS{root: dir}), "edit")
	result := executeTool(t, edit, map[string]any{
		"path":            "answer.go",
		"expected_sha256": digestText(original),
		"edits": []map[string]any{{
			"old_text": "func answer() int {\n\treturn 41\n}",
			"new_text": "func answer() int {\n\treturn 42\n}",
		}},
	})
	data, err := os.ReadFile(filepath.Join(dir, "answer.go"))
	if err != nil {
		t.Fatal(err)
	}
	updated := string(data)
	if !strings.Contains(updated, "return 42\r\n") {
		t.Fatalf("updated file = %q", updated)
	}
	if strings.Contains(strings.ReplaceAll(updated, "\r\n", ""), "\n") {
		t.Fatalf("line ending was not preserved: %q", updated)
	}
	text := toolText(t, result)
	if !strings.Contains(text, "new_sha256=") || !strings.Contains(text, "-\treturn 41") || !strings.Contains(text, "+\treturn 42") {
		t.Fatalf("edit result = %q", text)
	}
}

func TestEditRejectsStaleVersionWithoutOverwritingExternalChange(t *testing.T) {
	dir := t.TempDir()
	initial := "value = 1\n"
	path := filepath.Join(dir, "state.txt")
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := digestText(initial)
	if err := os.WriteFile(path, []byte("value = 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	edit := codingToolByName(t, NewToolkit(dirFS{root: dir}), "edit")
	call, _ := po.NewToolCall("stale", "edit", map[string]any{
		"path":            "state.txt",
		"expected_sha256": expected,
		"edits":           []map[string]any{{"old_text": "value = 1", "new_text": "value = 2"}},
	})
	_, err := edit.Execute(context.Background(), call, nil)
	if !errors.Is(err, ErrFileChanged) {
		t.Fatalf("error = %v, want ErrFileChanged", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "value = 9\n" {
		t.Fatalf("external change was overwritten: %q", data)
	}
}

func TestConcurrentEditsFromSameVersionAllowExactlyOneCommit(t *testing.T) {
	dir := t.TempDir()
	initial := "value = 0\n"
	if err := os.WriteFile(filepath.Join(dir, "state.txt"), []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	toolkit := NewToolkit(dirFS{root: dir})
	edit := codingToolByName(t, toolkit, "edit")
	expected := digestText(initial)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, next := range []string{"value = 1", "value = 2"} {
		next := next
		wg.Add(1)
		go func() {
			defer wg.Done()
			call, _ := po.NewToolCall("concurrent-"+next, "edit", map[string]any{
				"path":            "state.txt",
				"expected_sha256": expected,
				"edits":           []map[string]any{{"old_text": "value = 0", "new_text": next}},
			})
			_, err := edit.Execute(context.Background(), call, nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrFileChanged):
			conflicts++
		default:
			t.Fatalf("unexpected edit error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestEditRequiresUniqueNonOverlappingOriginalText(t *testing.T) {
	dir := t.TempDir()
	initial := "x = 1\nx = 1\n"
	if err := os.WriteFile(filepath.Join(dir, "dup.txt"), []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	edit := codingToolByName(t, NewToolkit(dirFS{root: dir}), "edit")
	call, _ := po.NewToolCall("dup", "edit", map[string]any{
		"path":            "dup.txt",
		"expected_sha256": digestText(initial),
		"edits":           []map[string]any{{"old_text": "x = 1", "new_text": "x = 2"}},
	})
	if _, err := edit.Execute(context.Background(), call, nil); err == nil || !strings.Contains(err.Error(), "matches 2 locations") {
		t.Fatalf("error = %v", err)
	}
}

func TestEditAppliesMultipleDisjointReplacementsAgainstOriginal(t *testing.T) {
	dir := t.TempDir()
	initial := "alpha\nkeep\nomega\n"
	if err := os.WriteFile(filepath.Join(dir, "multi.txt"), []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	edit := codingToolByName(t, NewToolkit(dirFS{root: dir}), "edit")
	_ = executeTool(t, edit, map[string]any{
		"path":            "multi.txt",
		"expected_sha256": digestText(initial),
		"edits": []map[string]any{
			{"old_text": "alpha", "new_text": "ALPHA"},
			{"old_text": "omega", "new_text": "OMEGA"},
		},
	})
	data, _ := os.ReadFile(filepath.Join(dir, "multi.txt"))
	if string(data) != "ALPHA\nkeep\nOMEGA\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestWriteCreatesNewFileButRequiresVersionForOverwrite(t *testing.T) {
	dir := t.TempDir()
	write := codingToolByName(t, NewToolkit(dirFS{root: dir}), "write")
	_ = executeTool(t, write, map[string]any{"path": "docs/new.md", "content": "hello\n"})
	data, err := os.ReadFile(filepath.Join(dir, "docs", "new.md"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("new file data=%q err=%v", data, err)
	}

	call, _ := po.NewToolCall("overwrite-no-version", "write", map[string]any{"path": "docs/new.md", "content": "bye\n"})
	if _, err := write.Execute(context.Background(), call, nil); !errors.Is(err, ErrVersionRequired) {
		t.Fatalf("error = %v, want ErrVersionRequired", err)
	}

	_ = executeTool(t, write, map[string]any{
		"path":            "docs/new.md",
		"content":         "bye\n",
		"expected_sha256": digestText("hello\n"),
	})
	data, _ = os.ReadFile(filepath.Join(dir, "docs", "new.md"))
	if string(data) != "bye\n" {
		t.Fatalf("overwritten data = %q", data)
	}
}

func TestMutationQueueWaitRespectsContext(t *testing.T) {
	queue := newMutationQueue()
	release, err := queue.acquire(context.Background(), "same.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	cause := errors.New("stop waiting")
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := queue.acquire(ctx, "same.txt")
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel(cause)
	if err := <-done; !errors.Is(err, cause) {
		t.Fatalf("error = %v, want cause", err)
	}
}
