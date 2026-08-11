package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestPromptLineReturnsDefaultOnEmptyInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n"))
	got := promptLine(reader, "Server", "https://memory.indexmemory.com")
	if got != "https://memory.indexmemory.com" {
		t.Fatalf("expected default returned, got %q", got)
	}
}

func TestPromptLineReturnsTrimmedTypedValue(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("  /srv/docs  \n"))
	got := promptLine(reader, "Folder path", "")
	if got != "/srv/docs" {
		t.Fatalf("expected trimmed input, got %q", got)
	}
}

func TestPromptYesNoDefaultsOnEmptyInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n"))
	if !promptYesNo(reader, "Continue?", true) {
		t.Fatal("expected default true on empty input")
	}
	reader2 := bufio.NewReader(strings.NewReader("\n"))
	if promptYesNo(reader2, "Continue?", false) {
		t.Fatal("expected default false on empty input")
	}
}

func TestPromptYesNoParsesYAndNVariants(t *testing.T) {
	cases := map[string]bool{"y\n": true, "yes\n": true, "Y\n": true, "n\n": false, "no\n": false, "N\n": false}
	for input, want := range cases {
		reader := bufio.NewReader(strings.NewReader(input))
		got := promptYesNo(reader, "?", !want) // default is the opposite, so a wrong parse would flip the result
		if got != want {
			t.Errorf("input %q: expected %v, got %v", input, want, got)
		}
	}
}

func TestPromptAPIKeyFallsBackToPlainReadWhenNotATerminal(t *testing.T) {
	// go test's stdin isn't a TTY, so promptAPIKey's term.IsTerminal check
	// takes the plain-read fallback path here — exactly the path used for
	// piped/scripted input.
	reader := bufio.NewReader(strings.NewReader("mem_test123\n"))
	got, err := promptAPIKey(reader)
	if err != nil {
		t.Fatal(err)
	}
	if got != "mem_test123" {
		t.Fatalf("expected mem_test123, got %q", got)
	}
}

func TestSplitPatternsTrimsAndDropsEmpty(t *testing.T) {
	got := splitPatterns(" node_modules ,, *.tmp,")
	want := []string{"node_modules", "*.tmp"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}
