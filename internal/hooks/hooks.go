// Package hooks installs the local Git hook that triggers skeeper sync.
package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	beginMarker = "# >>> skeeper post-commit hook >>>"
	endMarker   = "# <<< skeeper post-commit hook <<<"
	hookBody    = beginMarker + `
if command -v skeeper >/dev/null 2>&1; then
  skeeper sync --hook || true
else
  echo "skeeper: command not found, run 'skeeper sync' after installing skeeper" >&2
fi
` + endMarker + `
`
)

// InstallPostCommit appends or replaces skeeper's managed post-commit block.
func InstallPostCommit(gitDir string) error {
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}
	path := filepath.Join(hooksDir, "post-commit")

	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read post-commit hook: %w", err)
	}

	next := replaceManagedBlock(string(content))
	if strings.TrimSpace(next) == "" {
		next = "#!/bin/sh\n\n" + hookBody
	} else {
		if !strings.HasPrefix(next, "#!") {
			next = "#!/bin/sh\n\n" + next
		}
		if !strings.HasSuffix(next, "\n") {
			next += "\n"
		}
		next += "\n" + hookBody
	}

	if err := writeFile(path, []byte(next), 0o600); err != nil {
		return fmt.Errorf("write post-commit hook: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("mark post-commit hook executable: %w", err)
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

func replaceManagedBlock(content string) string {
	start := strings.Index(content, beginMarker)
	if start == -1 {
		return content
	}
	end := strings.Index(content[start:], endMarker)
	if end == -1 {
		return content
	}
	end += start + len(endMarker)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	trimmed := content[:start] + content[end:]
	return strings.TrimRight(trimmed, "\n")
}
