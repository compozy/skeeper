package gitexec

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const uncommitted = "uncommitted"

// Git wraps common git operations used by skeeper.
type Git struct {
	runner Runner
}

// NewGit returns a git command helper.
func NewGit(runner Runner) *Git {
	return &Git{runner: runner}
}

// Root returns the top-level worktree path for dir.
func (g *Git) Root(ctx context.Context, dir string) (string, error) {
	result, err := g.runner.Run(ctx, dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("find git worktree root: %w", err)
	}
	return TrimmedStdout(result), nil
}

// GitDir returns the absolute .git directory path for root.
func (g *Git) GitDir(ctx context.Context, root string) (string, error) {
	result, err := g.runner.Run(ctx, root, "git", "rev-parse", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("find git dir: %w", err)
	}
	dir := TrimmedStdout(result)
	if dir == "" {
		return "", errors.New("git dir is empty")
	}
	if filepath.IsAbs(dir) {
		return dir, nil
	}
	return filepath.Join(root, dir), nil
}

// CurrentBranch returns the checked-out branch name.
func (g *Git) CurrentBranch(ctx context.Context, root string) (string, error) {
	result, err := g.runner.Run(ctx, root, "git", "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("read current branch: %w", err)
	}
	branch := TrimmedStdout(result)
	if branch == "" {
		return "", errors.New("current git checkout is detached; skeeper requires a branch")
	}
	return branch, nil
}

// HeadSHA returns the current HEAD SHA.
func (g *Git) HeadSHA(ctx context.Context, root string) (string, error) {
	result, err := g.runner.Run(ctx, root, "git", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return uncommitted, nil
	}
	sha := TrimmedStdout(result)
	if sha == "" {
		return uncommitted, nil
	}
	return sha, nil
}

// HeadSubject returns the current HEAD subject.
func (g *Git) HeadSubject(ctx context.Context, root string) (string, error) {
	result, err := g.runner.Run(ctx, root, "git", "log", "-1", "--format=%s")
	if err != nil {
		return uncommitted, nil
	}
	subject := TrimmedStdout(result)
	if subject == "" {
		return uncommitted, nil
	}
	return subject, nil
}

// RepoBaseName returns the repository directory name.
func RepoBaseName(root string) string {
	return filepath.Base(filepath.Clean(root))
}

// SSHURLFromNameWithOwner returns a deterministic GitHub SSH URL.
func SSHURLFromNameWithOwner(nameWithOwner string) string {
	trimmed := strings.TrimSpace(nameWithOwner)
	if strings.HasPrefix(trimmed, "git@") || strings.HasPrefix(trimmed, "ssh://") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, ".git") {
		return "git@github.com:" + trimmed
	}
	return "git@github.com:" + trimmed + ".git"
}
