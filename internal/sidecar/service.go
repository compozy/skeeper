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

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/hooks"
	"github.com/compozy/skeeper/internal/matcher"
	"github.com/compozy/skeeper/internal/state"
)

const (
	// DirName is the sidecar clone directory in the main worktree.
	DirName              = ".skeeper"
	stateDirName         = "skeeper"
	remoteUnknown        = "unknown"
	hookNamespaceTimeout = 750 * time.Millisecond
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
	Namespace    string
	NamespaceSet bool
	Bootstrap    string
	Patterns     []string
}

// InitDefaults reports the values init should present before user overrides.
type InitDefaults struct {
	SidecarName string
	Visibility  string
	Namespace   string
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
		Namespace:   DefaultNamespace(name),
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
	patterns, err := config.NormalizePatterns(opts.Patterns)
	if err != nil {
		return InitResult{}, err
	}
	if len(patterns) == 0 {
		patterns = config.DefaultPatterns()
	}
	namespace, err := initNamespace(root, opts)
	if err != nil {
		return InitResult{}, err
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

	cfg := config.Config{
		Sidecar:   sidecarURL,
		Bootstrap: strings.TrimSpace(opts.Bootstrap),
		Namespaces: []config.Namespace{
			{Name: namespace, Patterns: patterns},
		},
	}
	if err := config.Save(root, cfg); err != nil {
		return InitResult{}, err
	}
	if err := UpdateGitignore(root, cfg.Namespaces); err != nil {
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
	if opts.NamespaceSet && strings.TrimSpace(opts.Namespace) == "" {
		return errors.New("namespace cannot be empty")
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

func initNamespace(root string, opts InitOptions) (string, error) {
	namespace := strings.TrimSpace(opts.Namespace)
	if !opts.NamespaceSet {
		namespace = DefaultNamespace(gitexec.RepoBaseName(root))
	}
	return config.CleanNamespace(namespace)
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
	if err := UpdateGitignore(root, cfg.Namespaces); err != nil {
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
	restored := make([]string, 0)
	restoredBy := make(map[string]string)
	for _, namespace := range cfg.Namespaces {
		branch := branchName(namespace.Name, sourceBranch)
		ok, err := s.useExistingBranch(ctx, sidecarPath(root), branch)
		if err != nil {
			return HydrateResult{}, err
		}
		if !ok {
			continue
		}
		storageDir := sidecarStoragePath(root, namespace.Name)
		files, err := findMatchedFiles(ctx, storageDir, namespace.Patterns)
		if err != nil {
			return HydrateResult{}, err
		}
		for _, file := range files {
			if !namespace.Owns(file) {
				continue
			}
			if previous, ok := restoredBy[file]; ok {
				return HydrateResult{}, fmt.Errorf(
					"%s would be restored from multiple skeeper namespaces: %s, %s",
					file,
					previous,
					namespace.Name,
				)
			}
			restoredBy[file] = namespace.Name
			if err := copyFile(
				filepath.Join(storageDir, filepath.FromSlash(file)),
				filepath.Join(root, filepath.FromSlash(file)),
			); err != nil {
				return HydrateResult{}, err
			}
			restored = append(restored, file)
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
	return HydrateResult{Restored: restored, Commit: commit}, nil
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
	Namespaces   []NamespaceSyncResult
	Queued       bool
	QueueFailed  bool
	QueueError   string
}

// NamespaceSyncResult reports one namespace sync outcome.
type NamespaceSyncResult struct {
	Name         string
	Branch       string
	ChangedFiles int
	Committed    bool
	Commit       string
}

// Sync mirrors main-tree spec files into the sidecar and pushes the branch.
func (s *Service) Sync(ctx context.Context, dir string, opts SyncOptions) (SyncResult, error) {
	if opts.Hook {
		result, err := s.sync(ctx, dir, opts)
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
	project, err := s.prepareProject(ctx, dir)
	if err != nil {
		return SyncResult{}, err
	}
	routes, err := resolveMainRoutes(ctx, project.root, project.cfg)
	if err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{}
	for _, route := range routes {
		routeCtx := ctx
		cancel := func() {}
		if opts.Hook {
			routeCtx, cancel = context.WithTimeout(ctx, hookNamespaceTimeout)
		}
		branch := branchName(route.Namespace.Name, project.sourceBranch)
		if err := s.ensureBranch(routeCtx, project.sidecarDir, branch); err != nil {
			cancel()
			return SyncResult{}, namespaceSyncError{Namespace: route.Namespace.Name, Err: err}
		}
		if opts.Pull {
			if err := s.pullBranch(routeCtx, project.sidecarDir, branch); err != nil {
				cancel()
				return SyncResult{}, namespaceSyncError{Namespace: route.Namespace.Name, Err: err}
			}
		}
		storageDir := sidecarStoragePath(project.root, route.Namespace.Name)
		sidecarFiles, err := findMatchedFiles(routeCtx, storageDir, route.Namespace.Patterns)
		if err != nil {
			cancel()
			return SyncResult{}, namespaceSyncError{Namespace: route.Namespace.Name, Err: err}
		}
		if err := mirrorFiles(project.root, storageDir, route.Files, sidecarFiles); err != nil {
			cancel()
			return SyncResult{}, namespaceSyncError{Namespace: route.Namespace.Name, Err: err}
		}
		if err := s.git.AddAll(routeCtx, project.sidecarDir); err != nil {
			cancel()
			return SyncResult{}, namespaceSyncError{
				Namespace: route.Namespace.Name,
				Err:       fmt.Errorf("stage sidecar changes: %w", err),
			}
		}
		dirty, err := s.dirty(routeCtx, project.sidecarDir)
		if err != nil {
			cancel()
			return SyncResult{}, namespaceSyncError{Namespace: route.Namespace.Name, Err: err}
		}
		namespaceResult := NamespaceSyncResult{
			Name:         route.Namespace.Name,
			Branch:       branch,
			ChangedFiles: len(route.Files),
		}
		if dirty {
			commit, err := s.commitAndPush(routeCtx, project, branch, route)
			if err != nil {
				cancel()
				return SyncResult{}, namespaceSyncError{Namespace: route.Namespace.Name, Err: err}
			}
			namespaceResult.Committed = true
			namespaceResult.Commit = commit
			result.Committed = true
			if result.Commit == "" {
				result.Commit = commit
			}
		} else if err := s.flushQueuedPush(routeCtx, project, branch); err != nil {
			cancel()
			return SyncResult{}, namespaceSyncError{Namespace: route.Namespace.Name, Err: err}
		}
		cancel()
		result.ChangedFiles += len(route.Files)
		result.Namespaces = append(result.Namespaces, namespaceResult)
	}
	if err := project.store.ClearQueue(); err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

type namespaceSyncError struct {
	Namespace string
	Err       error
}

func (e namespaceSyncError) Error() string {
	return fmt.Sprintf("sync namespace %q: %v", e.Namespace, e.Err)
}

func (e namespaceSyncError) Unwrap() error {
	return e.Err
}

type projectContext struct {
	root         string
	cfg          config.Config
	store        *state.Store
	sidecarDir   string
	sourceBranch string
}

type namespaceRoute struct {
	Namespace config.Namespace
	Files     []string
}

func (s *Service) prepareProject(ctx context.Context, dir string) (projectContext, error) {
	root, cfg, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return projectContext{}, err
	}
	if err := s.ensureClone(ctx, root, cfg.Sidecar); err != nil {
		return projectContext{}, err
	}
	sourceBranch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return projectContext{}, err
	}
	return projectContext{
		root:         root,
		cfg:          cfg,
		store:        store,
		sidecarDir:   sidecarPath(root),
		sourceBranch: sourceBranch,
	}, nil
}

func resolveMainRoutes(ctx context.Context, root string, cfg config.Config) ([]namespaceRoute, error) {
	routes := make([]namespaceRoute, 0, len(cfg.Namespaces))
	owners := make(map[string]string)
	for _, namespace := range cfg.Namespaces {
		files, err := matcher.FindContext(ctx, root, namespace.Patterns)
		if err != nil {
			return nil, err
		}
		owned := make([]string, 0, len(files))
		for _, file := range files {
			if !namespace.Owns(file) {
				continue
			}
			if previous, ok := owners[file]; ok {
				return nil, fmt.Errorf("%s matches multiple skeeper namespaces: %s, %s", file, previous, namespace.Name)
			}
			owners[file] = namespace.Name
			owned = append(owned, file)
		}
		routes = append(routes, namespaceRoute{Namespace: namespace, Files: owned})
	}
	return routes, nil
}

func (s *Service) commitAndPush(
	ctx context.Context,
	project projectContext,
	branch string,
	route namespaceRoute,
) (string, error) {
	mainSHA, err := s.git.HeadSHA(ctx, project.root)
	if err != nil {
		return "", err
	}
	mainSubject, err := s.git.HeadSubject(ctx, project.root)
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
		project.sidecarDir,
		"git",
		"commit",
		"-m",
		message,
		"-m",
		"Main-Commit: "+mainSHA,
	); err != nil {
		return "", fmt.Errorf("commit sidecar changes for namespace %q: %w", route.Namespace.Name, err)
	}
	if _, err := s.runner.Run(
		ctx,
		project.sidecarDir,
		"git",
		"push",
		"-u",
		"origin",
		branch,
	); err != nil {
		return "", fmt.Errorf("push sidecar branch %q: %w", branch, err)
	}
	commit := ""
	if short, err := s.git.HeadShortSHA(ctx, project.sidecarDir); err == nil {
		commit = short
	}
	if err := project.store.AppendLog(
		fmt.Sprintf("synced %d specs in namespace %s at %s", len(route.Files), route.Namespace.Name, commit),
	); err != nil {
		return "", err
	}
	return commit, nil
}

func (s *Service) flushQueuedPush(ctx context.Context, project projectContext, branch string) error {
	queue, err := project.store.Queue()
	if err != nil {
		return err
	}
	if hasHead := s.git.HasHead(ctx, project.sidecarDir); !hasHead {
		return nil
	}
	shouldPush, err := s.shouldPushCleanBranch(ctx, project.sidecarDir, branch, len(queue) > 0)
	if err != nil {
		return err
	}
	if !shouldPush {
		return nil
	}
	if _, err := s.runner.Run(
		ctx,
		project.sidecarDir,
		"git",
		"push",
		"-u",
		"origin",
		branch,
	); err != nil {
		return fmt.Errorf("push queued sidecar branch %q: %w", branch, err)
	}
	return project.store.AppendLog(fmt.Sprintf("flushed pending sidecar push for %s", branch))
}

func (s *Service) shouldPushCleanBranch(ctx context.Context, sidecarDir, branch string, queued bool) (bool, error) {
	if queued {
		return true, nil
	}
	remoteRef := "refs/remotes/origin/" + branch
	if !s.git.RefExists(ctx, sidecarDir, remoteRef) {
		return true, nil
	}
	ahead, _, err := s.git.AheadBehind(ctx, sidecarDir, "HEAD", remoteRef)
	if err != nil {
		return false, fmt.Errorf("compare sidecar branch %q with origin: %w", branch, err)
	}
	return ahead > 0, nil
}

// Status describes the current sidecar state.
type Status struct {
	Sidecar     string
	Branch      string
	Namespaces  []NamespaceStatus
	PendingSync int
}

// NamespaceStatus describes one namespace's sidecar state.
type NamespaceStatus struct {
	Name         string
	Branch       string
	LastCommit   string
	LastUnix     int64
	Remote       string
	TrackedFiles int
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
	routes, err := resolveMainRoutes(ctx, root, cfg)
	if err != nil {
		return Status{}, err
	}
	queue, err := store.Queue()
	if err != nil {
		return Status{}, err
	}
	namespaces := make([]NamespaceStatus, 0, len(routes))
	for _, route := range routes {
		sidecarBranch := branchName(route.Namespace.Name, branch)
		branchAvailable, err := s.useExistingBranch(ctx, sidecarDir, sidecarBranch)
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
		if !s.git.HasHead(ctx, sidecarDir) {
			remote = "no local commits"
		}
		if branchAvailable {
			remote = s.remoteState(ctx, sidecarDir, sidecarBranch)
		}
		namespaces = append(namespaces, NamespaceStatus{
			Name:         route.Namespace.Name,
			Branch:       sidecarBranch,
			LastCommit:   lastCommit,
			LastUnix:     lastUnix,
			Remote:       remote,
			TrackedFiles: len(route.Files),
		})
	}
	return Status{
		Sidecar:     cfg.Sidecar,
		Branch:      branch,
		Namespaces:  namespaces,
		PendingSync: len(queue),
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
	namespace, err := ownerForPath(cfg, rel)
	if err != nil {
		return "", err
	}
	sourceBranch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return "", err
	}
	ok, err := s.useExistingBranch(ctx, sidecarPath(root), branchName(namespace.Name, sourceBranch))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	sidecarRel := sidecarFilePath(namespace.Name, rel)
	result, err := s.runner.Run(ctx, sidecarPath(root), "git", "log", "--pretty=format:%h\t%cr\t%s", "--", sidecarRel)
	if err != nil {
		return "", fmt.Errorf("read sidecar log for %s: %w", rel, err)
	}
	return result.Stdout, nil
}

func ownerForPath(cfg config.Config, rel string) (config.Namespace, error) {
	var owner config.Namespace
	count := 0
	for _, namespace := range cfg.Namespaces {
		if namespace.Owns(rel) {
			count++
			if count == 1 {
				owner = namespace
			}
		}
	}
	switch count {
	case 0:
		return config.Namespace{}, fmt.Errorf("%s does not match configured skeeper namespaces", rel)
	case 1:
		return owner, nil
	default:
		return config.Namespace{}, fmt.Errorf(
			"%s matches multiple skeeper namespaces including %s",
			rel,
			owner.Name,
		)
	}
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
	if !s.git.HasHead(ctx, sidecarDir) {
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

func (s *Service) useExistingBranch(ctx context.Context, sidecarDir, branch string) (bool, error) {
	if s.git.RefExists(ctx, sidecarDir, "refs/heads/"+branch) {
		_, switchErr := s.runner.Run(ctx, sidecarDir, "git", "switch", branch)
		return switchErr == nil, switchErr
	}
	if s.git.RefExists(ctx, sidecarDir, "refs/remotes/origin/"+branch) {
		_, switchErr := s.runner.Run(ctx, sidecarDir, "git", "switch", "--track", "origin/"+branch)
		return switchErr == nil, switchErr
	}
	return false, nil
}

func (s *Service) ensureBranch(ctx context.Context, sidecarDir, branch string) error {
	if s.git.RefExists(ctx, sidecarDir, "refs/heads/"+branch) {
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "switch", branch); err != nil {
			return fmt.Errorf("switch sidecar branch %q: %w", branch, err)
		}
		if err := s.prepareBranchWorktree(ctx, sidecarDir, branch); err != nil {
			return err
		}
		return nil
	}
	if s.git.RefExists(ctx, sidecarDir, "refs/remotes/origin/"+branch) {
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "switch", "--track", "origin/"+branch); err != nil {
			return fmt.Errorf("track sidecar branch %q: %w", branch, err)
		}
		if err := s.prepareBranchWorktree(ctx, sidecarDir, branch); err != nil {
			return err
		}
		return nil
	}
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "switch", "--orphan", branch); err != nil {
		return fmt.Errorf("create sidecar branch %q: %w", branch, err)
	}
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "read-tree", "--empty"); err != nil {
		return fmt.Errorf("clear sidecar branch %q index: %w", branch, err)
	}
	if err := clearSidecarWorktree(sidecarDir); err != nil {
		return fmt.Errorf("clear sidecar branch %q worktree: %w", branch, err)
	}
	return nil
}

func (s *Service) prepareBranchWorktree(ctx context.Context, sidecarDir, branch string) error {
	if !s.git.HasHead(ctx, sidecarDir) {
		return nil
	}
	if err := s.git.ResetAndClean(ctx, sidecarDir); err != nil {
		return fmt.Errorf("prepare sidecar branch %q worktree: %w", branch, err)
	}
	return nil
}

func clearSidecarWorktree(sidecarDir string) error {
	entries, err := os.ReadDir(sidecarDir)
	if err != nil {
		return fmt.Errorf("read sidecar worktree %s: %w", sidecarDir, err)
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		path := filepath.Join(sidecarDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("clear sidecar worktree path %s: %w", path, err)
		}
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
		reason = "sync timed out: " + reason
	}
	entry := state.Entry{Time: time.Now().UTC(), Reason: reason, MainSHA: mainSHA}
	var namespaceErr namespaceSyncError
	if errors.As(syncErr, &namespaceErr) {
		entry.Namespace = namespaceErr.Namespace
	}
	if err := store.Enqueue(entry); err != nil {
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

func validateExistingConfig(cfg config.Config, opts InitOptions) error {
	sidecarURL := strings.TrimSpace(opts.Sidecar)
	if sidecarURL != "" && cfg.Sidecar != sidecarURL {
		return fmt.Errorf("%s already exists with incompatible sidecar %q", config.Filename, cfg.Sidecar)
	}
	name := strings.TrimSpace(opts.SidecarName)
	if name != "" && !sidecarNameMatches(cfg.Sidecar, name) {
		return fmt.Errorf("%s already exists with incompatible sidecar %q", config.Filename, cfg.Sidecar)
	}
	if opts.NamespaceSet {
		namespaceName, err := config.CleanNamespace(opts.Namespace)
		if err != nil {
			return err
		}
		if len(cfg.Namespaces) != 1 || cfg.Namespaces[0].Name != namespaceName {
			return fmt.Errorf("%s already exists with incompatible namespace", config.Filename)
		}
	}
	bootstrap := strings.TrimSpace(opts.Bootstrap)
	if bootstrap != "" && bootstrap != cfg.Bootstrap {
		return fmt.Errorf("%s already exists with incompatible bootstrap", config.Filename)
	}
	patterns, err := config.NormalizePatterns(opts.Patterns)
	if err != nil {
		return err
	}
	if len(patterns) > 0 && (len(cfg.Namespaces) != 1 || !sameStrings(patterns, cfg.Namespaces[0].Patterns)) {
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

func sidecarPath(root string) string {
	return filepath.Join(root, DirName)
}

func sidecarStoragePath(root string, namespace string) string {
	return filepath.Join(sidecarPath(root), filepath.FromSlash(namespace))
}

func sidecarFilePath(namespace string, rel string) string {
	return filepath.ToSlash(filepath.Join(filepath.FromSlash(namespace), filepath.FromSlash(rel)))
}

func findMatchedFiles(ctx context.Context, root string, patterns []string) ([]string, error) {
	if !exists(root) {
		return nil, nil
	}
	return matcher.FindContext(ctx, root, patterns)
}

func branchName(namespace, sourceBranch string) string {
	return namespace + "/" + config.NamespaceBranchSegment + "/" + sourceBranch
}

// DefaultNamespace returns a safe sidecar namespace derived from a repo name.
func DefaultNamespace(repoName string) string {
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
	namespace := strings.Trim(b.String(), "-.")
	if namespace == "" {
		return "project"
	}
	if _, err := config.CleanNamespace(namespace); err != nil {
		return "project"
	}
	return namespace
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
