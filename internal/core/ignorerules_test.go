package core

import "testing"

func TestIgnoresDotfilesAndDotDirectories(t *testing.T) {
	cases := []string{".DS_Store", ".git/config", "notes/.hidden.md"}
	for _, c := range cases {
		if !IsIgnored(c, nil) {
			t.Errorf("expected %q to be ignored", c)
		}
	}
}

func TestIgnoresOfficeLockFiles(t *testing.T) {
	cases := []string{
		"~$report.docx",
		"Psychology/~$notes.docx",
		"Psychology/~$ихоанализа и изкуствен интелект.docx",
	}
	for _, c := range cases {
		if !IsIgnored(c, nil) {
			t.Errorf("expected %q to be ignored", c)
		}
	}
}

func TestAllowsOrdinaryFiles(t *testing.T) {
	cases := []string{"notes/2026-08-10.md", "invoice.pdf"}
	for _, c := range cases {
		if IsIgnored(c, nil) {
			t.Errorf("expected %q to NOT be ignored", c)
		}
	}
}

func TestCustomPatternMatchesAtAnyDepth(t *testing.T) {
	patterns := []string{"node_modules"}
	cases := []string{"node_modules", "project/node_modules", "project/node_modules/left-pad"}
	for _, c := range cases {
		if !IsIgnored(c, patterns) {
			t.Errorf("expected %q to be ignored by pattern %v", c, patterns)
		}
	}
}

func TestCustomPatternGlob(t *testing.T) {
	patterns := []string{"*.tmp"}
	if !IsIgnored("scratch/output.tmp", patterns) {
		t.Error("expected scratch/output.tmp to be ignored")
	}
	if IsIgnored("scratch/output.txt", patterns) {
		t.Error("expected scratch/output.txt to NOT be ignored")
	}
}

func TestCustomPatternsDoNotOverrideDotfileRule(t *testing.T) {
	if !IsIgnored(".DS_Store", []string{"*.tmp"}) {
		t.Error("expected .DS_Store to still be ignored regardless of unrelated custom patterns")
	}
}

func TestNoCustomPatternsMeansOnlyBuiltInRulesApply(t *testing.T) {
	if IsIgnored("node_modules", nil) {
		t.Error("expected node_modules to NOT be ignored without a custom pattern")
	}
}
