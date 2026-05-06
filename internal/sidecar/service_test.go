package sidecar_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/sidecar"
)

func TestServiceSyncHydrateStatusAndLogWithRealGit(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := config.Config{
		Sidecar:  remote,
		Patterns: []string{"**/SPEC.md"},
	}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := sidecar.UpdateGitignore(root, cfg.Patterns); err != nil {
		t.Fatalf("update gitignore: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename, ".gitignore")
	git(t, root, "commit", "-m", "bootstrap")

	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.Sync(ctx, root, sidecar.SyncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !result.Committed {
		t.Fatal("expected sidecar commit")
	}
	if result.ChangedFiles != 1 {
		t.Fatalf("expected 1 changed file, got %d", result.ChangedFiles)
	}
	assertFile(t, filepath.Join(root, sidecar.DirName, "src/auth/SPEC.md"), "# Auth\n")

	logOutput, err := service.Log(ctx, root, "src/auth/SPEC.md")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if !strings.Contains(logOutput, "sync ") || !strings.Contains(logOutput, "bootstrap") {
		t.Fatalf("expected sync history, got %q", logOutput)
	}
	fullCommit := gitOutput(t, filepath.Join(root, sidecar.DirName), "log", "-1", "--format=%B")
	mainSHA := gitOutput(t, root, "rev-parse", "HEAD")
	if !strings.Contains(fullCommit, "Main-Commit: "+mainSHA) {
		t.Fatalf("expected main commit metadata in sidecar commit:\n%s", fullCommit)
	}

	status, err := service.Status(ctx, root)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Sidecar != remote || status.Branch != "main" || status.TrackedFiles != 1 || status.PendingSync != 0 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.LastCommit == "" {
		t.Fatal("expected last sidecar commit in status")
	}

	if err := os.RemoveAll(filepath.Join(root, sidecar.DirName)); err != nil {
		t.Fatalf("remove sidecar clone: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove main spec: %v", err)
	}
	hydrated, err := service.Hydrate(ctx, root)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(hydrated.Restored) != 1 || hydrated.Restored[0] != "src/auth/SPEC.md" {
		t.Fatalf("unexpected hydrate result: %#v", hydrated)
	}
	assertFile(t, filepath.Join(root, "src/auth/SPEC.md"), "# Auth\n")

	statusOutput := gitOutput(t, root, "status", "--short", "--ignored", "src/auth/SPEC.md")
	if !strings.Contains(statusOutput, "!! src/auth/SPEC.md") {
		t.Fatalf("expected main repo to ignore spec file, got %q", statusOutput)
	}
}

func TestServiceSyncMirrorsDeletes(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := config.Config{Sidecar: remote, Patterns: []string{"**/SPEC.md"}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename)
	git(t, root, "commit", "-m", "bootstrap")

	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove spec: %v", err)
	}
	result, err := service.Sync(ctx, root, sidecar.SyncOptions{})
	if err != nil {
		t.Fatalf("delete sync: %v", err)
	}
	if !result.Committed {
		t.Fatal("expected delete sync commit")
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName, "src/auth/SPEC.md")); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar spec to be removed, stat err=%v", err)
	}
}

func TestServiceHookSyncQueuesFailureWithoutReturningError(t *testing.T) {
	root := newMainRepo(t)
	cfg := config.Config{Sidecar: filepath.Join(t.TempDir(), "missing.git"), Patterns: []string{"**/SPEC.md"}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.Sync(context.Background(), root, sidecar.SyncOptions{Hook: true})
	if err != nil {
		t.Fatalf("hook sync must not return an error, got %v", err)
	}
	if !result.Queued {
		t.Fatal("expected failed hook sync to be queued")
	}
	queuePath := filepath.Join(root, ".git", "skeeper", "queue.json")
	data, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if !strings.Contains(string(data), "clone sidecar") {
		t.Fatalf("expected clone failure reason in queue, got %s", string(data))
	}
}

func TestServiceHookSyncReportsQueueWriteFailureWithoutReturningError(t *testing.T) {
	root := newMainRepo(t)
	cfg := config.Config{Sidecar: filepath.Join(t.TempDir(), "missing.git"), Patterns: []string{"**/SPEC.md"}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "skeeper"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write state path blocker: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.Sync(context.Background(), root, sidecar.SyncOptions{Hook: true})
	if err != nil {
		t.Fatalf("hook sync must not return an error, got %v", err)
	}
	if !result.QueueFailed {
		t.Fatalf("expected queue failure to be reported, got %#v", result)
	}
	if result.QueueError == "" {
		t.Fatal("expected queue failure error message")
	}
}

func TestServiceInitUsesExistingCompatibleConfigIdempotently(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := config.Config{
		Sidecar:   remote,
		Bootstrap: "brew install skeeper",
		Patterns:  []string{"**/SPEC.md"},
	}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.Init(ctx, root, sidecar.InitOptions{
		SidecarName: "sidecar",
		Bootstrap:   "brew install skeeper",
		Patterns:    []string{"**/SPEC.md"},
	})
	if err != nil {
		t.Fatalf("idempotent init: %v", err)
	}
	if result.Config.Sidecar != remote {
		t.Fatalf("expected existing sidecar to remain %q, got %q", remote, result.Config.Sidecar)
	}
	reloaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.Sidecar != cfg.Sidecar || reloaded.Bootstrap != cfg.Bootstrap ||
		!sameStrings(reloaded.Patterns, cfg.Patterns) {
		t.Fatalf("config changed unexpectedly: %#v", reloaded)
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName, ".git")); err != nil {
		t.Fatalf("expected sidecar clone to exist: %v", err)
	}
	hook, err := os.ReadFile(filepath.Join(root, ".git", "hooks", "post-commit"))
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if !strings.Contains(string(hook), "skeeper sync --hook") {
		t.Fatalf("expected hook to be installed, got %s", string(hook))
	}
}

func TestServiceInitRejectsIncompatibleExistingConfig(t *testing.T) {
	root := newMainRepo(t)
	cfg := config.Config{Sidecar: filepath.Join(t.TempDir(), "sidecar.git"), Patterns: []string{"**/SPEC.md"}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	_, err := service.Init(context.Background(), root, sidecar.InitOptions{
		SidecarName: "other-specs",
		Patterns:    []string{"docs/**"},
	})
	if err == nil {
		t.Fatal("expected incompatible config error, got nil")
	}
	if !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("expected incompatible config error, got %v", err)
	}
	got, err := config.Load(root)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got.Sidecar != cfg.Sidecar || got.Patterns[0] != cfg.Patterns[0] {
		t.Fatalf("config was modified: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName)); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar dir not to be created, stat err=%v", err)
	}
}

func TestServiceStatusAndLogRequireExistingSidecarClone(t *testing.T) {
	root := newMainRepo(t)
	cfg := config.Config{Sidecar: filepath.Join(t.TempDir(), "sidecar.git"), Patterns: []string{"**/SPEC.md"}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Status(context.Background(), root); err == nil || !strings.Contains(err.Error(), "hydrate") {
		t.Fatalf("expected status to require hydrate, got %v", err)
	}
	if _, err := service.Log(
		context.Background(),
		root,
		"src/auth/SPEC.md",
	); err == nil ||
		!strings.Contains(err.Error(), "hydrate") {
		t.Fatalf("expected log to require hydrate, got %v", err)
	}
}

func TestServiceLogRejectsPathOutsideProjectRoot(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := config.Config{Sidecar: remote, Patterns: []string{"**/SPEC.md"}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename)
	git(t, root, "commit", "-m", "bootstrap")
	if _, err := sidecar.New(&gitexec.ExecRunner{}).Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	_, err := sidecar.New(&gitexec.ExecRunner{}).Log(ctx, root, "../outside/SPEC.md")
	if err == nil {
		t.Fatal("expected path traversal error, got nil")
	}
	if !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("expected outside-root error, got %v", err)
	}
}

func newMainRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	return root
}

func newBareRepo(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "sidecar.git")
	git(t, "", "init", "--bare", "--initial-branch=main", remote)
	return remote
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
	_ = gitOutput(t, dir, args...)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
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

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("file %s mismatch: got %q want %q", path, string(data), want)
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
