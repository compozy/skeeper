package sidecar

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/config"
)

func TestUpdateGitignoreWritesNamespacePatternsAndEffectiveExcludes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	initGitignoreRepo(t, root)
	namespaces := []config.Namespace{
		{Name: "skills", Patterns: []string{"skills/*.md"}},
		{Name: "repo", Patterns: []string{"**/*.md"}, Exclude: []string{"drafts/**", "skills/*.md"}},
	}

	if err := UpdateGitignore(root, namespaces); err != nil {
		t.Fatalf("update gitignore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		".skeeper/",
		"skills/*.md",
		"**/*.md",
		"!drafts/**",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected gitignore to contain %q, got:\n%s", want, content)
		}
	}
	assertGitignoreIgnored(t, root, "skills/review.md")
	assertGitignoreIgnored(t, root, "src/auth/SPEC.md")
	assertGitignoreVisible(t, root, "drafts/notes.md")
}

func TestUpdateGitignoreReignoresSpecificNamespaceInsideExcludedTree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	initGitignoreRepo(t, root)
	namespaces := []config.Namespace{
		{Name: "repo", Patterns: []string{"**/*.md"}, Exclude: []string{"drafts/**"}},
		{Name: "drafts", Patterns: []string{"drafts/specific/*.md"}},
	}

	if err := UpdateGitignore(root, namespaces); err != nil {
		t.Fatalf("update gitignore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"**/*.md",
		"!drafts/**",
		"drafts/specific/*.md",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected gitignore to contain %q, got:\n%s", want, content)
		}
	}
	assertGitignoreVisible(t, root, "drafts/other.md")
	assertGitignoreIgnored(t, root, "drafts/specific/owned.md")
}

func assertGitignoreIgnored(t *testing.T, root, rel string) {
	t.Helper()
	writeGitignoreCandidate(t, root, rel)
	cmd := exec.CommandContext(context.Background(), "git", "check-ignore", rel)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected %s to be ignored, err=%v out=%s", rel, err, string(out))
	}
}

func initGitignoreRepo(t *testing.T, root string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "init", "-b", "main")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init git repo: %v out=%s", err, string(out))
	}
}

func assertGitignoreVisible(t *testing.T, root, rel string) {
	t.Helper()
	writeGitignoreCandidate(t, root, rel)
	cmd := exec.CommandContext(context.Background(), "git", "check-ignore", rel)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected %s to be visible, check-ignore output=%s", rel, string(out))
	}
}

func writeGitignoreCandidate(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create candidate dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("candidate\n"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
}
