package matcher

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/compozy/skeeper/internal/managedblock"
)

func TestFindMatchesIgnoredAndHiddenSpecFiles(t *testing.T) {
	setHermeticGitignoreEnv(t)
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

func TestFindRespectsProjectGitignoreRules(t *testing.T) {
	setHermeticGitignoreEnv(t)
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "node_modules/\ndist/\n*.generated.md\n!keep.generated.md\n")
	writeFile(t, root, "src/auth/SPEC.md", "auth")
	writeFile(t, root, "node_modules/pkg/SPEC.md", "node")
	writeFile(t, root, "dist/SPEC.md", "dist")
	writeFile(t, root, "skip.generated.md", "skip")
	writeFile(t, root, "keep.generated.md", "keep")

	got, err := Find(root, []string{"**/SPEC.md", "*.generated.md"})
	if err != nil {
		t.Fatalf("find matches: %v", err)
	}
	want := []string{
		"keep.generated.md",
		"src/auth/SPEC.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matches mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestFindIgnoresSkeeperManagedGitignoreBlock(t *testing.T) {
	setHermeticGitignoreEnv(t)
	root := t.TempDir()
	writeFile(
		t,
		root,
		".gitignore",
		"dist/\n\n"+managedblock.SkeeperGitignoreBegin+"\n.skeeper/\n**/SPEC.md\n"+
			managedblock.SkeeperGitignoreEnd+"\n",
	)
	writeFile(t, root, "src/auth/SPEC.md", "auth")
	writeFile(t, root, "dist/SPEC.md", "dist")

	got, err := Find(root, []string{"**/SPEC.md"})
	if err != nil {
		t.Fatalf("find matches: %v", err)
	}
	want := []string{"src/auth/SPEC.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matches mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestFindRespectsNestedGitignoreAndInfoExclude(t *testing.T) {
	setHermeticGitignoreEnv(t)
	root := t.TempDir()
	writeFile(t, root, ".git/info/exclude", "local-only/\n")
	writeFile(t, root, "src/auth/SPEC.md", "auth")
	writeFile(t, root, "nested/.gitignore", "SPEC.md\n")
	writeFile(t, root, "nested/SPEC.md", "nested")
	writeFile(t, root, "local-only/SPEC.md", "local")

	got, err := Find(root, []string{"**/SPEC.md"})
	if err != nil {
		t.Fatalf("find matches: %v", err)
	}
	want := []string{"src/auth/SPEC.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matches mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestFindRejectsInvalidPattern(t *testing.T) {
	setHermeticGitignoreEnv(t)
	if _, err := Find(t.TempDir(), []string{"["}); err == nil {
		t.Fatal("expected invalid pattern error, got nil")
	}
}

func setHermeticGitignoreEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
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
