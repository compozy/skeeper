package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/compozy/skeeper/internal/managedblock"
)

// UpdateGitignore appends or replaces skeeper's managed ignore block.
func UpdateGitignore(root string, patterns []string) error {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	block := buildGitignoreBlock(patterns)
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

func buildGitignoreBlock(patterns []string) string {
	var b strings.Builder
	b.WriteString(managedblock.SkeeperGitignoreBegin)
	b.WriteByte('\n')
	b.WriteString(DirName)
	b.WriteString("/\n")
	for _, pattern := range patterns {
		b.WriteString(strings.TrimSpace(pattern))
		b.WriteByte('\n')
	}
	b.WriteString(managedblock.SkeeperGitignoreEnd)
	b.WriteByte('\n')
	return b.String()
}
