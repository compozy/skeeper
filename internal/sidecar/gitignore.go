package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	gitignoreBegin = "# >>> skeeper ignored specs >>>"
	gitignoreEnd   = "# <<< skeeper ignored specs <<<"
)

// UpdateGitignore appends or replaces skeeper's managed ignore block.
func UpdateGitignore(root string, patterns []string) error {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	block := buildGitignoreBlock(patterns)
	content := replaceGitignoreBlock(string(data))
	if strings.TrimSpace(content) == "" {
		content = block
	} else {
		content = strings.TrimRight(content, "\n") + "\n\n" + block
	}
	if err := writeFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return nil
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func buildGitignoreBlock(patterns []string) string {
	var b strings.Builder
	b.WriteString(gitignoreBegin)
	b.WriteByte('\n')
	b.WriteString(DirName)
	b.WriteString("/\n")
	for _, pattern := range patterns {
		b.WriteString(strings.TrimSpace(pattern))
		b.WriteByte('\n')
	}
	b.WriteString(gitignoreEnd)
	b.WriteByte('\n')
	return b.String()
}

func replaceGitignoreBlock(content string) string {
	start := strings.Index(content, gitignoreBegin)
	if start == -1 {
		return content
	}
	end := strings.Index(content[start:], gitignoreEnd)
	if end == -1 {
		return content
	}
	end += start + len(gitignoreEnd)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return strings.TrimRight(content[:start]+content[end:], "\n")
}
