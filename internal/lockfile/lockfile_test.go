package lockfile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/reconcile"
)

func TestStoreWriteLoadCanonicalizesAndValidates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(&gitexec.ExecRunner{})
	lock := Lock{
		Version:      Version,
		Sidecar:      "https://github.com/user/project-specs",
		SourceBranch: "main",
		Namespaces: []NamespaceRecord{
			{
				Name:          "project",
				SidecarBranch: "project/__branches__/main",
				Commit:        "0123456789abcdef0123456789abcdef01234567",
				Digest:        "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				Files:         1,
				Bytes:         12,
			},
		},
	}
	if err := store.Write(reconcile.RepoRoot(root), lock); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, Filename))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if !strings.Contains(string(data), `"sidecar": "git@github.com:user/project-specs.git"`) {
		t.Fatalf("expected canonical sidecar URL, got:\n%s", string(data))
	}
	loaded, err := store.Load(reconcile.RepoRoot(root))
	if err != nil {
		t.Fatalf("load lock: %v", err)
	}
	if loaded.Sidecar != "git@github.com:user/project-specs.git" {
		t.Fatalf("loaded sidecar mismatch: %#v", loaded)
	}
}

func TestValidateRejectsMalformedLock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*Lock)
	}{
		{
			name: "bad version",
			edit: func(lock *Lock) { lock.Version = 99 },
		},
		{
			name: "empty sidecar",
			edit: func(lock *Lock) { lock.Sidecar = "" },
		},
		{
			name: "empty source branch",
			edit: func(lock *Lock) { lock.SourceBranch = "" },
		},
		{
			name: "empty namespace",
			edit: func(lock *Lock) { lock.Namespaces[0].Name = "" },
		},
		{
			name: "duplicate namespace",
			edit: func(lock *Lock) {
				lock.Namespaces = append(lock.Namespaces, lock.Namespaces[0])
			},
		},
		{
			name: "short commit",
			edit: func(lock *Lock) { lock.Namespaces[0].Commit = "abc123" },
		},
		{
			name: "zero commit",
			edit: func(lock *Lock) { lock.Namespaces[0].Commit = "0000000000000000000000000000000000000000" },
		},
		{
			name: "short digest",
			edit: func(lock *Lock) { lock.Namespaces[0].Digest = "sha256:abcdef" },
		},
		{
			name: "non hex digest",
			edit: func(lock *Lock) {
				lock.Namespaces[0].Digest = "sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
			},
		},
		{
			name: "negative files",
			edit: func(lock *Lock) { lock.Namespaces[0].Files = -1 },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lock := validLock()
			tt.edit(&lock)
			if err := Validate(lock); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDigestResultIsStableForNamespaceTree(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	sidecarDir := t.TempDir()
	git(t, sidecarDir, "init", "-b", "project/__branches__/main")
	writeFile(t, sidecarDir, "project/b/SPEC.md", "b\n")
	writeFile(t, sidecarDir, "project/a/SPEC.md", "a\n")
	git(t, sidecarDir, "add", ".")
	git(t, sidecarDir, "commit", "-m", "sync namespace project")

	store := NewStore(&gitexec.ExecRunner{})
	namespace := config.Namespace{Name: "project", Patterns: []string{"**/SPEC.md"}}
	left, err := store.DigestResult(ctx, sidecarDir, namespace, reconcile.SidecarRef("HEAD"))
	if err != nil {
		t.Fatalf("digest left: %v", err)
	}
	right, err := store.DigestResult(ctx, sidecarDir, namespace, reconcile.SidecarRef("HEAD"))
	if err != nil {
		t.Fatalf("digest right: %v", err)
	}
	if left != right {
		t.Fatalf("digest not stable: %#v != %#v", left, right)
	}
	if left.Files != 2 || left.Bytes != 4 || !strings.HasPrefix(string(left.Digest), "sha256:") {
		t.Fatalf("unexpected digest result: %#v", left)
	}
}

func TestDigestWorkingTreeUsesStagedContent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, root, "src/SPEC.md", "worktree\n")
	result, err := DigestWorkingTree(root, []string{"src/SPEC.md"}, map[string]string{"src/SPEC.md": "staged\n"})
	if err != nil {
		t.Fatalf("digest working tree: %v", err)
	}
	worktree, err := DigestWorkingTree(root, []string{"src/SPEC.md"}, nil)
	if err != nil {
		t.Fatalf("digest unstaged: %v", err)
	}
	if result.Digest == worktree.Digest {
		t.Fatalf("expected staged content to affect digest: staged=%s worktree=%s", result.Digest, worktree.Digest)
	}
}

func validLock() Lock {
	return Lock{
		Version:      Version,
		Sidecar:      "git@github.com:user/project-specs.git",
		SourceBranch: "main",
		Namespaces: []NamespaceRecord{
			{
				Name:          "project",
				SidecarBranch: "project/__branches__/main",
				Commit:        "0123456789abcdef0123456789abcdef01234567",
				Digest:        "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				Files:         1,
				Bytes:         12,
			},
		},
	}
}

func setGitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "skeeper tests")
	t.Setenv("GIT_AUTHOR_EMAIL", "skeeper@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "skeeper tests")
	t.Setenv("GIT_COMMITTER_EMAIL", "skeeper@example.com")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
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
