// Package hooks installs and checks skeeper-managed Git hooks.
package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/managedblock"
	"github.com/compozy/skeeper/internal/reconcile"
)

const (
	preCommitBegin  = "# >>> skeeper pre-commit hook >>>"
	preCommitEnd    = "# <<< skeeper pre-commit hook <<<"
	preMergeBegin   = "# >>> skeeper pre-merge-commit hook >>>"
	preMergeEnd     = "# <<< skeeper pre-merge-commit hook <<<"
	prePushBegin    = "# >>> skeeper pre-push hook >>>"
	prePushEnd      = "# <<< skeeper pre-push hook <<<"
	postCommitBegin = "# >>> skeeper post-commit hook >>>"
	postCommitEnd   = "# <<< skeeper post-commit hook <<<"
	attributesBegin = "# >>> skeeper lockfile merge driver >>>"
	attributesEnd   = "# <<< skeeper lockfile merge driver <<<"
)

// InstallOptions configures managed hook installation.
type InstallOptions struct {
	Config config.Config
}

// InstallResult reports paths changed by hook installation.
type InstallResult struct {
	PreCommit      string   `json:"pre_commit"`
	PreMergeCommit string   `json:"pre_merge_commit"`
	PrePush        string   `json:"pre_push"`
	Gitattributes  string   `json:"gitattributes"`
	RemovedLegacy  []string `json:"removed_legacy,omitempty"`
}

// CheckResult reports managed hook health.
type CheckResult struct {
	OK          bool     `json:"ok"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

// Manager installs and checks managed hooks.
type Manager interface {
	Install(ctx context.Context, root reconcile.RepoRoot, opts InstallOptions) (InstallResult, error)
	Check(ctx context.Context, root reconcile.RepoRoot) (CheckResult, error)
}

// GitManager is the production hook manager.
type GitManager struct {
	runner gitexec.Runner
	git    *gitexec.Git
}

var _ Manager = (*GitManager)(nil)

// NewManager returns a Git-backed hook manager.
func NewManager(runner gitexec.Runner) *GitManager {
	return &GitManager{runner: runner, git: gitexec.NewGit()}
}

// Install writes current managed hooks and removes legacy post-commit blocks.
func (m *GitManager) Install(ctx context.Context, root reconcile.RepoRoot, opts InstallOptions) (InstallResult, error) {
	gitDir, err := m.git.GitDir(ctx, string(root))
	if err != nil {
		return InstallResult{}, err
	}
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return InstallResult{}, fmt.Errorf("create hooks dir: %w", err)
	}
	allowEnv := opts.Config.Settings.Hooks.AllowSkipEnv
	if allowEnv == "" {
		allowEnv = config.DefaultAllowSkipEnv
	}
	preCommit := filepath.Join(hooksDir, "pre-commit")
	preMergeCommit := filepath.Join(hooksDir, "pre-merge-commit")
	prePush := filepath.Join(hooksDir, "pre-push")
	postCommit := filepath.Join(hooksDir, "post-commit")
	if err := installHookBlock(preCommit, preCommitBegin, preCommitEnd, preCommitBody(allowEnv)); err != nil {
		return InstallResult{}, err
	}
	if err := installHookBlock(
		preMergeCommit,
		preMergeBegin,
		preMergeEnd,
		preMergeCommitBody(allowEnv),
	); err != nil {
		return InstallResult{}, err
	}
	if err := installHookBlock(prePush, prePushBegin, prePushEnd, prePushBody()); err != nil {
		return InstallResult{}, err
	}
	removedLegacy, err := removeLegacyPostCommit(postCommit)
	if err != nil {
		return InstallResult{}, err
	}
	attributesPath := filepath.Join(string(root), ".gitattributes")
	if err := installAttributes(attributesPath); err != nil {
		return InstallResult{}, err
	}
	if _, err := m.runner.Run(
		ctx,
		string(root),
		"git",
		"config",
		"merge.skeeper-lock.name",
		"skeeper lockfile merge driver",
	); err != nil {
		return InstallResult{}, fmt.Errorf("configure skeeper merge driver name: %w", err)
	}
	if _, err := m.runner.Run(
		ctx,
		string(root),
		"git",
		"config",
		"merge.skeeper-lock.driver",
		"skeeper internal merge-driver %O %A %B",
	); err != nil {
		return InstallResult{}, fmt.Errorf("configure skeeper merge driver: %w", err)
	}
	return InstallResult{
		PreCommit:      preCommit,
		PreMergeCommit: preMergeCommit,
		PrePush:        prePush,
		Gitattributes:  attributesPath,
		RemovedLegacy:  removedLegacy,
	}, nil
}

// Check validates managed hook presence and ordering.
func (m *GitManager) Check(ctx context.Context, root reconcile.RepoRoot) (CheckResult, error) {
	gitDir, err := m.git.GitDir(ctx, string(root))
	if err != nil {
		return CheckResult{}, err
	}
	diagnostics := make([]string, 0)
	preCommit := filepath.Join(gitDir, "hooks", "pre-commit")
	if err := checkHookLast(preCommit, preCommitBegin, preCommitEnd); err != nil {
		diagnostics = append(diagnostics, err.Error())
	}
	preMergeCommit := filepath.Join(gitDir, "hooks", "pre-merge-commit")
	if err := checkHookContains(preMergeCommit, preMergeBegin, preMergeEnd); err != nil {
		diagnostics = append(diagnostics, err.Error())
	}
	prePush := filepath.Join(gitDir, "hooks", "pre-push")
	if err := checkHookContains(prePush, prePushBegin, prePushEnd); err != nil {
		diagnostics = append(diagnostics, err.Error())
	}
	attrs, err := os.ReadFile(filepath.Join(string(root), ".gitattributes"))
	if err != nil || !managedBlockContains(
		string(attrs),
		attributesBegin,
		attributesEnd,
		"skeeper.lock merge=skeeper-lock",
	) {
		diagnostics = append(diagnostics, ".gitattributes is missing skeeper.lock merge driver")
	}
	if err := m.checkMergeDriverConfig(ctx, string(root)); err != nil {
		diagnostics = append(diagnostics, err.Error())
	}
	return CheckResult{OK: len(diagnostics) == 0, Diagnostics: diagnostics}, nil
}

func (m *GitManager) checkMergeDriverConfig(ctx context.Context, root string) error {
	name, err := m.runner.Run(ctx, root, "git", "config", "--get", "merge.skeeper-lock.name")
	if err != nil || strings.TrimSpace(name.Stdout) != "skeeper lockfile merge driver" {
		return fmt.Errorf("merge_driver_unconfigured: run `skeeper repair`")
	}
	driver, err := m.runner.Run(ctx, root, "git", "config", "--get", "merge.skeeper-lock.driver")
	if err != nil || strings.TrimSpace(driver.Stdout) != "skeeper internal merge-driver %O %A %B" {
		return fmt.Errorf("merge_driver_unconfigured: run `skeeper repair`")
	}
	return nil
}

func preCommitBody(allowEnv string) string {
	return fmt.Sprintf(`set -eu
if [ "${%s:-}" = "1" ]; then
  if ! skeeper internal record-bypass --reason "pre-commit bypass via %s=1"; then
    echo "skeeper: bypass requested but audit record failed" >&2
    exit 1
  fi
  echo "skeeper: strict pre-commit bypass recorded; run 'skeeper sync' before pushing" >&2
  exit 0
fi
if command -v skeeper >/dev/null 2>&1; then
  skeeper internal pre-commit
else
  echo "skeeper: command not found; install skeeper or run '%s=1 git commit' and repair with 'skeeper sync'" >&2
  exit 1
fi
`, allowEnv, allowEnv, allowEnv)
}

func preMergeCommitBody(allowEnv string) string {
	return fmt.Sprintf(`set -eu
if [ "${%s:-}" = "1" ]; then
  if ! skeeper internal record-bypass --reason "pre-merge-commit bypass via %s=1"; then
    echo "skeeper: bypass requested but audit record failed" >&2
    exit 1
  fi
  echo "skeeper: strict pre-merge-commit bypass recorded; run 'skeeper sync' before pushing" >&2
  exit 0
fi
if command -v skeeper >/dev/null 2>&1; then
  skeeper internal pre-commit
else
  echo "skeeper: command not found; install skeeper or run '%s=1 git merge' and repair with 'skeeper sync'" >&2
  exit 1
fi
`, allowEnv, allowEnv, allowEnv)
}

func prePushBody() string {
	return `set -eu
if command -v skeeper >/dev/null 2>&1; then
  skeeper internal pre-push
else
  echo "skeeper: command not found; install skeeper before pushing" >&2
  exit 1
fi
`
}

func installHookBlock(path, begin, end, body string) error {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read hook %s: %w", filepath.Base(path), err)
	}
	next := managedblock.Replace(string(content), begin, end)
	block := begin + "\n" + body + end + "\n"
	if strings.TrimSpace(next) == "" {
		next = "#!/bin/sh\n\n" + block
	} else {
		if !strings.HasPrefix(next, "#!") {
			next = "#!/bin/sh\n\n" + next
		}
		next = strings.TrimRight(next, "\n") + "\n\n" + block
	}
	if err := managedblock.WriteFile(path, []byte(next), 0o600); err != nil {
		return fmt.Errorf("write hook %s: %w", filepath.Base(path), err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("mark hook %s executable: %w", filepath.Base(path), err)
	}
	return nil
}

func removeLegacyPostCommit(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read post-commit hook: %w", err)
	}
	next := managedblock.Replace(string(content), postCommitBegin, postCommitEnd)
	if next == string(content) {
		return nil, nil
	}
	if strings.TrimSpace(next) == "" || strings.TrimSpace(next) == "#!/bin/sh" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove empty legacy post-commit hook: %w", err)
		}
		return []string{path}, nil
	}
	if !strings.HasPrefix(next, "#!") {
		next = "#!/bin/sh\n\n" + next
	}
	if err := managedblock.WriteFile(path, []byte(strings.TrimRight(next, "\n")+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write post-commit hook: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, fmt.Errorf("mark post-commit hook executable: %w", err)
	}
	return []string{path}, nil
}

func installAttributes(path string) error {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitattributes: %w", err)
	}
	block := attributesBegin + "\nskeeper.lock merge=skeeper-lock\n" + attributesEnd + "\n"
	next := managedblock.Replace(string(content), attributesBegin, attributesEnd)
	if strings.TrimSpace(next) == "" {
		next = block
	} else {
		next = strings.TrimRight(next, "\n") + "\n\n" + block
	}
	if err := managedblock.WriteFile(path, []byte(next), 0o644); err != nil {
		return fmt.Errorf("write .gitattributes: %w", err)
	}
	return nil
}

func checkHookLast(path, begin, end string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s is missing; run `skeeper repair`", filepath.Base(path))
	}
	content := string(data)
	start := strings.Index(content, begin)
	finish := strings.Index(content, end)
	if start == -1 || finish == -1 || finish < start {
		return fmt.Errorf("%s missing skeeper managed block", filepath.Base(path))
	}
	after := strings.TrimSpace(content[finish+len(end):])
	if after != "" {
		return fmt.Errorf("%s has content after the skeeper block; run `skeeper repair`", filepath.Base(path))
	}
	return nil
}

func checkHookContains(path, begin, end string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s is missing; run `skeeper repair`", filepath.Base(path))
	}
	content := string(data)
	if !strings.Contains(content, begin) || !strings.Contains(content, end) {
		return fmt.Errorf("%s missing skeeper managed block", filepath.Base(path))
	}
	return nil
}

func managedBlockContains(content, begin, end, want string) bool {
	_, after, ok := strings.Cut(content, begin)
	if !ok {
		return false
	}
	remainder := after
	before, _, ok := strings.Cut(remainder, end)
	if !ok {
		return false
	}
	return strings.Contains(before, want)
}
