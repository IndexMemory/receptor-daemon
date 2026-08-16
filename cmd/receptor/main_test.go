package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestPromptLineReturnsDefaultOnEmptyInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n"))
	got, err := promptLine(reader, "Server", "https://memory.indexmemory.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://memory.indexmemory.com" {
		t.Fatalf("expected default returned, got %q", got)
	}
}

func TestPromptLineReturnsTrimmedTypedValue(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("  /srv/docs  \n"))
	got, err := promptLine(reader, "Folder path", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/srv/docs" {
		t.Fatalf("expected trimmed input, got %q", got)
	}
}

func TestPromptLineReturnsErrorOnExhaustedInput(t *testing.T) {
	// A real bug this guards against: silently treating EOF (piped input
	// ran out, or Ctrl+D) the same as "pressed Enter for the default"
	// causes an infinite loop in any caller that keeps re-prompting until
	// it gets a non-default answer (e.g. the setup wizard's "at least one
	// folder is required" loop).
	reader := bufio.NewReader(strings.NewReader(""))
	if _, err := promptLine(reader, "Folder path", ""); err == nil {
		t.Fatal("expected an error when the input stream is exhausted")
	}
}

func TestPromptYesNoDefaultsOnEmptyInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n"))
	got, err := promptYesNo(reader, "Continue?", true)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("expected default true on empty input")
	}
	reader2 := bufio.NewReader(strings.NewReader("\n"))
	got2, err := promptYesNo(reader2, "Continue?", false)
	if err != nil {
		t.Fatal(err)
	}
	if got2 {
		t.Fatal("expected default false on empty input")
	}
}

func TestPromptYesNoParsesYAndNVariants(t *testing.T) {
	cases := map[string]bool{"y\n": true, "yes\n": true, "Y\n": true, "n\n": false, "no\n": false, "N\n": false}
	for input, want := range cases {
		reader := bufio.NewReader(strings.NewReader(input))
		got, err := promptYesNo(reader, "?", !want) // default is the opposite, so a wrong parse would flip the result
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("input %q: expected %v, got %v", input, want, got)
		}
	}
}

func TestPromptYesNoReturnsErrorOnExhaustedInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	if _, err := promptYesNo(reader, "Continue?", true); err == nil {
		t.Fatal("expected an error when the input stream is exhausted")
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
