package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPostCommitPreservesExistingHookAndIsIdempotent(t *testing.T) {
	t.Parallel()

	gitDir := filepath.Join(t.TempDir(), ".git")
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	path := filepath.Join(hooksDir, "post-commit")
	original := "#!/bin/sh\n\necho existing\n"
	if err := os.WriteFile(path, []byte(original), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	if err := InstallPostCommit(gitDir); err != nil {
		t.Fatalf("install hook: %v", err)
	}
	if err := InstallPostCommit(gitDir); err != nil {
		t.Fatalf("install hook again: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "echo existing") {
		t.Fatalf("existing hook content was not preserved:\n%s", content)
	}
	if count := strings.Count(content, beginMarker); count != 1 {
		t.Fatalf("expected one managed block, got %d:\n%s", count, content)
	}
	if !strings.Contains(content, "skeeper sync --hook || true") {
		t.Fatalf("expected non-blocking sync command:\n%s", content)
	}
}
