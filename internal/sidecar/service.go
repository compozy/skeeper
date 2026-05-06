// Package sidecar coordinates skeeper's mirrored Git repository.
package sidecar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
		git:    gitexec.NewGit(),
	}
}

// InitOptions configures project bootstrap.
type InitOptions struct {
	Sidecar      string
	SidecarName  string
	Visibility   string
	Directory    string
	DirectorySet bool
	NoDirectory  bool
	Bootstrap    string
	Patterns     []string
}

// InitDefaults reports the values init should present before user overrides.
type InitDefaults struct {
	SidecarName string
	Visibility  string
	Directory   string
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

// InitDefaults returns deterministic defaults for the project at dir.
func (s *Service) InitDefaults(ctx context.Context, dir string) (InitDefaults, error) {
	root, err := s.git.Root(ctx, dir)
	if err != nil {
		return InitDefaults{}, err
	}
	name := gitexec.RepoBaseName(root)
	return InitDefaults{
		SidecarName: name + "-specs",
		Visibility:  "private",
		Directory:   DefaultDirectory(name),
		Patterns:    config.DefaultPatterns(),
	}, nil
}

// Init creates the remote sidecar, clones it, writes config, and installs hooks.
func (s *Service) Init(ctx context.Context, dir string, opts InitOptions) (InitResult, error) {
	root, err := s.git.Root(ctx, dir)
	if err != nil {
		return InitResult{}, err
	}
	if err := validateInitOptions(opts); err != nil {
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

	sidecarURL, err := s.initSidecarURL(ctx, root, name, visibility, opts.Sidecar)
	if err != nil {
		return InitResult{}, err
	}
	if _, err := s.runner.Run(ctx, root, "git", "clone", sidecarURL, DirName); err != nil {
		return InitResult{}, fmt.Errorf("clone sidecar into %s: %w", DirName, err)
	}

	directory, err := initDirectory(root, opts)
	if err != nil {
		return InitResult{}, err
	}
	cfg := config.Config{
		Sidecar:   sidecarURL,
		Directory: directory,
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

func validateInitOptions(opts InitOptions) error {
	if strings.TrimSpace(opts.Sidecar) != "" && strings.TrimSpace(opts.SidecarName) != "" {
		return errors.New("sidecar and sidecar name are mutually exclusive")
	}
	if opts.NoDirectory && opts.DirectorySet {
		return errors.New("directory and no-directory are mutually exclusive")
	}
	if opts.DirectorySet && strings.TrimSpace(opts.Directory) == "" {
		return errors.New("directory cannot be empty; use no-directory to opt out")
	}
	return nil
}

func (s *Service) initSidecarURL(
	ctx context.Context,
	root string,
	name string,
	visibility string,
	sidecarURL string,
) (string, error) {
	trimmed := strings.TrimSpace(sidecarURL)
	if trimmed != "" {
		return trimmed, nil
	}
	if err := s.createRemote(ctx, root, name, visibility); err != nil {
		return "", err
	}
	return s.remoteSSHURL(ctx, root, name)
}

func initDirectory(root string, opts InitOptions) (string, error) {
	if opts.NoDirectory {
		return "", nil
	}
	directory := strings.TrimSpace(opts.Directory)
	if !opts.DirectorySet {
		directory = DefaultDirectory(gitexec.RepoBaseName(root))
	}
	return config.CleanDirectory(directory)
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
	sourceBranch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return HydrateResult{}, err
	}
	if err := s.tryUseBranch(ctx, sidecarPath(root), branchName(cfg, sourceBranch)); err != nil {
		return HydrateResult{}, err
	}

	storageDir := sidecarStoragePath(root, cfg)
	files, err := findMatchedFiles(ctx, storageDir, cfg.Patterns)
	if err != nil {
		return HydrateResult{}, err
	}
	for _, file := range files {
		if err := copyFile(
			filepath.Join(storageDir, filepath.FromSlash(file)),
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
	if short, err := s.git.HeadShortSHA(ctx, sidecarPath(root)); err == nil {
		commit = short
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
	root          string
	cfg           config.Config
	store         *state.Store
	sidecarBranch string
	mainFiles     []string
	sidecarDir    string
	storageDir    string
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
	sidecarFiles, err := findMatchedFiles(ctx, syncCtx.storageDir, syncCtx.cfg.Patterns)
	if err != nil {
		return SyncResult{}, err
	}
	if err := mirrorFiles(syncCtx.root, syncCtx.storageDir, syncCtx.mainFiles, sidecarFiles); err != nil {
		return SyncResult{}, err
	}
	if err := s.git.AddAll(ctx, syncCtx.sidecarDir); err != nil {
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
	sourceBranch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return syncContext{}, err
	}
	sidecarDir := sidecarPath(root)
	sidecarBranch := branchName(cfg, sourceBranch)
	if err := s.ensureBranch(ctx, sidecarDir, sidecarBranch); err != nil {
		return syncContext{}, err
	}
	if opts.Pull {
		if err := s.pullBranch(ctx, sidecarDir, sidecarBranch); err != nil {
			return syncContext{}, err
		}
	}
	mainFiles, err := matcher.FindContext(ctx, root, cfg.Patterns)
	if err != nil {
		return syncContext{}, err
	}
	return syncContext{
		root:          root,
		cfg:           cfg,
		store:         store,
		sidecarBranch: sidecarBranch,
		mainFiles:     mainFiles,
		sidecarDir:    sidecarDir,
		storageDir:    sidecarStoragePath(root, cfg),
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
	if _, err := s.runner.Run(
		ctx,
		syncCtx.sidecarDir,
		"git",
		"push",
		"-u",
		"origin",
		syncCtx.sidecarBranch,
	); err != nil {
		return "", fmt.Errorf("push sidecar branch %q: %w", syncCtx.sidecarBranch, err)
	}
	commit := ""
	if short, err := s.git.HeadShortSHA(ctx, syncCtx.sidecarDir); err == nil {
		commit = short
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
	if hasHead := s.git.HasHead(ctx, syncCtx.sidecarDir); !hasHead {
		return nil
	}
	if _, err := s.runner.Run(
		ctx,
		syncCtx.sidecarDir,
		"git",
		"push",
		"-u",
		"origin",
		syncCtx.sidecarBranch,
	); err != nil {
		return fmt.Errorf("push queued sidecar branch %q: %w", syncCtx.sidecarBranch, err)
	}
	return syncCtx.store.AppendLog(fmt.Sprintf("flushed queued sidecar push for %s", syncCtx.sidecarBranch))
}

func (s *Service) hasHead(ctx context.Context, dir string) bool {
	return s.git.HasHead(ctx, dir)
}

func (s *Service) hasLocalBranch(ctx context.Context, dir, branch string) bool {
	return s.git.RefExists(ctx, dir, "refs/heads/"+branch)
}

// Status describes the current sidecar state.
type Status struct {
	Sidecar       string
	Branch        string
	Directory     string
	SidecarBranch string
	LastCommit    string
	LastUnix      int64
	Remote        string
	TrackedFiles  int
	PendingSync   int
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
	sidecarBranch := branchName(cfg, branch)
	if err := s.tryUseBranch(ctx, sidecarDir, sidecarBranch); err != nil {
		return Status{}, err
	}
	branchAvailable := s.hasLocalBranch(ctx, sidecarDir, sidecarBranch)
	files, err := matcher.FindContext(ctx, root, cfg.Patterns)
	if err != nil {
		return Status{}, err
	}
	queue, err := store.Queue()
	if err != nil {
		return Status{}, err
	}
	lastCommit := ""
	lastUnix := int64(0)
	if branchAvailable {
		if info, err := s.git.LastCommit(ctx, sidecarDir); err == nil {
			lastCommit = info.ShortHash
			lastUnix = info.Unix
		}
	}
	remote := "not pushed"
	if !s.hasHead(ctx, sidecarDir) {
		remote = "no local commits"
	}
	if branchAvailable {
		remote = s.remoteState(ctx, sidecarDir, sidecarBranch)
	}
	return Status{
		Sidecar:       cfg.Sidecar,
		Branch:        branch,
		Directory:     cfg.Directory,
		SidecarBranch: sidecarBranch,
		LastCommit:    lastCommit,
		LastUnix:      lastUnix,
		Remote:        remote,
		TrackedFiles:  len(files),
		PendingSync:   len(queue),
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
	sourceBranch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return "", err
	}
	if err := s.tryUseBranch(ctx, sidecarPath(root), branchName(cfg, sourceBranch)); err != nil {
		return "", err
	}
	sidecarRel := sidecarFilePath(cfg, rel)
	result, err := s.runner.Run(ctx, sidecarPath(root), "git", "log", "--pretty=format:%h\t%cr\t%s", "--", sidecarRel)
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
	if !s.git.RefExists(ctx, sidecarDir, remoteRef) {
		return "not pushed"
	}
	ahead, behind, err := s.git.AheadBehind(ctx, sidecarDir, "HEAD", remoteRef)
	if err != nil {
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
	if s.git.RefExists(ctx, sidecarDir, "refs/heads/"+branch) {
		_, switchErr := s.runner.Run(ctx, sidecarDir, "git", "switch", branch)
		return switchErr
	}
	if s.git.RefExists(ctx, sidecarDir, "refs/remotes/origin/"+branch) {
		_, switchErr := s.runner.Run(ctx, sidecarDir, "git", "switch", "--track", "origin/"+branch)
		return switchErr
	}
	return nil
}

func (s *Service) ensureBranch(ctx context.Context, sidecarDir, branch string) error {
	if s.git.RefExists(ctx, sidecarDir, "refs/heads/"+branch) {
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "switch", branch); err != nil {
			return fmt.Errorf("switch sidecar branch %q: %w", branch, err)
		}
		return nil
	}
	if s.git.RefExists(ctx, sidecarDir, "refs/remotes/origin/"+branch) {
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
	if !s.git.RefExists(ctx, sidecarDir, "refs/remotes/origin/"+branch) {
		return nil
	}
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "rebase", "origin/"+branch); err != nil {
		return fmt.Errorf("rebase sidecar branch %q: %w", branch, err)
	}
	return nil
}

func (s *Service) dirty(ctx context.Context, dir string) (bool, error) {
	dirty, err := s.git.IsDirty(ctx, dir)
	if err != nil {
		return false, fmt.Errorf("read git status: %w", err)
	}
	return dirty, nil
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
	sidecarURL := strings.TrimSpace(opts.Sidecar)
	if sidecarURL != "" && cfg.Sidecar != sidecarURL {
		return fmt.Errorf("%s already exists with incompatible sidecar %q", config.Filename, cfg.Sidecar)
	}
	name := strings.TrimSpace(opts.SidecarName)
	if name != "" && !sidecarNameMatches(cfg.Sidecar, name) {
		return fmt.Errorf("%s already exists with incompatible sidecar %q", config.Filename, cfg.Sidecar)
	}
	if opts.NoDirectory && cfg.Directory != "" {
		return fmt.Errorf("%s already exists with incompatible directory", config.Filename)
	}
	if opts.DirectorySet {
		directory, err := config.CleanDirectory(opts.Directory)
		if err != nil {
			return err
		}
		if directory != cfg.Directory {
			return fmt.Errorf("%s already exists with incompatible directory", config.Filename)
		}
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

func sidecarStoragePath(root string, cfg config.Config) string {
	if cfg.Directory == "" {
		return sidecarPath(root)
	}
	return filepath.Join(sidecarPath(root), filepath.FromSlash(cfg.Directory))
}

func sidecarFilePath(cfg config.Config, rel string) string {
	if cfg.Directory == "" {
		return rel
	}
	return filepath.ToSlash(filepath.Join(filepath.FromSlash(cfg.Directory), filepath.FromSlash(rel)))
}

func findMatchedFiles(ctx context.Context, root string, patterns []string) ([]string, error) {
	if !exists(root) {
		return nil, nil
	}
	return matcher.FindContext(ctx, root, patterns)
}

func branchName(cfg config.Config, sourceBranch string) string {
	if cfg.Directory == "" {
		return sourceBranch
	}
	return cfg.Directory + "/" + config.DirectoryBranchSegment + "/" + sourceBranch
}

// DefaultDirectory returns a safe sidecar namespace derived from a repo name.
func DefaultDirectory(repoName string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.TrimSpace(repoName) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '.' || r == '_':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	directory := strings.Trim(b.String(), "-.")
	if directory == "" {
		return "project"
	}
	if _, err := config.CleanDirectory(directory); err != nil {
		return "project"
	}
	return directory
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
