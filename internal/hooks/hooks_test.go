package hooks

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

func TestGitManagerInstallMigratesLegacyPostCommitAndKeepsPreCommitLast(t *testing.T) {
	t.Parallel()

	root := newHookRepo(t)
	hooksDir := filepath.Join(root, ".git", "hooks")
	preCommitPath := filepath.Join(hooksDir, "pre-commit")
	postCommitPath := filepath.Join(hooksDir, "post-commit")
	if err := os.WriteFile(preCommitPath, []byte("#!/bin/sh\n\necho husky\n"), 0o755); err != nil {
		t.Fatalf("write pre-commit: %v", err)
	}
	legacyPostCommit := "#!/bin/sh\n\necho keep\n\n" +
		postCommitBegin + "\nskeeper sync || true\n" + postCommitEnd + "\n"
	if err := os.WriteFile(postCommitPath, []byte(legacyPostCommit), 0o755); err != nil {
		t.Fatalf("write post-commit: %v", err)
	}

	result, err := NewManager(&gitexec.ExecRunner{}).Install(
		context.Background(),
		reconcile.RepoRoot(root),
		InstallOptions{Config: config.Config{}},
	)
	if err != nil {
		t.Fatalf("install hooks: %v", err)
	}
	if len(result.RemovedLegacy) != 1 || filepath.Base(result.RemovedLegacy[0]) != filepath.Base(postCommitPath) {
		t.Fatalf("expected legacy post-commit migration, got %#v", result.RemovedLegacy)
	}

	preCommit := readFile(t, preCommitPath)
	if !strings.Contains(preCommit, "echo husky") {
		t.Fatalf("pre-commit lost existing content:\n%s", preCommit)
	}
	if !strings.HasSuffix(strings.TrimSpace(preCommit), preCommitEnd) {
		t.Fatalf("skeeper pre-commit block must be last:\n%s", preCommit)
	}
	if !strings.Contains(preCommit, "set -eu") ||
		!strings.Contains(preCommit, "bypass requested but audit record failed") {
		t.Fatalf("pre-commit bypass must fail closed:\n%s", preCommit)
	}
	preMerge := readFile(t, filepath.Join(hooksDir, "pre-merge-commit"))
	if !strings.Contains(preMerge, "skeeper internal pre-commit") ||
		!strings.Contains(preMerge, "pre-merge-commit bypass") {
		t.Fatalf("pre-merge-commit hook mismatch:\n%s", preMerge)
	}
	if got := readFile(t, postCommitPath); strings.Contains(got, postCommitBegin) ||
		!strings.Contains(got, "echo keep") {
		t.Fatalf("post-commit migration mismatch:\n%s", got)
	}

	check, err := NewManager(&gitexec.ExecRunner{}).Check(context.Background(), reconcile.RepoRoot(root))
	if err != nil {
		t.Fatalf("check hooks: %v", err)
	}
	if !check.OK {
		t.Fatalf("expected hooks ok: %#v", check)
	}
}

func TestGitManagerCheckRejectsContentAfterPreCommitBlock(t *testing.T) {
	t.Parallel()

	root := newHookRepo(t)
	manager := NewManager(&gitexec.ExecRunner{})
	if _, err := manager.Install(
		context.Background(),
		reconcile.RepoRoot(root),
		InstallOptions{Config: config.Config{}},
	); err != nil {
		t.Fatalf("install hooks: %v", err)
	}
	preCommitPath := filepath.Join(root, ".git", "hooks", "pre-commit")
	appendFile(t, preCommitPath, "\necho formatter-after-skeeper\n")

	check, err := manager.Check(context.Background(), reconcile.RepoRoot(root))
	if err != nil {
		t.Fatalf("check hooks: %v", err)
	}
	if check.OK || len(check.Diagnostics) == 0 ||
		!strings.Contains(check.Diagnostics[0], "content after the skeeper block") {
		t.Fatalf("expected trailing-content diagnostic: %#v", check)
	}
}

func TestGitManagerCheckRequiresManagedAttributesBlock(t *testing.T) {
	t.Parallel()

	root := newHookRepo(t)
	manager := NewManager(&gitexec.ExecRunner{})
	if _, err := manager.Install(
		context.Background(),
		reconcile.RepoRoot(root),
		InstallOptions{Config: config.Config{}},
	); err != nil {
		t.Fatalf("install hooks: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".gitattributes"),
		[]byte("skeeper.lock merge=skeeper-lock\n"),
		0o644,
	); err != nil {
		t.Fatalf("rewrite .gitattributes: %v", err)
	}

	check, err := manager.Check(context.Background(), reconcile.RepoRoot(root))
	if err != nil {
		t.Fatalf("check hooks: %v", err)
	}
	if check.OK || len(check.Diagnostics) == 0 ||
		!strings.Contains(check.Diagnostics[0], ".gitattributes") {
		t.Fatalf("expected managed attributes diagnostic: %#v", check)
	}
}

func TestGitManagerCheckRequiresMergeDriverConfig(t *testing.T) {
	t.Parallel()

	root := newHookRepo(t)
	manager := NewManager(&gitexec.ExecRunner{})
	if _, err := manager.Install(
		context.Background(),
		reconcile.RepoRoot(root),
		InstallOptions{Config: config.Config{}},
	); err != nil {
		t.Fatalf("install hooks: %v", err)
	}
	git(t, root, "config", "--unset", "merge.skeeper-lock.driver")

	check, err := manager.Check(context.Background(), reconcile.RepoRoot(root))
	if err != nil {
		t.Fatalf("check hooks: %v", err)
	}
	if check.OK || !diagnosticsContain(check.Diagnostics, "merge_driver_unconfigured") {
		t.Fatalf("expected merge-driver config diagnostic: %#v", check)
	}
}

func TestStrictHookBodies(t *testing.T) {
	t.Parallel()

	preCommit := preCommitBody(config.DefaultAllowSkipEnv)
	for _, want := range []string{
		"set -eu",
		"skeeper internal record-bypass",
		"bypass requested but audit record failed",
		"skeeper internal pre-commit",
	} {
		if !strings.Contains(preCommit, want) {
			t.Fatalf("pre-commit body missing %q:\n%s", want, preCommit)
		}
	}
	prePush := prePushBody()
	if !strings.Contains(prePush, "set -eu") || !strings.Contains(prePush, "skeeper internal pre-push") {
		t.Fatalf("pre-push body mismatch:\n%s", prePush)
	}
	preMerge := preMergeCommitBody(config.DefaultAllowSkipEnv)
	if !strings.Contains(preMerge, "set -eu") || !strings.Contains(preMerge, "skeeper internal pre-commit") {
		t.Fatalf("pre-merge-commit body mismatch:\n%s", preMerge)
	}
}

func diagnosticsContain(diagnostics []string, want string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, want) {
			return true
		}
	}
	return false
}

func newHookRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "--initial-branch=main")
	return root
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		t.Fatalf("append %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
