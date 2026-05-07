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
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := sidecar.UpdateGitignore(root, cfg.Namespaces); err != nil {
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
	assertFile(t, filepath.Join(root, sidecar.DirName, "project/src/auth/SPEC.md"), "# Auth\n")

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
	if status.Sidecar != remote || status.Branch != "main" || len(status.Namespaces) != 1 || status.PendingSync != 0 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.Namespaces[0].TrackedFiles != 1 {
		t.Fatalf("expected 1 tracked file, got %#v", status.Namespaces[0])
	}
	if status.Namespaces[0].LastCommit == "" {
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
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
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
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName, "project/src/auth/SPEC.md")); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar spec to be removed, stat err=%v", err)
	}
}

func TestServiceSyncUsesMultipleNamespacesAndSidecarBranches(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	service := sidecar.New(&gitexec.ExecRunner{})
	repoA := newMainRepo(t)
	repoB := newMainRepo(t)

	bootstrapRepo(t, repoA, config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "skills", Patterns: []string{"skills/*.md"}},
			{Name: "repo", Patterns: []string{"**/*.md"}, Exclude: []string{"skills/*.md"}},
		},
	})
	bootstrapRepo(t, repoB, singleNamespaceConfig(remote, "repo-b", []string{"**/SPEC.md"}))

	writeFile(t, repoA, "skills/review.md", "# Skill\n")
	writeFile(t, repoA, "src/auth/SPEC.md", "# Repo A\n")
	if _, err := service.Sync(ctx, repoA, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync repo A: %v", err)
	}
	writeFile(t, repoB, "src/auth/SPEC.md", "# Repo B\n")
	if _, err := service.Sync(ctx, repoB, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync repo B: %v", err)
	}

	assertSidecarFile(t, remote, "skills/__branches__/main", "skills/skills/review.md", "# Skill\n")
	assertSidecarFile(t, remote, "repo/__branches__/main", "repo/src/auth/SPEC.md", "# Repo A\n")
	assertSidecarFile(t, remote, "repo-b/__branches__/main", "repo-b/src/auth/SPEC.md", "# Repo B\n")

	status, err := service.Status(ctx, repoA)
	if err != nil {
		t.Fatalf("status repo A: %v", err)
	}
	if len(status.Namespaces) != 2 || status.Namespaces[0].Name != "skills" ||
		status.Namespaces[0].Branch != "skills/__branches__/main" {
		t.Fatalf("unexpected namespaced status: %#v", status)
	}

	logOutput, err := service.Log(ctx, repoA, "src/auth/SPEC.md")
	if err != nil {
		t.Fatalf("log repo A: %v", err)
	}
	if !strings.Contains(logOutput, "bootstrap") {
		t.Fatalf("expected repo A sidecar history, got %q", logOutput)
	}

	if err := os.Remove(filepath.Join(repoA, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove repo A spec: %v", err)
	}
	if _, err := service.Sync(ctx, repoA, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("delete sync repo A: %v", err)
	}
	assertSidecarMissing(t, remote, "repo/__branches__/main", "repo/src/auth/SPEC.md")
	assertSidecarFile(t, remote, "repo-b/__branches__/main", "repo-b/src/auth/SPEC.md", "# Repo B\n")

	if err := os.RemoveAll(filepath.Join(repoB, sidecar.DirName)); err != nil {
		t.Fatalf("remove repo B sidecar clone: %v", err)
	}
	if err := os.Remove(filepath.Join(repoB, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove repo B spec: %v", err)
	}
	hydrated, err := service.Hydrate(ctx, repoB)
	if err != nil {
		t.Fatalf("hydrate repo B: %v", err)
	}
	if len(hydrated.Restored) != 1 || hydrated.Restored[0] != "src/auth/SPEC.md" {
		t.Fatalf("unexpected hydrated files: %#v", hydrated)
	}
	assertFile(t, filepath.Join(repoB, "src/auth/SPEC.md"), "# Repo B\n")
}

func TestServiceSyncRejectsOverlappingNamespaceOwnership(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "docs", Patterns: []string{"docs/**"}},
			{Name: "specs", Patterns: []string{"docs/**/*.md"}},
		},
	})
	writeFile(t, root, "docs/auth/SPEC.md", "# Auth\n")

	_, err := sidecar.New(&gitexec.ExecRunner{}).Sync(ctx, root, sidecar.SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "multiple skeeper namespaces") {
		t.Fatalf("expected namespace overlap error, got %v", err)
	}
}

func TestServiceSyncMovesFileWhenNamespaceExcludeChangesOwnership(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "repo", []string{"**/*.md"}))
	writeFile(t, root, "skills/review.md", "# Skill\n")
	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	assertSidecarFile(t, remote, "repo/__branches__/main", "repo/skills/review.md", "# Skill\n")

	cfg := config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "skills", Patterns: []string{"skills/*.md"}},
			{Name: "repo", Patterns: []string{"**/*.md"}, Exclude: []string{"skills/*.md"}},
		},
	}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save updated config: %v", err)
	}
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("ownership migration sync: %v", err)
	}
	assertSidecarMissing(t, remote, "repo/__branches__/main", "repo/skills/review.md")
	assertSidecarFile(t, remote, "skills/__branches__/main", "skills/skills/review.md", "# Skill\n")
}

func TestServiceSyncCleansSidecarWorktreeBetweenNamespacesAfterQueuedPush(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "repo", Patterns: []string{"docs/*.md"}},
			{Name: "skills", Patterns: []string{"skills/*.md"}},
		},
	})
	service := sidecar.New(&gitexec.ExecRunner{})
	writeFile(t, root, "skills/review.md", "# Skill\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	assertSidecarFile(t, remote, "skills/__branches__/main", "skills/skills/review.md", "# Skill\n")

	sidecarDir := filepath.Join(root, sidecar.DirName)
	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	git(t, sidecarDir, "remote", "set-url", "origin", missingRemote)
	writeFile(t, root, "docs/SPEC.md", "# Repo\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err == nil {
		t.Fatal("expected sync to fail while pushing repo namespace")
	}

	git(t, sidecarDir, "remote", "set-url", "origin", remote)
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("retry sync: %v", err)
	}
	assertSidecarFile(t, remote, "repo/__branches__/main", "repo/docs/SPEC.md", "# Repo\n")
	assertSidecarFile(t, remote, "skills/__branches__/main", "skills/skills/review.md", "# Skill\n")
	assertSidecarMissing(t, remote, "skills/__branches__/main", "repo/docs/SPEC.md")
}

func TestServiceHookSyncQueuesNamespaceSpecificFailure(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "repo", Patterns: []string{"docs/*.md"}},
			{Name: "skills", Patterns: []string{"skills/*.md"}},
		},
	})
	service := sidecar.New(&gitexec.ExecRunner{})
	writeFile(t, root, "skills/review.md", "# Skill\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	sidecarDir := filepath.Join(root, sidecar.DirName)
	git(t, sidecarDir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	writeFile(t, root, "docs/SPEC.md", "# Repo\n")
	result, err := service.Sync(ctx, root, sidecar.SyncOptions{Hook: true})
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
	queue := string(data)
	if !strings.Contains(queue, `"namespace": "repo"`) || !strings.Contains(queue, "push sidecar branch") {
		t.Fatalf("expected namespace-specific push failure in queue, got %s", queue)
	}
}

func TestServiceSyncPullRebasesNamespacedBranch(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	root := newMainRepo(t)
	cfg := config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "repo-a", Patterns: []string{"**/SPEC.md"}},
		},
	}
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	external := filepath.Join(t.TempDir(), "external")
	git(t, "", "clone", remote, external)
	git(t, external, "switch", "--track", "origin/repo-a/__branches__/main")
	writeFile(t, external, "repo-a/external/SPEC.md", "# External\n")
	git(t, external, "add", "repo-a/external/SPEC.md")
	git(t, external, "commit", "-m", "external sidecar update")
	git(t, external, "push", "origin", "repo-a/__branches__/main")

	result, err := service.Sync(ctx, root, sidecar.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("sync pull: %v", err)
	}
	if !result.Committed {
		t.Fatal("expected sync pull to commit deletion of external-only spec")
	}
	assertSidecarMissing(t, remote, "repo-a/__branches__/main", "repo-a/external/SPEC.md")
}

func TestServiceStatusReportsRemoteState(t *testing.T) {
	setGitIdentity(t)

	tests := []struct {
		name  string
		want  string
		setup func(*testing.T, statusFixture)
	}{
		{
			name: "not pushed",
			want: "not pushed",
		},
		{
			name: "in sync",
			want: "in sync",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				git(t, fixture.sidecarDir, "push", "-u", "origin", "project/__branches__/main")
			},
		},
		{
			name: "ahead",
			want: "ahead by 1 commit(s)",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				git(t, fixture.sidecarDir, "push", "-u", "origin", "project/__branches__/main")
				commitSidecarFile(t, fixture.sidecarDir, "local/SPEC.md", "# Local\n", "local sidecar update")
			},
		},
		{
			name: "behind",
			want: "behind by 1 commit(s)",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				git(t, fixture.sidecarDir, "push", "-u", "origin", "project/__branches__/main")
				base := gitOutput(t, fixture.sidecarDir, "rev-parse", "HEAD")
				remoteCommit := commitFromCurrentTree(t, fixture.sidecarDir, base, "remote sidecar update")
				git(t, fixture.sidecarDir, "push", "origin", remoteCommit+":refs/heads/project/__branches__/main")
			},
		},
		{
			name: "diverged",
			want: "diverged (ahead 1, behind 1)",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				git(t, fixture.sidecarDir, "push", "-u", "origin", "project/__branches__/main")
				base := gitOutput(t, fixture.sidecarDir, "rev-parse", "HEAD")
				commitSidecarFile(t, fixture.sidecarDir, "local/SPEC.md", "# Local\n", "local sidecar update")
				remoteCommit := commitFromCurrentTree(t, fixture.sidecarDir, base, "remote sidecar update")
				git(t, fixture.sidecarDir, "push", "origin", remoteCommit+":refs/heads/project/__branches__/main")
			},
		},
		{
			name: "unknown fetch failure",
			want: "unknown",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				missingRemote := filepath.Join(t.TempDir(), "missing.git")
				git(t, fixture.sidecarDir, "remote", "set-url", "origin", missingRemote)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newStatusFixture(t)
			if tt.setup != nil {
				tt.setup(t, fixture)
			}
			status, err := fixture.service.Status(context.Background(), fixture.root)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if len(status.Namespaces) != 1 {
				t.Fatalf("expected one namespace status, got %#v", status.Namespaces)
			}
			if status.Namespaces[0].Remote != tt.want {
				t.Fatalf("remote state mismatch: got %q want %q", status.Namespaces[0].Remote, tt.want)
			}
		})
	}
}

func TestServiceHookSyncQueuesFailureWithoutReturningError(t *testing.T) {
	root := newMainRepo(t)
	cfg := singleNamespaceConfig(filepath.Join(t.TempDir(), "missing.git"), "project", []string{"**/SPEC.md"})
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

func singleNamespaceConfig(sidecarURL, name string, patterns []string) config.Config {
	return config.Config{
		Sidecar: sidecarURL,
		Namespaces: []config.Namespace{
			{Name: name, Patterns: patterns},
		},
	}
}

func bootstrapRepo(t *testing.T, root string, cfg config.Config) {
	t.Helper()
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename)
	git(t, root, "commit", "-m", "bootstrap")
}

func assertSidecarFile(t *testing.T, remote, branch, path, want string) {
	t.Helper()
	got := gitOutput(t, "", "--git-dir", remote, "show", branch+":"+path)
	want = strings.TrimSuffix(want, "\n")
	if got != want {
		t.Fatalf("sidecar file %s:%s mismatch: got %q want %q", branch, path, got, want)
	}
}

func assertSidecarMissing(t *testing.T, remote, branch, path string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "--git-dir", remote, "show", branch+":"+path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected %s:%s to be absent, got %q", branch, path, string(out))
	}
}

func TestServiceHookSyncReportsQueueWriteFailureWithoutReturningError(t *testing.T) {
	root := newMainRepo(t)
	cfg := singleNamespaceConfig(filepath.Join(t.TempDir(), "missing.git"), "project", []string{"**/SPEC.md"})
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
		Namespaces: []config.Namespace{
			{Name: "project", Patterns: []string{"**/SPEC.md"}},
		},
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
		len(reloaded.Namespaces) != 1 ||
		!sameStrings(reloaded.Namespaces[0].Patterns, cfg.Namespaces[0].Patterns) {
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

func TestServiceInitUsesExistingSidecarURLAndDefaultNamespace(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)

	result, err := sidecar.New(&gitexec.ExecRunner{}).Init(ctx, root, sidecar.InitOptions{
		Sidecar:  remote,
		Patterns: []string{"**/SPEC.md"},
	})
	if err != nil {
		t.Fatalf("init with existing sidecar URL: %v", err)
	}
	wantNamespace := sidecar.DefaultNamespace(filepath.Base(root))
	if result.Config.Sidecar != remote || len(result.Config.Namespaces) != 1 ||
		result.Config.Namespaces[0].Name != wantNamespace {
		t.Fatalf("unexpected config: %#v", result.Config)
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName, ".git")); err != nil {
		t.Fatalf("expected sidecar clone: %v", err)
	}
	reloaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(reloaded.Namespaces) != 1 || reloaded.Namespaces[0].Name != wantNamespace {
		t.Fatalf("expected namespace %q, got %#v", wantNamespace, reloaded.Namespaces)
	}
}

func TestServiceInitRejectsInvalidPatternsBeforeSideEffects(t *testing.T) {
	root := newMainRepo(t)
	remote := filepath.Join(t.TempDir(), "shared-specs.git")

	_, err := sidecar.New(&gitexec.ExecRunner{}).Init(context.Background(), root, sidecar.InitOptions{
		Sidecar:  remote,
		Patterns: []string{"["},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid glob") {
		t.Fatalf("expected invalid glob error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, config.Filename)); !os.IsNotExist(statErr) {
		t.Fatalf("expected config not to be written, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, sidecar.DirName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected sidecar clone not to be created, stat err=%v", statErr)
	}
}

func TestServiceInitRejectsIncompatibleExistingConfig(t *testing.T) {
	root := newMainRepo(t)
	cfg := singleNamespaceConfig(filepath.Join(t.TempDir(), "sidecar.git"), "project", []string{"**/SPEC.md"})
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
	if got.Sidecar != cfg.Sidecar || len(got.Namespaces) != 1 ||
		got.Namespaces[0].Patterns[0] != cfg.Namespaces[0].Patterns[0] {
		t.Fatalf("config was modified: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName)); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar dir not to be created, stat err=%v", err)
	}
}

func TestServiceStatusAndLogRequireExistingSidecarClone(t *testing.T) {
	root := newMainRepo(t)
	cfg := singleNamespaceConfig(filepath.Join(t.TempDir(), "sidecar.git"), "project", []string{"**/SPEC.md"})
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
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
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

type statusFixture struct {
	root       string
	remote     string
	sidecarDir string
	service    *sidecar.Service
}

func newStatusFixture(t *testing.T) statusFixture {
	t.Helper()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename)
	git(t, root, "commit", "-m", "bootstrap")

	sidecarDir := filepath.Join(root, sidecar.DirName)
	git(t, "", "init", "-b", "main", sidecarDir)
	git(t, sidecarDir, "remote", "add", "origin", remote)
	git(t, sidecarDir, "switch", "-c", "project/__branches__/main")
	commitSidecarFile(t, sidecarDir, "project/src/auth/SPEC.md", "# Auth\n", "initial sidecar sync")
	return statusFixture{
		root:       root,
		remote:     remote,
		sidecarDir: sidecarDir,
		service:    sidecar.New(&gitexec.ExecRunner{}),
	}
}

func commitSidecarFile(t *testing.T, root, rel, content, message string) string {
	t.Helper()
	writeFile(t, root, rel, content)
	git(t, root, "add", rel)
	git(t, root, "commit", "-m", message)
	return gitOutput(t, root, "rev-parse", "HEAD")
}

func commitFromCurrentTree(t *testing.T, root, parent, message string) string {
	t.Helper()
	tree := gitOutput(t, root, "write-tree")
	return gitOutput(t, root, "commit-tree", tree, "-p", parent, "-m", message)
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
