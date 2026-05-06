// Package managedblock updates marker-delimited file sections.
package managedblock

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// SkeeperGitignoreBegin marks the start of skeeper's managed .gitignore block.
	SkeeperGitignoreBegin = "# >>> skeeper ignored specs >>>"
	// SkeeperGitignoreEnd marks the end of skeeper's managed .gitignore block.
	SkeeperGitignoreEnd = "# <<< skeeper ignored specs <<<"
)

// Replace removes the block between begin and end markers from content.
func Replace(content, begin, end string) string {
	start := strings.Index(content, begin)
	if start == -1 {
		return content
	}
	finish := strings.Index(content[start:], end)
	if finish == -1 {
		return content
	}
	finish += start + len(end)
	if finish < len(content) && content[finish] == '\n' {
		finish++
	}
	return strings.TrimRight(content[:start]+content[finish:], "\n")
}

// WriteFile writes data to path, syncs it, and closes the file.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
