package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/managedblock"
)

// UpdateGitignore appends or replaces skeeper's managed ignore block.
func UpdateGitignore(root string, namespaces []config.Namespace) error {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	block := buildGitignoreBlock(namespaces)
	content := managedblock.Replace(string(data), managedblock.SkeeperGitignoreBegin, managedblock.SkeeperGitignoreEnd)
	if strings.TrimSpace(content) == "" {
		content = block
	} else {
		content = strings.TrimRight(content, "\n") + "\n\n" + block
	}
	if err := managedblock.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return nil
}

func buildGitignoreBlock(namespaces []config.Namespace) string {
	var b strings.Builder
	b.WriteString(managedblock.SkeeperGitignoreBegin)
	b.WriteByte('\n')
	b.WriteString(DirName)
	b.WriteString("/\n")
	for _, pattern := range ignoredPatterns(namespaces) {
		b.WriteString(pattern)
		b.WriteByte('\n')
	}
	b.WriteString(managedblock.SkeeperGitignoreEnd)
	b.WriteByte('\n')
	return b.String()
}

func ignoredPatterns(namespaces []config.Namespace) []string {
	included := make([]string, 0)
	includedSet := make(map[string]struct{})
	excluded := make(map[string]struct{})
	patterns := make([]string, 0)
	for _, namespace := range namespaces {
		for _, pattern := range namespace.Patterns {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if _, ok := includedSet[pattern]; ok {
				continue
			}
			includedSet[pattern] = struct{}{}
			included = append(included, pattern)
			patterns = append(patterns, pattern)
		}
		for _, pattern := range namespace.Exclude {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			excluded[pattern] = struct{}{}
		}
	}
	excludedPatterns := make([]string, 0, len(excluded))
	for pattern := range excluded {
		excludedPatterns = append(excludedPatterns, pattern)
	}
	sort.Strings(excludedPatterns)
	for _, pattern := range excludedPatterns {
		patterns = append(patterns, "!"+pattern)
	}
	reincluded := make(map[string]struct{})
	for _, exclude := range excludedPatterns {
		for _, include := range included {
			if !includeRestoresIgnoredOwnership(exclude, include) {
				continue
			}
			if _, ok := reincluded[include]; ok {
				continue
			}
			reincluded[include] = struct{}{}
			patterns = append(patterns, include)
		}
	}
	return patterns
}

func includeRestoresIgnoredOwnership(exclude, include string) bool {
	if include == exclude {
		return true
	}
	excludePrefix := literalGlobPrefix(exclude)
	includePrefix := literalGlobPrefix(include)
	switch {
	case excludePrefix == "":
		return includePrefix != "" || globSpecificity(include) > globSpecificity(exclude)
	case includePrefix == "":
		return false
	case strings.HasPrefix(includePrefix, excludePrefix):
		return globSpecificity(include) > globSpecificity(exclude)
	default:
		return false
	}
}

func literalGlobPrefix(pattern string) string {
	idx := strings.IndexAny(pattern, "*?[{")
	if idx < 0 {
		return strings.TrimSuffix(pattern, "/")
	}
	prefix := pattern[:idx]
	if slash := strings.LastIndex(prefix, "/"); slash >= 0 {
		return prefix[:slash+1]
	}
	return ""
}

func globSpecificity(pattern string) int {
	specificity := 0
	for _, r := range pattern {
		switch r {
		case '*', '?', '[', ']', '{', '}', ',':
			continue
		default:
			specificity++
		}
	}
	return specificity
}
