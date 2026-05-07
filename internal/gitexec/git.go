package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const uncommitted = "uncommitted"

// Git wraps common git operations used by skeeper.
type Git struct{}

// CommitInfo describes the current HEAD commit.
type CommitInfo struct {
	ShortHash string
	Unix      int64
}

// NewGit returns a git helper.
func NewGit() *Git {
	return &Git{}
}

// Root returns the top-level worktree path for dir.
func (g *Git) Root(ctx context.Context, dir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repo, err := openRepository(dir)
	if err != nil {
		return "", fmt.Errorf("find git worktree root: %w", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("find git worktree root: %w", err)
	}
	root := worktree.Filesystem.Root()
	if root == "" {
		return "", errors.New("git worktree root is empty")
	}
	return filepath.Clean(root), nil
}

// GitDir returns the absolute .git directory path for root.
func (g *Git) GitDir(ctx context.Context, root string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repo, err := openRepository(root)
	if err != nil {
		return "", fmt.Errorf("find git dir: %w", err)
	}
	storer, ok := repo.Storer.(interface {
		Filesystem() billy.Filesystem
	})
	if !ok {
		return "", errors.New("git repository storage does not expose filesystem")
	}
	dir := storer.Filesystem().Root()
	if dir == "" {
		return "", errors.New("git dir is empty")
	}
	return filepath.Clean(dir), nil
}

// CurrentBranch returns the checked-out branch name.
func (g *Git) CurrentBranch(ctx context.Context, root string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repo, err := openRepository(root)
	if err != nil {
		return "", fmt.Errorf("read current branch: %w", err)
	}
	ref, err := repo.Head()
	if err != nil {
		cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--quiet", "--short", "HEAD")
		cmd.Dir = root
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if shellErr := cmd.Run(); shellErr != nil {
			return "", fmt.Errorf("read current branch: %w", err)
		}
		branch := strings.TrimSpace(stdout.String())
		if branch == "" {
			return "", fmt.Errorf("read current branch: %w", err)
		}
		return branch, nil
	}
	if !ref.Name().IsBranch() {
		return "", errors.New("current git checkout is detached; skeeper requires a branch")
	}
	return ref.Name().Short(), nil
}

// HeadSHA returns the current HEAD SHA.
func (g *Git) HeadSHA(ctx context.Context, root string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repo, err := openRepository(root)
	if err != nil {
		return uncommitted, nil
	}
	ref, err := repo.Head()
	if err != nil || ref.Hash().IsZero() {
		return uncommitted, nil
	}
	return ref.Hash().String(), nil
}

// HeadSubject returns the current HEAD subject.
func (g *Git) HeadSubject(ctx context.Context, root string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repo, err := openRepository(root)
	if err != nil {
		return uncommitted, nil
	}
	commit, err := headCommit(repo)
	if err != nil {
		return uncommitted, nil
	}
	subject, _, _ := strings.Cut(commit.Message, "\n")
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return uncommitted, nil
	}
	return subject, nil
}

// HeadShortSHA returns the short current HEAD SHA.
func (g *Git) HeadShortSHA(ctx context.Context, root string) (string, error) {
	sha, err := g.HeadSHA(ctx, root)
	if err != nil || sha == uncommitted {
		return "", err
	}
	return shortHashString(sha), nil
}

// HasHead reports whether dir has a valid HEAD commit.
func (g *Git) HasHead(ctx context.Context, dir string) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	repo, err := openRepository(dir)
	if err != nil {
		return false
	}
	_, err = repo.Head()
	return err == nil
}

// RefExists reports whether ref exists in dir.
func (g *Git) RefExists(ctx context.Context, dir, ref string) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	repo, err := openRepository(dir)
	if err != nil {
		return false
	}
	_, err = repo.Reference(plumbing.ReferenceName(ref), true)
	return err == nil
}

// LastCommit returns the short hash and commit time for HEAD.
func (g *Git) LastCommit(ctx context.Context, dir string) (CommitInfo, error) {
	if err := ctx.Err(); err != nil {
		return CommitInfo{}, err
	}
	repo, err := openRepository(dir)
	if err != nil {
		return CommitInfo{}, err
	}
	commit, err := headCommit(repo)
	if err != nil {
		return CommitInfo{}, err
	}
	return CommitInfo{
		ShortHash: shortHash(commit.Hash),
		Unix:      commit.Committer.When.UTC().Unix(),
	}, nil
}

// IsDirty reports whether the worktree or index has uncommitted changes.
func (g *Git) IsDirty(ctx context.Context, dir string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	repo, err := openRepository(dir)
	if err != nil {
		return false, err
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return false, err
	}
	status, err := worktree.Status()
	if err != nil {
		return false, err
	}
	return !status.IsClean(), nil
}

// AheadBehind counts commits reachable from left but not right, and right but not left.
func (g *Git) AheadBehind(ctx context.Context, dir, left, right string) (int, int, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	repo, err := openRepository(dir)
	if err != nil {
		return 0, 0, err
	}
	leftHash, err := resolveHash(repo, left)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve %s: %w", left, err)
	}
	rightHash, err := resolveHash(repo, right)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve %s: %w", right, err)
	}
	leftReachable, err := reachableCommits(repo, leftHash)
	if err != nil {
		return 0, 0, err
	}
	rightReachable, err := reachableCommits(repo, rightHash)
	if err != nil {
		return 0, 0, err
	}
	ahead := 0
	for hash := range leftReachable {
		if _, ok := rightReachable[hash]; !ok {
			ahead++
		}
	}
	behind := 0
	for hash := range rightReachable {
		if _, ok := leftReachable[hash]; !ok {
			behind++
		}
	}
	return ahead, behind, nil
}

func openRepository(dir string) (*gogit.Repository, error) {
	return gogit.PlainOpenWithOptions(dir, &gogit.PlainOpenOptions{
		DetectDotGit:          true,
		EnableDotGitCommonDir: true,
	})
}

func headCommit(repo *gogit.Repository) (*object.Commit, error) {
	ref, err := repo.Head()
	if err != nil {
		return nil, err
	}
	return repo.CommitObject(ref.Hash())
}

func resolveHash(repo *gogit.Repository, revision string) (plumbing.Hash, error) {
	if revision == "HEAD" {
		ref, err := repo.Head()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		return ref.Hash(), nil
	}
	if strings.HasPrefix(revision, "refs/") {
		ref, err := repo.Reference(plumbing.ReferenceName(revision), true)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		return ref.Hash(), nil
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(revision))
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return *hash, nil
}

func reachableCommits(repo *gogit.Repository, start plumbing.Hash) (map[plumbing.Hash]struct{}, error) {
	seen := make(map[plumbing.Hash]struct{})
	stack := []plumbing.Hash{start}
	for len(stack) > 0 {
		hash := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[hash]; ok {
			continue
		}
		commit, err := repo.CommitObject(hash)
		if err != nil {
			return nil, fmt.Errorf("read commit %s: %w", shortHash(hash), err)
		}
		seen[hash] = struct{}{}
		stack = append(stack, commit.ParentHashes...)
	}
	return seen, nil
}

func shortHash(hash plumbing.Hash) string {
	return shortHashString(hash.String())
}

func shortHashString(hash string) string {
	if len(hash) <= 7 {
		return hash
	}
	return hash[:7]
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
