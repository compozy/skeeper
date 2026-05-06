// Package hooks installs the local Git hook that triggers skeeper sync.
package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/compozy/skeeper/internal/managedblock"
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

	next := managedblock.Replace(string(content), beginMarker, endMarker)
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

	if err := managedblock.WriteFile(path, []byte(next), 0o600); err != nil {
		return fmt.Errorf("write post-commit hook: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("mark post-commit hook executable: %w", err)
	}
	return nil
}
