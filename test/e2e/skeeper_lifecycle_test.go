package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSkeeperLifecycleAcrossRealGitClones(t *testing.T) {
	env := newE2EEnv(t)
	project := env.newMainRepo("project")
	mainRemote := env.newBareRepo("project.git")
	env.git(project, "remote", "add", "origin", mainRemote)

	env.run(project, "skeeper",
		"init",
		"--sidecar-name", "project-specs",
		"--bootstrap", "brew install compozy/skeeper/skeeper",
		"--patterns", "**/SPEC.md",
	)
	env.assertContainsFile(filepath.Join(project, ".skeeper.yml"), "bootstrap: brew install")
	env.assertContainsFile(filepath.Join(project, ".gitignore"), ".skeeper/")
	env.assertContainsFile(filepath.Join(project, ".gitignore"), "**/SPEC.md")
	env.assertContainsFile(filepath.Join(project, ".git", "hooks", "post-commit"), "skeeper sync --hook")
	env.assertContainsFile(env.ghLog, "repo create project-specs --private")

	env.writeFile(project, "README.md", "# project\n")
	env.git(project, "add", "README.md", ".skeeper.yml", ".gitignore")
	env.git(project, "commit", "-m", "bootstrap skeeper")
	env.git(project, "push", "-u", "origin", "main")

	env.writeFile(project, "src/auth/service.go", "package auth\n")
	env.writeFile(project, "src/auth/SPEC.md", "# Auth spec\n\nOAuth provider design.\n")
	env.git(project, "add", "src/auth/service.go")
	env.git(project, "commit", "-m", "auth: add OAuth provider")
	env.git(project, "push")

	status := env.gitOutput(project, "status", "--short", "--ignored", "src/auth/SPEC.md")
	if !strings.Contains(status, "!! src/auth/SPEC.md") {
		t.Fatalf("expected main repo to ignore spec file, got %q", status)
	}

	syncOut := env.run(project, "skeeper", "sync")
	if !strings.Contains(syncOut, "no spec changes") && !strings.Contains(syncOut, "synced 1 specs") {
		t.Fatalf("unexpected sync output: %q", syncOut)
	}
	env.assertSidecarFile("main", "src/auth/SPEC.md", "# Auth spec\n\nOAuth provider design.\n")
	env.assertSidecarMissing("main", "src/auth/service.go")

	statusOut := env.run(project, "skeeper", "status")
	env.assertOutputContains(statusOut, "pending sync:  0")
	env.assertOutputContains(statusOut, "tracked files: 1")
	logOut := env.run(project, "skeeper", "log", "src/auth/SPEC.md")
	env.assertOutputContains(logOut, "auth: add OAuth provider")

	fresh := filepath.Join(env.root, "fresh")
	env.git("", "clone", mainRemote, fresh)
	env.run(fresh, "skeeper", "hydrate")
	env.assertFile(filepath.Join(fresh, "src/auth/SPEC.md"), "# Auth spec\n\nOAuth provider design.\n")
	env.assertContainsFile(filepath.Join(fresh, ".git", "hooks", "post-commit"), "skeeper sync --hook")
}

func TestSkeeperSyncFlushesQueuedHookPushFailure(t *testing.T) {
	env := newE2EEnv(t)
	project := env.newMainRepo("project")

	env.run(project, "skeeper",
		"init",
		"--sidecar-name", "project-specs",
		"--patterns", "**/SPEC.md",
	)
	env.writeFile(project, "README.md", "# project\n")
	env.git(project, "add", "README.md", ".skeeper.yml", ".gitignore")
	env.git(project, "commit", "-m", "bootstrap skeeper")

	offlineRemote := filepath.Join(env.root, "project-specs-offline.git")
	if err := os.Rename(env.sidecarRemote, offlineRemote); err != nil {
		t.Fatalf("move sidecar remote offline: %v", err)
	}
	env.writeFile(project, "src/auth/service.go", "package auth\n")
	env.writeFile(project, "src/auth/SPEC.md", "# Queued auth spec\n")
	env.git(project, "add", "src/auth/service.go")
	env.git(project, "commit", "-m", "auth: commit while sidecar remote is offline")
	env.assertContainsFile(filepath.Join(project, ".git", "skeeper", "queue.json"), "reason")

	if err := os.Rename(offlineRemote, env.sidecarRemote); err != nil {
		t.Fatalf("restore sidecar remote: %v", err)
	}
	statusOut := env.run(project, "skeeper", "status")
	env.assertOutputContains(statusOut, "pending sync:  1")
	env.assertOutputContains(statusOut, "remote:   not pushed")
	env.run(project, "skeeper", "sync")
	env.assertSidecarFile("main", "src/auth/SPEC.md", "# Queued auth spec\n")
	if _, err := os.Stat(filepath.Join(project, ".git", "skeeper", "queue.json")); !os.IsNotExist(err) {
		t.Fatalf("expected retry queue to be cleared, stat err=%v", err)
	}
}

type e2eEnv struct {
	t             *testing.T
	root          string
	binDir        string
	skeeper       string
	ghLog         string
	sidecarRemote string
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	env := &e2eEnv{
		t:             t,
		root:          root,
		binDir:        binDir,
		skeeper:       filepath.Join(binDir, "skeeper"),
		ghLog:         filepath.Join(root, "gh.log"),
		sidecarRemote: filepath.Join(root, "project-specs.git"),
	}
	env.buildSkeeper(t)
	env.writeFakeGH(t)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "global-gitconfig"))
	t.Setenv("GIT_AUTHOR_NAME", "skeeper e2e")
	t.Setenv("GIT_AUTHOR_EMAIL", "skeeper-e2e@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "skeeper e2e")
	t.Setenv("GIT_COMMITTER_EMAIL", "skeeper-e2e@example.com")
	t.Setenv("GH_REMOTE", env.sidecarRemote)
	t.Setenv("GH_LOG", env.ghLog)
	return env
}

func (e *e2eEnv) buildSkeeper(t *testing.T) {
	t.Helper()
	repoRoot := repositoryRoot(t)
	runCommand(t, repoRoot, "go", "build", "-trimpath", "-o", e.skeeper, "./cmd/skeeper")
}

func (e *e2eEnv) writeFakeGH(t *testing.T) {
	t.Helper()
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1" = "repo" ] && [ "$2" = "create" ]; then
  if [ ! -d "$GH_REMOTE" ]; then
    git init --bare --initial-branch=main "$GH_REMOTE" >/dev/null
  fi
  exit 0
fi
if [ "$1" = "repo" ] && [ "$2" = "view" ]; then
  printf '%s\n' "$GH_REMOTE"
  exit 0
fi
echo "unexpected gh invocation: $*" >&2
exit 2
`
	path := filepath.Join(e.binDir, "gh")
	writeFile(t, path, []byte(script), 0o600)
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("make fake gh executable: %v", err)
	}
}

func (e *e2eEnv) newMainRepo(name string) string {
	e.t.Helper()
	path := filepath.Join(e.root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		e.t.Fatalf("create repo dir: %v", err)
	}
	e.git(path, "init", "-b", "main")
	return path
}

func (e *e2eEnv) newBareRepo(name string) string {
	e.t.Helper()
	path := filepath.Join(e.root, name)
	e.git("", "init", "--bare", "--initial-branch=main", path)
	return path
}

func (e *e2eEnv) run(dir, name string, args ...string) string {
	e.t.Helper()
	return runCommand(e.t, dir, name, args...)
}

func (e *e2eEnv) git(dir string, args ...string) {
	e.t.Helper()
	_ = e.gitOutput(dir, args...)
}

func (e *e2eEnv) gitOutput(dir string, args ...string) string {
	e.t.Helper()
	return runCommand(e.t, dir, "git", args...)
}

func (e *e2eEnv) writeFile(root, rel, content string) {
	e.t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatalf("create parent for %s: %v", rel, err)
	}
	writeFile(e.t, path, []byte(content), 0o644)
}

func (e *e2eEnv) assertSidecarFile(branch, path, want string) {
	e.t.Helper()
	got := e.gitOutput("", "--git-dir", e.sidecarRemote, "show", branch+":"+path)
	if got != want {
		e.t.Fatalf("sidecar file %s mismatch: got %q want %q", path, got, want)
	}
}

func (e *e2eEnv) assertSidecarMissing(branch, path string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--git-dir", e.sidecarRemote, "show", branch+":"+path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		e.t.Fatalf("expected %s to be absent from sidecar, got output %q", path, string(out))
	}
}

func (e *e2eEnv) assertContainsFile(path, want string) {
	e.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("read %s: %v", path, err)
	}
	e.assertOutputContains(string(data), want)
}

func (e *e2eEnv) assertFile(path, want string) {
	e.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		e.t.Fatalf("file %s mismatch: got %q want %q", path, string(data), want)
	}
}

func (e *e2eEnv) assertOutputContains(output, want string) {
	e.t.Helper()
	if !strings.Contains(output, want) {
		e.t.Fatalf("expected output to contain %q, got %q", want, output)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func runCommand(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

func writeFile(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		t.Fatalf("write %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
