package core

import (
	"path/filepath"
	"strings"
)

// IsIgnored reports whether relativePath should be skipped. Dotfiles/dot-
// directories and Microsoft Office's ~$ lock files (created alongside a
// .docx/.xlsx/.pptx while it's open for editing — transient owner-info
// files, never real content) are always ignored, non-configurably.
// patterns are additional per-folder glob patterns, matched against both
// the full relative path and every individual path component — so a plain
// "node_modules" pattern matches that directory (and everything under it)
// at any depth, not just when it's the file's immediate parent.
//
// relativePath uses "/" separators (e.g. "notes/2026-08-10.md").
func IsIgnored(relativePath string, patterns []string) bool {
	components := strings.Split(relativePath, "/")
	for _, c := range components {
		if strings.HasPrefix(c, ".") || strings.HasPrefix(c, "~$") {
			return true
		}
	}
	if len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		if ok, _ := filepath.Match(pattern, relativePath); ok {
			return true
		}
		for _, c := range components {
			if ok, _ := filepath.Match(pattern, c); ok {
				return true
			}
		}
	}
	return false
}
