package output

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateHeadUsesMostRestrictiveLimit(t *testing.T) {
	text := "one\ntwo\nthree\nfour\n"
	result := TruncateHead(text, Limits{MaxBytes: 1024, MaxLines: 2})

	if !result.Truncated {
		t.Fatal("expected output to be truncated")
	}
	if result.Content != "one\ntwo\n" {
		t.Fatalf("Content = %q, want first two lines", result.Content)
	}
	if result.TotalLines != 4 || result.OutputLines != 2 {
		t.Fatalf("line counts = %d/%d, want 2/4", result.OutputLines, result.TotalLines)
	}
}

func TestTruncateTailKeepsFinalLines(t *testing.T) {
	text := "compile package-a\ncompile package-b\nFAIL package-c\nsummary\n"
	result := TruncateTail(text, Limits{MaxBytes: 1024, MaxLines: 2})

	if result.Content != "FAIL package-c\nsummary\n" {
		t.Fatalf("Content = %q", result.Content)
	}
}

func TestByteLimitDoesNotSplitUTF8Rune(t *testing.T) {
	text := "A你好B"
	head := TruncateHead(text, Limits{MaxBytes: 5, MaxLines: 100})
	tail := TruncateTail(text, Limits{MaxBytes: 5, MaxLines: 100})

	if !utf8.ValidString(head.Content) {
		t.Fatalf("head content is invalid UTF-8: %q", head.Content)
	}
	if !utf8.ValidString(tail.Content) {
		t.Fatalf("tail content is invalid UTF-8: %q", tail.Content)
	}
}

func TestNoticeMakesLossExplicit(t *testing.T) {
	result := Result{
		Truncated:   true,
		TotalBytes:  100,
		OutputBytes: 40,
		TotalLines:  10,
		OutputLines: 4,
	}

	notice := result.Notice("artifact://tool-output/123")
	for _, want := range []string{"4 of 10 lines", "40 of 100 bytes", "artifact://tool-output/123"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("Notice() = %q, want substring %q", notice, want)
		}
	}
}

func TestTrailingNewlineDoesNotCreateExtraLogicalLine(t *testing.T) {
	result := TruncateHead("a\nb\n", Limits{MaxBytes: 1024, MaxLines: 2})
	if result.Truncated {
		t.Fatal("two lines with trailing newline should fit a two-line limit")
	}
	if result.TotalLines != 2 {
		t.Fatalf("TotalLines = %d, want 2", result.TotalLines)
	}
}
