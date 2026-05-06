package matcher

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFindMatchesIgnoredAndHiddenSpecFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, root, "src/auth/SPEC.md", "auth")
	writeFile(t, root, "docs/specs/onboarding.md", "docs")
	writeFile(t, root, ".claude/plans/q2.md", "plan")
	writeFile(t, root, ".skeeper/src/auth/SPEC.md", "sidecar")
	writeFile(t, root, ".git/SPEC.md", "git")
	writeFile(t, root, "README.md", "readme")

	got, err := Find(root, []string{"**/SPEC.md", "docs/specs/**", ".claude/plans/**"})
	if err != nil {
		t.Fatalf("find matches: %v", err)
	}
	want := []string{
		".claude/plans/q2.md",
		"docs/specs/onboarding.md",
		"src/auth/SPEC.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matches mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestFindRejectsInvalidPattern(t *testing.T) {
	t.Parallel()

	if _, err := Find(t.TempDir(), []string{"["}); err == nil {
		t.Fatal("expected invalid pattern error, got nil")
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
