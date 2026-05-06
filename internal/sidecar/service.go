// Package sidecar coordinates skeeper's mirrored Git repository.
package sidecar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/hooks"
	"github.com/compozy/skeeper/internal/matcher"
	"github.com/compozy/skeeper/internal/state"
)

const (
	// DirName is the sidecar clone directory in the main worktree.
	DirName       = ".skeeper"
	stateDirName  = "skeeper"
	remoteUnknown = "unknown"
)

// Service executes sidecar workflows.
type Service struct {
	runner gitexec.Runner
	git    *gitexec.Git
}

// New returns a sidecar service.
func New(runner gitexec.Runner) *Service {
	return &Service{
		runner: runner,
		git:    gitexec.NewGit(runner),
	}
}

// InitOptions configures project bootstrap.
type InitOptions struct {
	SidecarName string
	Visibility  string
	Bootstrap   string
	Patterns    []string
}

// InitResult reports files changed by init.
type InitResult struct {
	Root      string
	Sidecar   string
	Config    config.Config
	HookPath  string
	Gitignore string
}

// Init creates the remote sidecar, clones it, writes config, and installs hooks.
func (s *Service) Init(ctx context.Context, dir string, opts InitOptions) (InitResult, error) {
	root, err := s.git.Root(ctx, dir)
	if err != nil {
		return InitResult{}, err
	}
	sidecarDir := filepath.Join(root, DirName)
	name := strings.TrimSpace(opts.SidecarName)
	if name == "" {
		name = gitexec.RepoBaseName(root) + "-specs"
	}
	visibility := strings.TrimSpace(opts.Visibility)
	if visibility == "" {
		visibility = "private"
	}
	patterns := cleanPatterns(opts.Patterns)
	if len(patterns) == 0 {
		patterns = config.DefaultPatterns()
	}
	if exists(filepath.Join(root, config.Filename)) {
		return s.initExistingProject(ctx, root, opts)
	}
	if exists(sidecarDir) {
		return InitResult{}, fmt.Errorf("%s already exists", DirName)
	}

	if err := s.createRemote(ctx, root, name, visibility); err != nil {
		return InitResult{}, err
	}
	sidecarURL, err := s.remoteSSHURL(ctx, root, name)
	if err != nil {
		return InitResult{}, err
	}
	if _, err := s.runner.Run(ctx, root, "git", "clone", sidecarURL, DirName); err != nil {
		return InitResult{}, fmt.Errorf("clone sidecar into %s: %w", DirName, err)
	}

	cfg := config.Config{
		Sidecar:   sidecarURL,
		Bootstrap: strings.TrimSpace(opts.Bootstrap),
		Patterns:  patterns,
	}
	if err := config.Save(root, cfg); err != nil {
		return InitResult{}, err
	}
	if err := UpdateGitignore(root, cfg.Patterns); err != nil {
		return InitResult{}, err
	}
	gitDir, err := s.git.GitDir(ctx, root)
	if err != nil {
		return InitResult{}, err
	}
	if err := hooks.InstallPostCommit(gitDir); err != nil {
		return InitResult{}, err
	}

	return InitResult{
		Root:      root,
		Sidecar:   sidecarDir,
		Config:    cfg,
		HookPath:  filepath.Join(gitDir, "hooks", "post-commit"),
		Gitignore: filepath.Join(root, ".gitignore"),
	}, nil
}

func (s *Service) initExistingProject(ctx context.Context, root string, opts InitOptions) (InitResult, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return InitResult{}, err
	}
	if err := validateExistingConfig(cfg, opts); err != nil {
		return InitResult{}, err
	}
	if err := s.ensureClone(ctx, root, cfg.Sidecar); err != nil {
		return InitResult{}, err
	}
	if err := UpdateGitignore(root, cfg.Patterns); err != nil {
		return InitResult{}, err
	}
	gitDir, err := s.git.GitDir(ctx, root)
	if err != nil {
		return InitResult{}, err
	}
	if err := hooks.InstallPostCommit(gitDir); err != nil {
		return InitResult{}, err
	}
	return InitResult{
		Root:      root,
		Sidecar:   sidecarPath(root),
		Config:    cfg,
		HookPath:  filepath.Join(gitDir, "hooks", "post-commit"),
		Gitignore: filepath.Join(root, ".gitignore"),
	}, nil
}

// HydrateResult reports files restored by hydrate.
type HydrateResult struct {
	Restored []string
	Commit   string
}

// Hydrate clones the sidecar if needed and restores spec files to the main tree.
func (s *Service) Hydrate(ctx context.Context, dir string) (HydrateResult, error) {
	root, cfg, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return HydrateResult{}, err
	}
	if err := s.ensureClone(ctx, root, cfg.Sidecar); err != nil {
		return HydrateResult{}, err
	}
	branch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return HydrateResult{}, err
	}
	if err := s.tryUseBranch(ctx, sidecarPath(root), branch); err != nil {
		return HydrateResult{}, err
	}

	files, err := matcher.Find(sidecarPath(root), cfg.Patterns)
	if err != nil {
		return HydrateResult{}, err
	}
	for _, file := range files {
		if err := copyFile(
			filepath.Join(sidecarPath(root), filepath.FromSlash(file)),
			filepath.Join(root, filepath.FromSlash(file)),
		); err != nil {
			return HydrateResult{}, err
		}
	}

	gitDir, err := s.git.GitDir(ctx, root)
	if err != nil {
		return HydrateResult{}, err
	}
	if err := hooks.InstallPostCommit(gitDir); err != nil {
		return HydrateResult{}, err
	}
	commit := ""
	if result, err := s.runner.Run(ctx, sidecarPath(root), "git", "rev-parse", "--short", "HEAD"); err == nil {
		commit = gitexec.TrimmedStdout(result)
	}
	return HydrateResult{Restored: files, Commit: commit}, nil
}

// SyncOptions configures a sync run.
type SyncOptions struct {
	Pull bool
	Hook bool
}

// SyncResult reports a completed sync.
type SyncResult struct {
	ChangedFiles int
	Committed    bool
	Commit       string
	Queued       bool
	QueueFailed  bool
	QueueError   string
}

type syncContext struct {
	root       string
	cfg        config.Config
	store      *state.Store
	branch     string
	mainFiles  []string
	sidecarDir string
}

// Sync mirrors main-tree spec files into the sidecar and pushes the branch.
func (s *Service) Sync(ctx context.Context, dir string, opts SyncOptions) (SyncResult, error) {
	if opts.Hook {
		hookCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		defer cancel()
		result, err := s.sync(hookCtx, dir, opts)
		if err == nil {
			return result, nil
		}
		if queueErr := s.queueHookFailure(ctx, dir, err); queueErr != nil {
			return SyncResult{QueueFailed: true, QueueError: queueErr.Error()}, nil
		}
		return SyncResult{Queued: true}, nil
	}
	return s.sync(ctx, dir, opts)
}

func (s *Service) sync(ctx context.Context, dir string, opts SyncOptions) (SyncResult, error) {
	syncCtx, err := s.prepareSync(ctx, dir, opts)
	if err != nil {
		return SyncResult{}, err
	}
	sidecarFiles, err := matcher.Find(syncCtx.sidecarDir, syncCtx.cfg.Patterns)
	if err != nil {
		return SyncResult{}, err
	}
	if err := mirrorFiles(syncCtx.root, syncCtx.sidecarDir, syncCtx.mainFiles, sidecarFiles); err != nil {
		return SyncResult{}, err
	}
	if _, err := s.runner.Run(ctx, syncCtx.sidecarDir, "git", "add", "--all", "."); err != nil {
		return SyncResult{}, fmt.Errorf("stage sidecar changes: %w", err)
	}
	dirty, err := s.dirty(ctx, syncCtx.sidecarDir)
	if err != nil {
		return SyncResult{}, err
	}
	if !dirty {
		if err := s.flushQueuedPush(ctx, syncCtx); err != nil {
			return SyncResult{}, err
		}
		if err := syncCtx.store.ClearQueue(); err != nil {
			return SyncResult{}, err
		}
		return SyncResult{ChangedFiles: len(syncCtx.mainFiles)}, nil
	}

	commit, err := s.commitAndPush(ctx, syncCtx)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{ChangedFiles: len(syncCtx.mainFiles), Committed: true, Commit: commit}, nil
}

func (s *Service) prepareSync(ctx context.Context, dir string, opts SyncOptions) (syncContext, error) {
	root, cfg, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return syncContext{}, err
	}
	if err := s.ensureClone(ctx, root, cfg.Sidecar); err != nil {
		return syncContext{}, err
	}
	branch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return syncContext{}, err
	}
	sidecarDir := sidecarPath(root)
	if err := s.ensureBranch(ctx, sidecarDir, branch); err != nil {
		return syncContext{}, err
	}
	if opts.Pull {
		if err := s.pullBranch(ctx, sidecarDir, branch); err != nil {
			return syncContext{}, err
		}
	}
	mainFiles, err := matcher.Find(root, cfg.Patterns)
	if err != nil {
		return syncContext{}, err
	}
	return syncContext{
		root:       root,
		cfg:        cfg,
		store:      store,
		branch:     branch,
		mainFiles:  mainFiles,
		sidecarDir: sidecarDir,
	}, nil
}

func (s *Service) commitAndPush(ctx context.Context, syncCtx syncContext) (string, error) {
	mainSHA, err := s.git.HeadSHA(ctx, syncCtx.root)
	if err != nil {
		return "", err
	}
	mainSubject, err := s.git.HeadSubject(ctx, syncCtx.root)
	if err != nil {
		return "", err
	}
	shortSHA := mainSHA
	if len(shortSHA) > 12 {
		shortSHA = shortSHA[:12]
	}
	message := fmt.Sprintf("sync %s - %s", shortSHA, mainSubject)
	if _, err := s.runner.Run(
		ctx,
		syncCtx.sidecarDir,
		"git",
		"commit",
		"-m",
		message,
		"-m",
		"Main-Commit: "+mainSHA,
	); err != nil {
		return "", fmt.Errorf("commit sidecar changes: %w", err)
	}
	if _, err := s.runner.Run(ctx, syncCtx.sidecarDir, "git", "push", "-u", "origin", syncCtx.branch); err != nil {
		return "", fmt.Errorf("push sidecar branch %q: %w", syncCtx.branch, err)
	}
	commit := ""
	if result, err := s.runner.Run(ctx, syncCtx.sidecarDir, "git", "rev-parse", "--short", "HEAD"); err == nil {
		commit = gitexec.TrimmedStdout(result)
	}
	if err := syncCtx.store.ClearQueue(); err != nil {
		return "", err
	}
	if err := syncCtx.store.AppendLog(
		fmt.Sprintf("synced %d specs at %s", len(syncCtx.mainFiles), commit),
	); err != nil {
		return "", err
	}
	return commit, nil
}

func (s *Service) flushQueuedPush(ctx context.Context, syncCtx syncContext) error {
	queue, err := syncCtx.store.Queue()
	if err != nil {
		return err
	}
	if len(queue) == 0 {
		return nil
	}
	if hasHead := s.hasHead(ctx, syncCtx.sidecarDir); !hasHead {
		return nil
	}
	if _, err := s.runner.Run(ctx, syncCtx.sidecarDir, "git", "push", "-u", "origin", syncCtx.branch); err != nil {
		return fmt.Errorf("push queued sidecar branch %q: %w", syncCtx.branch, err)
	}
	return syncCtx.store.AppendLog(fmt.Sprintf("flushed queued sidecar push for %s", syncCtx.branch))
}

func (s *Service) hasHead(ctx context.Context, dir string) bool {
	_, err := s.runner.Run(ctx, dir, "git", "rev-parse", "--verify", "HEAD")
	return err == nil
}

// Status describes the current sidecar state.
type Status struct {
	Sidecar      string
	Branch       string
	LastCommit   string
	LastUnix     int64
	Remote       string
	TrackedFiles int
	PendingSync  int
}

// Status returns a summary suitable for CLI display.
func (s *Service) Status(ctx context.Context, dir string) (Status, error) {
	root, cfg, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return Status{}, err
	}
	sidecarDir := sidecarPath(root)
	if err := requireClone(root); err != nil {
		return Status{}, err
	}
	branch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return Status{}, err
	}
	files, err := matcher.Find(root, cfg.Patterns)
	if err != nil {
		return Status{}, err
	}
	queue, err := store.Queue()
	if err != nil {
		return Status{}, err
	}
	lastCommit := ""
	lastUnix := int64(0)
	if result, err := s.runner.Run(ctx, sidecarDir, "git", "log", "-1", "--format=%h %ct"); err == nil {
		parts := strings.Fields(gitexec.TrimmedStdout(result))
		if len(parts) == 2 {
			lastCommit = parts[0]
			if parsed, parseErr := strconv.ParseInt(parts[1], 10, 64); parseErr == nil {
				lastUnix = parsed
			}
		}
	}
	remote := s.remoteState(ctx, sidecarDir, branch)
	return Status{
		Sidecar:      cfg.Sidecar,
		Branch:       branch,
		LastCommit:   lastCommit,
		LastUnix:     lastUnix,
		Remote:       remote,
		TrackedFiles: len(files),
		PendingSync:  len(queue),
	}, nil
}

// Log returns sidecar history for a mirrored file path.
func (s *Service) Log(ctx context.Context, dir, path string) (string, error) {
	root, cfg, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return "", err
	}
	if err := requireClone(root); err != nil {
		return "", err
	}
	rel, err := resolveProjectPath(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve log path: %w", err)
	}
	if !pathMatchesPatterns(rel, cfg.Patterns) {
		return "", fmt.Errorf("%s does not match configured skeeper patterns", rel)
	}
	result, err := s.runner.Run(ctx, sidecarPath(root), "git", "log", "--pretty=format:%h\t%cr\t%s", "--", rel)
	if err != nil {
		return "", fmt.Errorf("read sidecar log for %s: %w", rel, err)
	}
	return result.Stdout, nil
}

func (s *Service) createRemote(ctx context.Context, root, name, visibility string) error {
	args := []string{"repo", "create", name}
	switch visibility {
	case "private":
		args = append(args, "--private")
	case "public":
		args = append(args, "--public")
	case "internal":
		args = append(args, "--internal")
	default:
		return fmt.Errorf("visibility must be private, public, or internal: %q", visibility)
	}
	if _, err := s.runner.Run(ctx, root, "gh", args...); err != nil {
		return fmt.Errorf("create sidecar repo with gh: %w", err)
	}
	return nil
}

func (s *Service) remoteSSHURL(ctx context.Context, root, name string) (string, error) {
	result, err := s.runner.Run(ctx, root, "gh", "repo", "view", name, "--json", "sshUrl", "--jq", ".sshUrl")
	if err != nil {
		if strings.Contains(name, "/") {
			return gitexec.SSHURLFromNameWithOwner(name), nil
		}
		return "", fmt.Errorf("read sidecar SSH URL with gh: %w", err)
	}
	url := gitexec.TrimmedStdout(result)
	if url == "" {
		return "", errors.New("gh repo view returned an empty sshUrl")
	}
	return url, nil
}

func (s *Service) loadProject(ctx context.Context, dir string) (string, config.Config, *state.Store, error) {
	root, err := s.git.Root(ctx, dir)
	if err != nil {
		return "", config.Config{}, nil, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return "", config.Config{}, nil, err
	}
	gitDir, err := s.git.GitDir(ctx, root)
	if err != nil {
		return "", config.Config{}, nil, err
	}
	return root, cfg, state.New(filepath.Join(gitDir, stateDirName)), nil
}

func (s *Service) remoteState(ctx context.Context, sidecarDir, branch string) string {
	dirty, err := s.dirty(ctx, sidecarDir)
	if err != nil {
		return remoteUnknown
	}
	if dirty {
		return "local changes"
	}
	if !s.hasHead(ctx, sidecarDir) {
		return "no local commits"
	}
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "fetch", "origin"); err != nil {
		return remoteUnknown
	}
	remoteRef := "refs/remotes/origin/" + branch
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "rev-parse", "--verify", remoteRef); err != nil {
		return "not pushed"
	}
	result, err := s.runner.Run(ctx, sidecarDir, "git", "rev-list", "--left-right", "--count", "HEAD..."+remoteRef)
	if err != nil {
		return remoteUnknown
	}
	parts := strings.Fields(gitexec.TrimmedStdout(result))
	if len(parts) != 2 {
		return remoteUnknown
	}
	ahead, aheadErr := strconv.Atoi(parts[0])
	behind, behindErr := strconv.Atoi(parts[1])
	if aheadErr != nil || behindErr != nil {
		return remoteUnknown
	}
	switch {
	case ahead > 0 && behind > 0:
		return fmt.Sprintf("diverged (ahead %d, behind %d)", ahead, behind)
	case ahead > 0:
		return fmt.Sprintf("ahead by %d commit(s)", ahead)
	case behind > 0:
		return fmt.Sprintf("behind by %d commit(s)", behind)
	default:
		return "in sync"
	}
}

func (s *Service) ensureClone(ctx context.Context, root, url string) error {
	sidecarDir := sidecarPath(root)
	if exists(filepath.Join(sidecarDir, ".git")) {
		return nil
	}
	if exists(sidecarDir) {
		return fmt.Errorf("%s exists but is not a git clone", DirName)
	}
	if _, err := s.runner.Run(ctx, root, "git", "clone", url, DirName); err != nil {
		return fmt.Errorf("clone sidecar into %s: %w", DirName, err)
	}
	return nil
}

func requireClone(root string) error {
	sidecarDir := sidecarPath(root)
	if exists(filepath.Join(sidecarDir, ".git")) {
		return nil
	}
	if exists(sidecarDir) {
		return fmt.Errorf("%s exists but is not a git clone", DirName)
	}
	return fmt.Errorf("%s clone missing; run `skeeper hydrate`", DirName)
}

func (s *Service) tryUseBranch(ctx context.Context, sidecarDir, branch string) error {
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "rev-parse", "--verify", "refs/heads/"+branch); err == nil {
		_, switchErr := s.runner.Run(ctx, sidecarDir, "git", "switch", branch)
		return switchErr
	}
	if _, err := s.runner.Run(
		ctx,
		sidecarDir,
		"git",
		"rev-parse",
		"--verify",
		"refs/remotes/origin/"+branch,
	); err == nil {
		_, switchErr := s.runner.Run(ctx, sidecarDir, "git", "switch", "--track", "origin/"+branch)
		return switchErr
	}
	return nil
}

func (s *Service) ensureBranch(ctx context.Context, sidecarDir, branch string) error {
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "rev-parse", "--verify", "refs/heads/"+branch); err == nil {
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "switch", branch); err != nil {
			return fmt.Errorf("switch sidecar branch %q: %w", branch, err)
		}
		return nil
	}
	if _, err := s.runner.Run(
		ctx,
		sidecarDir,
		"git",
		"rev-parse",
		"--verify",
		"refs/remotes/origin/"+branch,
	); err == nil {
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "switch", "--track", "origin/"+branch); err != nil {
			return fmt.Errorf("track sidecar branch %q: %w", branch, err)
		}
		return nil
	}
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "switch", "-c", branch); err != nil {
		return fmt.Errorf("create sidecar branch %q: %w", branch, err)
	}
	return nil
}

func (s *Service) pullBranch(ctx context.Context, sidecarDir, branch string) error {
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "fetch", "origin"); err != nil {
		return fmt.Errorf("fetch sidecar origin: %w", err)
	}
	if _, err := s.runner.Run(
		ctx,
		sidecarDir,
		"git",
		"rev-parse",
		"--verify",
		"refs/remotes/origin/"+branch,
	); err != nil {
		return nil
	}
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "rebase", "origin/"+branch); err != nil {
		return fmt.Errorf("rebase sidecar branch %q: %w", branch, err)
	}
	return nil
}

func (s *Service) dirty(ctx context.Context, dir string) (bool, error) {
	result, err := s.runner.Run(ctx, dir, "git", "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("read git status: %w", err)
	}
	return strings.TrimSpace(result.Stdout) != "", nil
}

func (s *Service) queueHookFailure(ctx context.Context, dir string, syncErr error) error {
	root, err := s.git.Root(ctx, dir)
	if err != nil {
		return nil
	}
	gitDir, err := s.git.GitDir(ctx, root)
	if err != nil {
		return nil
	}
	mainSHA, err := s.git.HeadSHA(ctx, root)
	if err != nil {
		mainSHA = "unknown"
	}
	store := state.New(filepath.Join(gitDir, stateDirName))
	reason := syncErr.Error()
	if gitexec.IsDeadline(syncErr) || strings.Contains(reason, "signal: killed") {
		reason = "sync timed out"
	}
	if err := store.Enqueue(state.Entry{Time: time.Now().UTC(), Reason: reason, MainSHA: mainSHA}); err != nil {
		return err
	}
	if err := store.AppendLog("queued sync: " + reason); err != nil {
		return err
	}
	return nil
}

func mirrorFiles(root, sidecarDir string, mainFiles, sidecarFiles []string) error {
	mainSet := make(map[string]struct{}, len(mainFiles))
	for _, file := range mainFiles {
		mainSet[file] = struct{}{}
		if err := copyFile(
			filepath.Join(root, filepath.FromSlash(file)),
			filepath.Join(sidecarDir, filepath.FromSlash(file)),
		); err != nil {
			return err
		}
	}
	for _, file := range sidecarFiles {
		if _, ok := mainSet[file]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(sidecarDir, filepath.FromSlash(file))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove sidecar file %s: %w", file, err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create parent for %s: %w", dst, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("open %s: %w", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

func cleanPatterns(patterns []string) []string {
	cleaned := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned
}

func validateExistingConfig(cfg config.Config, opts InitOptions) error {
	name := strings.TrimSpace(opts.SidecarName)
	if name != "" && !sidecarNameMatches(cfg.Sidecar, name) {
		return fmt.Errorf("%s already exists with incompatible sidecar %q", config.Filename, cfg.Sidecar)
	}
	bootstrap := strings.TrimSpace(opts.Bootstrap)
	if bootstrap != "" && bootstrap != cfg.Bootstrap {
		return fmt.Errorf("%s already exists with incompatible bootstrap", config.Filename)
	}
	patterns := cleanPatterns(opts.Patterns)
	if len(patterns) > 0 && !sameStrings(patterns, cfg.Patterns) {
		return fmt.Errorf("%s already exists with incompatible patterns", config.Filename)
	}
	return nil
}

func sidecarNameMatches(sidecarURL, name string) bool {
	if sidecarURL == name {
		return true
	}
	trimmedName := strings.TrimSuffix(filepath.Base(strings.TrimSpace(name)), ".git")
	trimmedSidecar := strings.TrimSuffix(filepath.Base(strings.TrimSpace(sidecarURL)), ".git")
	return trimmedName != "" && trimmedName == trimmedSidecar
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

func resolveProjectPath(root, path string) (string, error) {
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the project root", path)
	}
	return filepath.ToSlash(filepath.Clean(rel)), nil
}

func pathMatchesPatterns(path string, patterns []string) bool {
	for _, pattern := range patterns {
		ok, err := doublestar.PathMatch(filepath.ToSlash(pattern), path)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func sidecarPath(root string) string {
	return filepath.Join(root, DirName)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
