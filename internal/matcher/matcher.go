// Package matcher discovers project files from skeeper glob patterns.
package matcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/compozy/skeeper/internal/managedblock"
	"github.com/git-pkgs/gitignore"
)

var excludedDirs = map[string]struct{}{
	".git":     {},
	".skeeper": {},
}

const globalExcludesProbeTimeout = time.Second

// Options controls project file discovery.
type Options struct {
	RespectGitignore bool
}

// Find returns sorted slash-separated relative file paths that match patterns.
func Find(root string, patterns []string) ([]string, error) {
	return FindContext(context.Background(), root, patterns)
}

// FindContext returns sorted slash-separated relative file paths that match patterns.
func FindContext(ctx context.Context, root string, patterns []string) ([]string, error) {
	return FindContextWithOptions(ctx, root, patterns, Options{RespectGitignore: true})
}

// FindContextWithOptions returns sorted slash-separated relative file paths
// that match patterns and discovery options.
func FindContextWithOptions(ctx context.Context, root string, patterns []string, opts Options) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, err := normalizePatterns(patterns)
	if err != nil {
		return nil, err
	}
	var ignored *gitignore.Matcher
	if opts.RespectGitignore {
		ignored, err = newIgnoredMatcher(ctx, root)
		if err != nil {
			return nil, err
		}
	}
	seen := make(map[string]struct{})

	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if entry.IsDir() {
			if excluded(rel) || ignoredPath(ignored, rel, true) {
				return filepath.SkipDir
			}
			if ignored != nil {
				loadNestedIgnore(root, rel, ignored)
			}
			return nil
		}
		if excluded(rel) || ignoredPath(ignored, rel, false) {
			return nil
		}
		if matchesAny(rel, normalized) {
			seen[rel] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk project files: %w", err)
	}

	files := make([]string, 0, len(seen))
	for path := range seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func ignoredPath(ignored *gitignore.Matcher, rel string, isDir bool) bool {
	return ignored != nil && ignored.MatchPath(rel, isDir)
}

func newIgnoredMatcher(ctx context.Context, root string) (*gitignore.Matcher, error) {
	ignored := gitignore.New("")
	if global := globalExcludesFile(ctx); global != "" {
		ignored.AddFromFile(global, "")
	}
	ignored.AddFromFile(filepath.Join(root, ".git", "info", "exclude"), "")
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	switch {
	case err == nil:
		content := removeSkeeperIgnoreBlock(string(data))
		ignored.AddPatterns([]byte(content), "")
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, fmt.Errorf("read .gitignore: %w", err)
	}
	return ignored, nil
}

func loadNestedIgnore(root, rel string, ignored *gitignore.Matcher) {
	path := filepath.Join(root, filepath.FromSlash(rel), ".gitignore")
	if _, err := os.Stat(path); err == nil {
		ignored.AddFromFile(path, rel)
	}
}

func globalExcludesFile(ctx context.Context) string {
	cmdCtx, cancel := context.WithTimeout(ctx, globalExcludesProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "git", "config", "--global", "core.excludesfile").Output()
	if err == nil {
		path := strings.TrimSpace(string(out))
		if path != "" {
			return expandTilde(path)
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git", "ignore")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "git", "ignore")
}

func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

func removeSkeeperIgnoreBlock(content string) string {
	return managedblock.Replace(content, managedblock.SkeeperGitignoreBegin, managedblock.SkeeperGitignoreEnd)
}

func normalizePatterns(patterns []string) ([]string, error) {
	normalized := make([]string, 0, len(patterns))
	for _, raw := range patterns {
		pattern := normalizePattern(raw)
		if !doublestar.ValidatePathPattern(pattern) {
			return nil, fmt.Errorf("invalid glob pattern %q", raw)
		}
		normalized = append(normalized, pattern)
	}
	return normalized, nil
}

func normalizePattern(pattern string) string {
	trimmed := strings.TrimSpace(filepath.ToSlash(pattern))
	trimmed = strings.TrimPrefix(trimmed, "./")
	return trimmed
}

func excluded(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	_, ok := excludedDirs[first]
	return ok
}

func matchesAny(path string, patterns []string) bool {
	for _, pattern := range patterns {
		ok, err := doublestar.PathMatch(pattern, path)
		if err == nil && ok {
			return true
		}
	}
	return false
}
