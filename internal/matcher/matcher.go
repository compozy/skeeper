// Package matcher discovers project files from skeeper glob patterns.
package matcher

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

var excludedPrefixes = []string{
	".git/",
	".skeeper/",
}

// Find returns sorted slash-separated relative file paths that match patterns.
func Find(root string, patterns []string) ([]string, error) {
	fsys := os.DirFS(root)
	seen := make(map[string]struct{})
	for _, raw := range patterns {
		pattern := normalizePattern(raw)
		if !doublestar.ValidatePathPattern(pattern) {
			return nil, fmt.Errorf("invalid glob pattern %q", raw)
		}
		matches, err := doublestar.Glob(fsys, pattern, doublestar.WithFilesOnly())
		if err != nil {
			return nil, fmt.Errorf("match pattern %q: %w", raw, err)
		}
		for _, match := range matches {
			path := filepath.ToSlash(filepath.Clean(match))
			if path == "." || excluded(path) {
				continue
			}
			seen[path] = struct{}{}
		}
	}

	files := make([]string, 0, len(seen))
	for path := range seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func normalizePattern(pattern string) string {
	trimmed := strings.TrimSpace(filepath.ToSlash(pattern))
	trimmed = strings.TrimPrefix(trimmed, "./")
	return trimmed
}

func excluded(path string) bool {
	for _, prefix := range excludedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
