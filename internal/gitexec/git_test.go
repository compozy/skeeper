package gitexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExecRunnerCommandErrorCapturesExitCode(t *testing.T) {
	t.Parallel()

	_, err := (&ExecRunner{}).Run(context.Background(), t.TempDir(), "sh", "-c", "exit 7")
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("expected CommandError, got %T: %v", err, err)
	}
	if commandErr.ExitCode != 7 {
		t.Fatalf("expected exit code 7, got %d", commandErr.ExitCode)
	}
}

func TestGitLocalOperationsWithGoGit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := newRepo(t)
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-m", "bootstrap")
	subdir := filepath.Join(root, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	helper := NewGit()
	gotRoot, err := helper.Root(ctx, subdir)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval root symlinks: %v", err)
	}
	gotRoot, err = filepath.EvalSymlinks(gotRoot)
	if err != nil {
		t.Fatalf("eval got root symlinks: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("root mismatch: got %q want %q", gotRoot, wantRoot)
	}
	gitDir, err := helper.GitDir(ctx, root)
	if err != nil {
		t.Fatalf("git dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
		t.Fatalf("expected git dir HEAD: %v", err)
	}
	branch, err := helper.CurrentBranch(ctx, root)
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	if branch != "main" {
		t.Fatalf("branch mismatch: got %q", branch)
	}
	sha, err := helper.HeadSHA(ctx, root)
	if err != nil {
		t.Fatalf("head sha: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("expected full SHA, got %q", sha)
	}
	short, err := helper.HeadShortSHA(ctx, root)
	if err != nil {
		t.Fatalf("head short sha: %v", err)
	}
	if len(short) != 7 {
		t.Fatalf("expected short SHA, got %q", short)
	}
	subject, err := helper.HeadSubject(ctx, root)
	if err != nil {
		t.Fatalf("head subject: %v", err)
	}
	if subject != "bootstrap" {
		t.Fatalf("subject mismatch: got %q", subject)
	}
	if !helper.HasHead(ctx, root) {
		t.Fatal("expected HEAD")
	}
	if !helper.RefExists(ctx, root, "refs/heads/main") {
		t.Fatal("expected main ref")
	}
	info, err := helper.LastCommit(ctx, root)
	if err != nil {
		t.Fatalf("last commit: %v", err)
	}
	if info.ShortHash == "" || info.Unix == 0 {
		t.Fatalf("unexpected commit info: %#v", info)
	}
	dirty, err := helper.IsDirty(ctx, root)
	if err != nil {
		t.Fatalf("dirty clean repo: %v", err)
	}
	if dirty {
		t.Fatal("expected clean repo")
	}
	writeFile(t, root, "TODO.md", "todo\n")
	dirty, err = helper.IsDirty(ctx, root)
	if err != nil {
		t.Fatalf("dirty after write: %v", err)
	}
	if !dirty {
		t.Fatal("expected dirty repo")
	}
	if err := helper.AddAll(ctx, root); err != nil {
		t.Fatalf("add all: %v", err)
	}
	dirty, err = helper.IsDirty(ctx, root)
	if err != nil {
		t.Fatalf("dirty after add: %v", err)
	}
	if !dirty {
		t.Fatal("expected staged repo to be dirty")
	}
}

func TestGitAheadBehindCounts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := newRepo(t)
	writeFile(t, root, "README.md", "base\n")
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-m", "base")
	base := gitOutput(t, root, "rev-parse", "HEAD")

	writeFile(t, root, "local.md", "local\n")
	git(t, root, "add", "local.md")
	git(t, root, "commit", "-m", "local")
	git(t, root, "update-ref", "refs/remotes/origin/main", base)

	helper := NewGit()
	ahead, behind, err := helper.AheadBehind(ctx, root, "HEAD", "refs/remotes/origin/main")
	if err != nil {
		t.Fatalf("ahead behind: %v", err)
	}
	if ahead != 1 || behind != 0 {
		t.Fatalf("ahead/behind mismatch: got %d/%d", ahead, behind)
	}

	tree := gitOutput(t, root, "rev-parse", "HEAD^{tree}")
	remote := gitOutput(t, root, "commit-tree", tree, "-p", base, "-m", "remote")
	git(t, root, "update-ref", "refs/remotes/origin/main", remote)
	ahead, behind, err = helper.AheadBehind(ctx, root, "HEAD", "refs/remotes/origin/main")
	if err != nil {
		t.Fatalf("diverged ahead behind: %v", err)
	}
	if ahead != 1 || behind != 1 {
		t.Fatalf("diverged ahead/behind mismatch: got %d/%d", ahead, behind)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "--initial-branch=main")
	git(t, root, "config", "user.name", "Skeeper Test")
	git(t, root, "config", "user.email", "skeeper@example.com")
	return root
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitOutput(t, dir, args...)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, string(out))
	}
	return trimTrailingNewline(string(out))
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

func trimTrailingNewline(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
