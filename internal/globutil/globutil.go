// Package globutil provides glob pattern matching with real ** (recursive)
// support. Go's stdlib path/filepath does NOT implement ** as recursive —
// it treats "**" as a redundant pair of single-segment wildcards, so
// "**/*.go" only ever matches files exactly one directory deep. This
// package implements actual recursive matching, and both the Guard
// (path/command checking) and the filesystem.glob capability (actually
// finding files) use the same logic here instead of duplicating it.
package globutil

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Match reports whether target matches pattern.
// Supports ** for arbitrary-depth directory matching.
func Match(pattern, target string) bool {
	if pattern == target {
		return true
	}
	if strings.Contains(pattern, "**") {
		return doubleStarMatch(pattern, target)
	}
	matched, _ := filepath.Match(pattern, target)
	return matched
}

func doubleStarMatch(pattern, target string) bool {
	parts := strings.SplitN(pattern, "**", 2)
	prefix, suffix := parts[0], strings.TrimPrefix(parts[1], "/")

	if prefix != "" {
		if !strings.HasPrefix(target, prefix) {
			return false
		}
		target = target[len(prefix):]
	}
	if suffix == "" {
		return true
	}
	if strings.HasSuffix(target, suffix) {
		return true
	}
	segments := strings.Split(target, "/")
	for i := range segments {
		candidate := strings.Join(segments[i:], "/")
		if matched, _ := filepath.Match(suffix, candidate); matched {
			return true
		}
	}
	return false
}

// Glob returns all files matching pattern, relative to the current
// working directory. Patterns without "**" go through stdlib filepath.Glob
// (faster, handles absolute/anchored patterns correctly). Patterns with
// "**" walk the filesystem and match each file against the pattern.
func Glob(pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(pattern)
	}

	var matches []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable entries (permissions, broken symlinks, etc.)
			// rather than aborting the whole search.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		clean := filepath.ToSlash(filepath.Clean(path))
		if Match(pattern, clean) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}
