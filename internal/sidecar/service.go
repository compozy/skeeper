// Package sidecar coordinates skeeper's mirrored Git repository.
package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/hooks"
	"github.com/compozy/skeeper/internal/lockfile"
	"github.com/compozy/skeeper/internal/matcher"
	"github.com/compozy/skeeper/internal/reconcile"
	"github.com/compozy/skeeper/internal/state"
)

const (
	// DirName is the sidecar clone directory in the main worktree.
	DirName       = ".skeeper"
	stateDirName  = "skeeper"
	statusUnknown = "unknown"
	pendingCommit = "0000000000000000000000000000000000000000"
)

// Service executes sidecar workflows.
type Service struct {
	runner  gitexec.Runner
	git     *gitexec.Git
	planner reconcile.Planner
	locks   *lockfile.JSONStore
	hooks   hooks.Manager
}

// New returns a sidecar service.
func New(runner gitexec.Runner) *Service {
	return &Service{
		runner:  runner,
		git:     gitexec.NewGit(),
		planner: reconcile.NewPlanner(runner),
		locks:   lockfile.NewStore(runner),
		hooks:   hooks.NewManager(runner),
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

// SyncOptions configures a sync run.
type SyncOptions struct {
	DryRun  bool
	JSON    bool
	Commit  bool
	Message string
	Hook    bool
	Staged  bool
	Force   bool
	// Pull is accepted for older call sites; strict sync always fetches/rebases.
	Pull bool
}

// SyncResult reports a completed sync.
type SyncResult struct {
	ChangedFiles int                   `json:"changed_files"`
	Committed    bool                  `json:"committed"`
	Commit       string                `json:"commit,omitempty"`
	Namespaces   []NamespaceSyncResult `json:"namespaces"`
	LockPath     string                `json:"lock_path,omitempty"`
	DryRun       bool                  `json:"dry_run,omitempty"`
}

// NamespaceSyncResult reports one namespace sync outcome.
type NamespaceSyncResult struct {
	Name         string                   `json:"name"`
	Branch       string                   `json:"branch"`
	ChangedFiles int                      `json:"changed_files"`
	Committed    bool                     `json:"committed"`
	Commit       string                   `json:"commit"`
	Digest       lockfile.NamespaceDigest `json:"digest"`
	Files        int                      `json:"files"`
	Bytes        int64                    `json:"bytes"`
	Warnings     []reconcile.Diagnostic   `json:"warnings,omitempty"`
}

// VerifyOptions configures lock verification.
type VerifyOptions struct {
	JSON         bool
	Hook         bool
	SourceBranch string
}

// VerifyResult reports lock verification.
type VerifyResult struct {
	OK          bool                    `json:"ok"`
	Namespaces  []NamespaceVerification `json:"namespaces"`
	Diagnostics []reconcile.Diagnostic  `json:"diagnostics,omitempty"`
}

// NamespaceVerification reports one namespace verification.
type NamespaceVerification struct {
	Name     string                   `json:"name"`
	Branch   string                   `json:"branch"`
	Commit   string                   `json:"commit"`
	Digest   lockfile.NamespaceDigest `json:"digest"`
	Expected lockfile.NamespaceDigest `json:"expected_digest"`
	OK       bool                     `json:"ok"`
}

// FSCKOptions configures working-tree drift checks.
type FSCKOptions struct {
	JSON         bool
	SourceBranch string
}

// FSCKResult reports working-tree drift against the lock.
type FSCKResult struct {
	OK          bool                   `json:"ok"`
	Diagnostics []reconcile.Diagnostic `json:"diagnostics,omitempty"`
}

// Status describes the current sidecar state.
type Status struct {
	Sidecar     string                 `json:"sidecar"`
	Branch      string                 `json:"branch"`
	LockPresent bool                   `json:"lock_present"`
	LockCommit  string                 `json:"lock_commit,omitempty"`
	Namespaces  []NamespaceStatus      `json:"namespaces"`
	Transaction *state.Transaction     `json:"transaction,omitempty"`
	Bypass      *state.Bypass          `json:"bypass,omitempty"`
	Diagnostics []reconcile.Diagnostic `json:"diagnostics,omitempty"`
}

// NamespaceStatus describes one namespace's sidecar state.
type NamespaceStatus struct {
	Name         string                   `json:"name"`
	Branch       string                   `json:"branch"`
	LastCommit   string                   `json:"last_commit,omitempty"`
	LastUnix     int64                    `json:"last_unix,omitempty"`
	Remote       string                   `json:"remote"`
	TrackedFiles int                      `json:"tracked_files"`
	LockedCommit string                   `json:"locked_commit,omitempty"`
	LockedDigest lockfile.NamespaceDigest `json:"locked_digest,omitempty"`
}

// LogOptions configures sidecar history output.
type LogOptions struct {
	Latest       bool
	SourceBranch string
}

// MergeDriverOptions contains paths passed by Git's custom merge-driver protocol.
type MergeDriverOptions struct {
	BasePath    string `json:"base_path,omitempty"`
	CurrentPath string `json:"current_path,omitempty"`
	OtherPath   string `json:"other_path,omitempty"`
}

// MergeDriverResult reports a regenerated lockfile merge result.
type MergeDriverResult struct {
	LockPath     string                 `json:"lock_path"`
	OutputPath   string                 `json:"output_path,omitempty"`
	Namespaces   []MergeDriverNamespace `json:"namespaces"`
	BasePath     string                 `json:"base_path,omitempty"`
	CurrentPath  string                 `json:"current_path,omitempty"`
	OtherPath    string                 `json:"other_path,omitempty"`
	ChangedFiles int                    `json:"changed_files"`
}

// MergeDriverNamespace reports regenerated lock data for one namespace.
type MergeDriverNamespace struct {
	Name   string                   `json:"name"`
	Branch string                   `json:"branch"`
	Digest lockfile.NamespaceDigest `json:"digest"`
	Files  int                      `json:"files"`
	Bytes  int64                    `json:"bytes"`
}

// MutateOptions configures adopt/untrack.
type MutateOptions struct {
	DryRun  bool
	JSON    bool
	Force   bool
	Commit  bool
	Message string
}

// MutateResult reports adopt/untrack changes.
type MutateResult struct {
	Plan    reconcile.Plan             `json:"plan"`
	Changed []reconcile.TargetDecision `json:"changed"`
	DryRun  bool                       `json:"dry_run,omitempty"`
}

// PatternTestOptions configures pattern test.
type PatternTestOptions struct {
	Namespace string
	JSON      bool
}

// PatternTestResult reports pattern test matches.
type PatternTestResult struct {
	Namespace string   `json:"namespace"`
	Glob      string   `json:"glob"`
	Matches   []string `json:"matches"`
}

// PatternAddOptions configures pattern add.
type PatternAddOptions struct {
	Namespace     string
	Exclude       []string
	AdoptExisting bool
	DryRun        bool
	JSON          bool
	Force         bool
	Commit        bool
	Message       string
}

// PatternAddResult reports pattern add.
type PatternAddResult struct {
	ConfigPath string         `json:"config_path"`
	Gitignore  string         `json:"gitignore"`
	Plan       reconcile.Plan `json:"plan"`
	DryRun     bool           `json:"dry_run,omitempty"`
}

// RepairStatus reports local-only repair state.
type RepairStatus struct {
	Transaction *state.Transaction `json:"transaction,omitempty"`
	Bypass      *state.Bypass      `json:"bypass,omitempty"`
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
	if err := s.ensureSidecarCommitIdentity(ctx, root, sidecarDir); err != nil {
		return InitResult{}, err
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
	loaded, err := config.Load(root)
	if err != nil {
		return InitResult{}, err
	}
	if err := UpdateGitignore(root, loaded.Namespaces); err != nil {
		return InitResult{}, err
	}
	installed, err := s.hooks.Install(ctx, reconcile.RepoRoot(root), hooks.InstallOptions{Config: loaded})
	if err != nil {
		return InitResult{}, err
	}
	return InitResult{
		Root:      root,
		Sidecar:   sidecarDir,
		Config:    loaded,
		HookPath:  installed.PreCommit,
		Gitignore: filepath.Join(root, ".gitignore"),
	}, nil
}

// Hydrate clones the sidecar if needed and restores spec files from skeeper.lock.
func (s *Service) Hydrate(ctx context.Context, dir string) (HydrateResult, error) {
	root, cfg, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return HydrateResult{}, err
	}
	if err := s.ensureClone(ctx, root, cfg.Sidecar); err != nil {
		return HydrateResult{}, err
	}
	lock, err := s.locks.Load(reconcile.RepoRoot(root))
	if err != nil {
		return HydrateResult{}, err
	}
	sidecarDir := sidecarPath(root)
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "fetch", "origin"); err != nil {
		return HydrateResult{}, fmt.Errorf("fetch sidecar origin: %w", err)
	}
	restored := make([]string, 0)
	for _, record := range lock.Namespaces {
		if err := s.ensureCommit(ctx, sidecarDir, record.Commit); err != nil {
			return HydrateResult{}, err
		}
		paths, err := s.treePaths(ctx, sidecarDir, record.Commit, record.Name)
		if err != nil {
			return HydrateResult{}, err
		}
		for _, sidecarRel := range paths {
			mainRel := strings.TrimPrefix(sidecarRel, record.Name+"/")
			blob, err := s.runner.Run(ctx, sidecarDir, "git", "show", record.Commit+":"+sidecarRel)
			if err != nil {
				return HydrateResult{}, fmt.Errorf("read sidecar blob %s: %w", sidecarRel, err)
			}
			if err := writeStringFile(
				filepath.Join(root, filepath.FromSlash(mainRel)),
				blob.Stdout,
				0o644,
			); err != nil {
				return HydrateResult{}, err
			}
			restored = append(restored, mainRel)
		}
	}
	installed, err := s.hooks.Install(ctx, reconcile.RepoRoot(root), hooks.InstallOptions{Config: cfg})
	if err != nil {
		return HydrateResult{}, err
	}
	_ = installed
	sort.Strings(restored)
	return HydrateResult{Restored: restored, Commit: lockCommitSummary(lock)}, nil
}

// HydrateResult reports files restored by hydrate.
type HydrateResult struct {
	Restored []string `json:"restored"`
	Commit   string   `json:"commit"`
}

// Sync mirrors main-tree spec files into the sidecar, pushes, writes, and stages skeeper.lock.
func (s *Service) Sync(ctx context.Context, dir string, opts SyncOptions) (SyncResult, error) {
	root, _, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return SyncResult{}, err
	}
	plan, err := s.planner.PlanSync(ctx, reconcile.RepoRoot(root), reconcile.SyncPlanOptions{
		Staged: opts.Hook || opts.Staged,
		Force:  opts.Force || opts.Hook,
	})
	if err != nil {
		return SyncResult{}, err
	}
	if opts.DryRun {
		return SyncResult{DryRun: true, ChangedFiles: plan.Guardrails.Files}, nil
	}
	tx := state.Transaction{
		Kind:       string(plan.Kind),
		Root:       root,
		Targets:    planFilePaths(plan),
		Namespaces: namespaceNames(plan.Namespaces),
	}
	if err := store.Begin(ctx, tx); err != nil {
		return SyncResult{}, err
	}
	current, _, err := store.Current(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	result, syncErr := s.applySyncPlan(ctx, plan)
	if syncErr != nil {
		return SyncResult{}, syncErr
	}
	if err := store.MarkPhase(ctx, current.ID, state.TransactionPhaseSidecarPushed); err != nil {
		return SyncResult{}, err
	}
	lock := lockfile.Lock{
		Version:      lockfile.Version,
		Sidecar:      lockfile.CanonicalSidecarURL(plan.SidecarURL),
		SourceBranch: plan.SourceBranch,
		Namespaces:   namespaceRecords(result.Namespaces),
	}
	if err := s.locks.Write(reconcile.RepoRoot(root), lock); err != nil {
		return SyncResult{}, err
	}
	if _, err := s.runner.Run(ctx, root, "git", "add", lockfile.Filename); err != nil {
		return SyncResult{}, fmt.Errorf("stage %s: %w", lockfile.Filename, err)
	}
	if err := store.MarkPhase(ctx, current.ID, state.TransactionPhaseLockStaged); err != nil {
		return SyncResult{}, err
	}
	result.LockPath = filepath.Join(root, lockfile.Filename)
	if err := store.ClearBypass(ctx); err != nil {
		return SyncResult{}, err
	}
	if opts.Commit {
		commit, err := s.commitMain(ctx, root, opts.Message)
		if err != nil {
			return SyncResult{}, err
		}
		result.Committed = true
		result.Commit = commit
	}
	if err := store.Complete(ctx, current.ID); err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

// Verify validates skeeper.lock against the sidecar remote.
func (s *Service) Verify(ctx context.Context, dir string, opts VerifyOptions) (VerifyResult, error) {
	root, cfg, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return VerifyResult{}, err
	}
	ctx, cancel, err := verifyContext(ctx, cfg, opts)
	if err != nil {
		return VerifyResult{}, err
	}
	defer cancel()
	lock, err := s.locks.Load(reconcile.RepoRoot(root))
	if err != nil {
		return VerifyResult{}, err
	}
	result, err := s.verifyPreamble(ctx, cfg, store, lock, opts)
	if err != nil {
		return VerifyResult{}, err
	}
	sidecarDir := sidecarPath(root)
	if opts.Hook && !exists(filepath.Join(sidecarDir, ".git")) {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, diagnostic(
			"lock.clone_missing",
			"sidecar clone is missing; run `skeeper sync` before pushing",
			"",
			"skeeper sync",
		))
		return result, nil
	}
	if err := s.ensureClone(ctx, root, cfg.Sidecar); err != nil {
		return VerifyResult{}, err
	}
	if err := s.fetchLockedBranches(ctx, sidecarDir, lock); err != nil {
		return VerifyResult{}, err
	}
	return s.verifyNamespaces(ctx, sidecarDir, cfg, lock, result)
}

func verifyContext(
	ctx context.Context,
	cfg config.Config,
	opts VerifyOptions,
) (context.Context, context.CancelFunc, error) {
	if !opts.Hook {
		return ctx, func() {}, nil
	}
	timeout, err := time.ParseDuration(cfg.Settings.Hooks.PrePushTimeout)
	if err != nil {
		return nil, nil, err
	}
	next, cancel := context.WithTimeout(ctx, timeout)
	return next, cancel, nil
}

func (s *Service) verifyPreamble(
	ctx context.Context,
	cfg config.Config,
	store *state.Store,
	lock lockfile.Lock,
	opts VerifyOptions,
) (VerifyResult, error) {
	result := VerifyResult{OK: true}
	if lockfile.CanonicalSidecarURL(cfg.Sidecar) != lock.Sidecar {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, diagnostic(
			"lock.sidecar_mismatch",
			"lock sidecar does not match .skeeper.yml sidecar; run `skeeper sync`",
			"",
			"",
		))
	}
	if opts.SourceBranch != "" && lock.SourceBranch != opts.SourceBranch {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, diagnostic(
			"lock.source_branch_mismatch",
			fmt.Sprintf("lock source_branch is %q, expected %q", lock.SourceBranch, opts.SourceBranch),
			"",
			"",
		))
	}
	if bypass, ok, err := store.Bypass(ctx); err != nil {
		return VerifyResult{}, err
	} else if ok {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, diagnostic(
			"bypass.active",
			fmt.Sprintf(
				"strict hook bypass recorded at %s; run `skeeper sync`",
				bypass.Time.Format(time.RFC3339),
			),
			"",
			"skeeper sync",
		))
	}
	return result, nil
}

func (s *Service) verifyNamespaces(
	ctx context.Context,
	sidecarDir string,
	cfg config.Config,
	lock lockfile.Lock,
	result VerifyResult,
) (VerifyResult, error) {
	for _, record := range lock.Namespaces {
		nsResult := NamespaceVerification{
			Name:     record.Name,
			Branch:   record.SidecarBranch,
			Commit:   record.Commit,
			Expected: record.Digest,
		}
		namespace, err := namespaceByName(cfg, record.Name)
		if err != nil {
			result.OK = false
			result.Diagnostics = append(
				result.Diagnostics,
				diagnostic("lock.namespace_missing", err.Error(), record.Name, "skeeper sync"),
			)
			result.Namespaces = append(result.Namespaces, nsResult)
			continue
		}
		if err := s.ensureCommit(ctx, sidecarDir, record.Commit); err != nil {
			result.OK = false
			result.Diagnostics = append(
				result.Diagnostics,
				diagnostic("lock.commit_missing", err.Error(), record.Name, "skeeper sync"),
			)
			result.Namespaces = append(result.Namespaces, nsResult)
			continue
		}
		digest, err := s.locks.DigestResult(ctx, sidecarDir, namespace, reconcile.SidecarRef(record.Commit))
		if err != nil {
			return VerifyResult{}, err
		}
		nsResult.Digest = digest.Digest
		nsResult.OK = digest.Digest == record.Digest && digest.Files == record.Files && digest.Bytes == record.Bytes
		if !nsResult.OK {
			result.OK = false
			result.Diagnostics = append(result.Diagnostics, diagnostic(
				"lock.digest_mismatch",
				fmt.Sprintf("namespace %s digest is %s, expected %s", record.Name, digest.Digest, record.Digest),
				record.Name,
				"skeeper sync",
			))
		}
		result.Namespaces = append(result.Namespaces, nsResult)
	}
	return result, nil
}

func (s *Service) fetchLockedBranches(ctx context.Context, sidecarDir string, lock lockfile.Lock) error {
	seen := map[string]struct{}{}
	for _, record := range lock.Namespaces {
		if _, ok := seen[record.SidecarBranch]; ok {
			continue
		}
		seen[record.SidecarBranch] = struct{}{}
		refspec := fmt.Sprintf(
			"refs/heads/%s:refs/remotes/origin/%s",
			record.SidecarBranch,
			record.SidecarBranch,
		)
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "fetch", "origin", refspec); err != nil {
			return fmt.Errorf("fetch sidecar branch %s: %w", record.SidecarBranch, err)
		}
	}
	return nil
}

// FSCK compares working-tree owned specs against skeeper.lock.
func (s *Service) FSCK(ctx context.Context, dir string, opts FSCKOptions) (FSCKResult, error) {
	root, _, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return FSCKResult{}, err
	}
	lock, err := s.locks.Load(reconcile.RepoRoot(root))
	if err != nil {
		return FSCKResult{}, err
	}
	plan, err := s.planner.PlanFSCK(
		ctx,
		reconcile.RepoRoot(root),
		reconcile.FSCKPlanOptions{SourceBranch: opts.SourceBranch},
	)
	if err != nil {
		return FSCKResult{}, err
	}
	result := FSCKResult{OK: true}
	if bypass, ok, err := store.Bypass(ctx); err != nil {
		return FSCKResult{}, err
	} else if ok {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, diagnostic(
			"bypass.active",
			fmt.Sprintf("strict hook bypass recorded at %s; run `skeeper sync`", bypass.Time.Format(time.RFC3339)),
			"",
			"skeeper sync",
		))
	}
	for _, record := range lock.Namespaces {
		namespacePlan, ok := planNamespace(plan, record.Name)
		if !ok {
			result.OK = false
			result.Diagnostics = append(result.Diagnostics, diagnostic(
				"fsck.namespace_missing",
				fmt.Sprintf("namespace %s is present in lock but missing from config", record.Name),
				record.Name,
				"skeeper sync",
			))
			continue
		}
		files := filePlanPaths(namespacePlan.Files)
		digest, err := lockfile.DigestWorkingTree(root, files, namespacePlan.StagedContent)
		if err != nil {
			return FSCKResult{}, err
		}
		if digest.Digest != record.Digest || digest.Files != record.Files || digest.Bytes != record.Bytes {
			result.OK = false
			result.Diagnostics = append(result.Diagnostics, diagnostic(
				"fsck.working_tree_drift",
				fmt.Sprintf("namespace %s working tree differs from locked sidecar content", record.Name),
				record.Name,
				"skeeper sync",
			))
		}
	}
	return result, nil
}

// Status returns a summary suitable for CLI display.
func (s *Service) Status(ctx context.Context, dir string) (Status, error) {
	root, cfg, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return Status{}, err
	}
	branch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return Status{}, err
	}
	plan, err := s.planner.PlanSync(ctx, reconcile.RepoRoot(root), reconcile.SyncPlanOptions{Force: true})
	if err != nil {
		return Status{}, err
	}
	status := Status{Sidecar: cfg.Sidecar, Branch: branch}
	if tx, ok, err := store.Current(ctx); err != nil {
		return Status{}, err
	} else if ok {
		status.Transaction = &tx
	}
	if bypass, ok, err := store.Bypass(ctx); err != nil {
		return Status{}, err
	} else if ok {
		status.Bypass = &bypass
		status.Diagnostics = append(
			status.Diagnostics,
			diagnostic("bypass.active", "strict hook bypass is active; run `skeeper sync`", "", "skeeper sync"),
		)
	}
	lock, lockErr := s.locks.Load(reconcile.RepoRoot(root))
	if lockErr == nil {
		status.LockPresent = true
		status.LockCommit = lockCommitSummary(lock)
	}
	for _, namespace := range plan.Namespaces {
		nsStatus := NamespaceStatus{
			Name:         string(namespace.Name),
			Branch:       namespace.Branch,
			TrackedFiles: len(namespace.Files),
			Remote:       "not checked",
		}
		if status.LockPresent {
			if record, ok := lockRecord(lock, string(namespace.Name)); ok {
				nsStatus.LockedCommit = shortHash(record.Commit)
				nsStatus.LockedDigest = record.Digest
				nsStatus.LastCommit = shortHash(record.Commit)
			}
		}
		if exists(filepath.Join(root, DirName, ".git")) {
			nsStatus.Remote = s.remoteState(ctx, sidecarPath(root), namespace.Branch)
			if info, err := s.git.LastCommit(ctx, sidecarPath(root)); err == nil {
				nsStatus.LastUnix = info.Unix
			}
		}
		status.Namespaces = append(status.Namespaces, nsStatus)
	}
	return status, nil
}

// Log returns sidecar history for a mirrored file path.
func (s *Service) Log(ctx context.Context, dir, path string, options ...LogOptions) (string, error) {
	opts := LogOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	root, cfg, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return "", err
	}
	if err := s.ensureClone(ctx, root, cfg.Sidecar); err != nil {
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
	sourceBranch := opts.SourceBranch
	if sourceBranch == "" {
		sourceBranch, err = s.git.CurrentBranch(ctx, root)
		if err != nil {
			return "", err
		}
	}
	branch := reconcile.BranchName(namespace.Name, sourceBranch)
	logRef, err := s.logRef(ctx, root, namespace.Name, branch, opts)
	if err != nil {
		return "", err
	}
	sidecarRel := sidecarFilePath(namespace.Name, rel)
	result, err := s.runner.Run(
		ctx,
		sidecarPath(root),
		"git",
		"log",
		"--pretty=format:%h\t%cr\t%s",
		logRef.Ref,
		"--",
		sidecarRel,
	)
	if err != nil {
		return "", fmt.Errorf("read %s sidecar log for %s: %w", logRef.Label, rel, err)
	}
	return logRef.Header + result.Stdout, nil
}

type logRef struct {
	Ref    string
	Label  string
	Header string
}

func (s *Service) logRef(ctx context.Context, root, namespaceName, branch string, opts LogOptions) (logRef, error) {
	if !opts.Latest {
		record, err := s.lockRecordForLog(root, namespaceName)
		if err != nil {
			return logRef{}, err
		}
		return logRef{Ref: record.Commit, Label: "locked"}, nil
	}
	if _, err := s.runner.Run(ctx, sidecarPath(root), "git", "fetch", "origin", branch); err != nil {
		return logRef{}, fmt.Errorf("fetch sidecar branch %s: %w", branch, err)
	}
	ref := "refs/remotes/origin/" + branch
	latestCommit, err := s.revParse(ctx, sidecarPath(root), ref)
	if err != nil {
		return logRef{}, fmt.Errorf("read latest sidecar ref %s: %w", ref, err)
	}
	record, err := s.lockRecordForLog(root, namespaceName)
	if err != nil {
		return logRef{}, err
	}
	state := "up-to-date"
	if record.Commit != latestCommit {
		state = "diverged"
	}
	header := fmt.Sprintf(
		"latest: %s\nlocked: %s\nstate: %s\n\n",
		shortHash(latestCommit),
		shortHash(record.Commit),
		state,
	)
	return logRef{Ref: ref, Label: "latest", Header: header}, nil
}

func (s *Service) lockRecordForLog(root, namespaceName string) (lockfile.NamespaceRecord, error) {
	lock, err := s.locks.Load(reconcile.RepoRoot(root))
	if err != nil {
		return lockfile.NamespaceRecord{}, err
	}
	record, ok := lockRecord(lock, namespaceName)
	if !ok {
		return lockfile.NamespaceRecord{}, fmt.Errorf(
			"namespace %s is missing from %s",
			namespaceName,
			lockfile.Filename,
		)
	}
	return record, nil
}

// Adopt stops tracking files in the main index after sidecar coverage is pushed.
func (s *Service) Adopt(ctx context.Context, dir string, targets []string, opts MutateOptions) (MutateResult, error) {
	return s.mutateCoverage(ctx, dir, targets, opts, true)
}

// Untrack removes main-index tracking while preserving sidecar coverage.
func (s *Service) Untrack(ctx context.Context, dir string, targets []string, opts MutateOptions) (MutateResult, error) {
	return s.mutateCoverage(ctx, dir, targets, opts, false)
}

// PatternTest reports matches for a candidate glob.
func (s *Service) PatternTest(
	ctx context.Context,
	dir, glob string,
	opts PatternTestOptions,
) (PatternTestResult, error) {
	root, cfg, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return PatternTestResult{}, err
	}
	patterns, err := config.NormalizePatterns([]string{glob})
	if err != nil {
		return PatternTestResult{}, err
	}
	namespace, err := inferNamespace(cfg, opts.Namespace)
	if err != nil {
		return PatternTestResult{}, err
	}
	matches, err := matcher.FindContextWithOptions(
		ctx,
		root,
		patterns,
		matcher.Options{RespectGitignore: namespace.RespectsGitignore()},
	)
	if err != nil {
		return PatternTestResult{}, err
	}
	return PatternTestResult{Namespace: namespace.Name, Glob: patterns[0], Matches: matches}, nil
}

// PatternAdd adds a namespace pattern and optionally adopts existing matches.
func (s *Service) PatternAdd(ctx context.Context, dir, glob string, opts PatternAddOptions) (PatternAddResult, error) {
	root, cfg, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return PatternAddResult{}, err
	}
	patterns, err := config.NormalizePatterns([]string{glob})
	if err != nil {
		return PatternAddResult{}, err
	}
	exclude, err := config.NormalizePatterns(opts.Exclude)
	if err != nil {
		return PatternAddResult{}, err
	}
	namespace, err := inferNamespace(cfg, opts.Namespace)
	if err != nil {
		return PatternAddResult{}, err
	}
	for i := range cfg.Namespaces {
		if cfg.Namespaces[i].Name == namespace.Name {
			cfg.Namespaces[i].Patterns = appendUnique(cfg.Namespaces[i].Patterns, patterns...)
			cfg.Namespaces[i].Exclude = appendUnique(cfg.Namespaces[i].Exclude, exclude...)
		}
	}
	normalized, err := cfg.Normalize()
	if err != nil {
		return PatternAddResult{}, err
	}
	if err := validateNoOverlap(ctx, root, normalized); err != nil {
		return PatternAddResult{}, err
	}
	plan, err := s.planner.PlanPattern(ctx, reconcile.RepoRoot(root), patterns[0], reconcile.PatternPlanOptions{
		Namespace:     namespace.Name,
		Exclude:       exclude,
		AdoptExisting: opts.AdoptExisting,
		Force:         opts.Force,
	})
	if err != nil {
		return PatternAddResult{}, err
	}
	result := PatternAddResult{
		ConfigPath: filepath.Join(root, config.Filename),
		Gitignore:  filepath.Join(root, ".gitignore"),
		Plan:       plan,
		DryRun:     opts.DryRun,
	}
	if opts.DryRun {
		return result, nil
	}
	if err := config.Save(root, normalized); err != nil {
		return PatternAddResult{}, err
	}
	if err := UpdateGitignore(root, normalized.Namespaces); err != nil {
		return PatternAddResult{}, err
	}
	if _, err := s.runner.Run(ctx, root, "git", "add", config.Filename, ".gitignore"); err != nil {
		return PatternAddResult{}, fmt.Errorf("stage pattern changes: %w", err)
	}
	if opts.AdoptExisting {
		if _, err := s.Adopt(ctx, root, []string{patterns[0]}, MutateOptions{
			Force:   opts.Force,
			Commit:  opts.Commit,
			Message: opts.Message,
		}); err != nil {
			return PatternAddResult{}, err
		}
	} else if opts.Commit {
		if _, err := s.commitMain(ctx, root, opts.Message); err != nil {
			return PatternAddResult{}, err
		}
	}
	return result, nil
}

// HooksInstall installs managed hooks.
func (s *Service) HooksInstall(ctx context.Context, dir string) (hooks.InstallResult, error) {
	root, cfg, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return hooks.InstallResult{}, err
	}
	return s.hooks.Install(ctx, reconcile.RepoRoot(root), hooks.InstallOptions{Config: cfg})
}

// HooksCheck checks managed hooks.
func (s *Service) HooksCheck(ctx context.Context, dir string) (hooks.CheckResult, error) {
	root, _, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return hooks.CheckResult{}, err
	}
	return s.hooks.Check(ctx, reconcile.RepoRoot(root))
}

// MergeDriver regenerates skeeper.lock for Git merge-driver invocations.
func (s *Service) MergeDriver(
	ctx context.Context,
	dir string,
	opts MergeDriverOptions,
) (MergeDriverResult, error) {
	root, cfg, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return MergeDriverResult{}, err
	}
	plan, err := s.planner.PlanSync(ctx, reconcile.RepoRoot(root), reconcile.SyncPlanOptions{
		Staged: true,
		Force:  true,
	})
	if err != nil {
		return MergeDriverResult{}, err
	}
	outputPath := filepath.Join(root, lockfile.Filename)
	if opts.CurrentPath != "" {
		outputPath, err = mergeDriverOutputPath(root, opts.CurrentPath)
		if err != nil {
			return MergeDriverResult{}, err
		}
	}
	seed, err := s.mergeDriverSeedLock(root, opts)
	if err != nil {
		return MergeDriverResult{}, err
	}
	lock, namespaces, err := s.mergeDriverLock(root, cfg, plan, seed)
	if err != nil {
		return MergeDriverResult{}, err
	}
	data, err := lockfile.Marshal(lock)
	if err != nil {
		return MergeDriverResult{}, err
	}
	if err := writeStringFile(outputPath, string(data), 0o644); err != nil {
		return MergeDriverResult{}, fmt.Errorf("write merge-driver output %s: %w", outputPath, err)
	}
	result := MergeDriverResult{
		LockPath:     filepath.Join(root, lockfile.Filename),
		OutputPath:   outputPath,
		Namespaces:   namespaces,
		BasePath:     opts.BasePath,
		CurrentPath:  opts.CurrentPath,
		OtherPath:    opts.OtherPath,
		ChangedFiles: plan.Guardrails.Files,
	}
	return result, nil
}

// RepairStatus returns current repair state.
func (s *Service) RepairStatus(ctx context.Context, dir string) (RepairStatus, error) {
	_, _, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return RepairStatus{}, err
	}
	result := RepairStatus{}
	if tx, ok, err := store.Current(ctx); err != nil {
		return RepairStatus{}, err
	} else if ok {
		result.Transaction = &tx
	}
	if bypass, ok, err := store.Bypass(ctx); err != nil {
		return RepairStatus{}, err
	} else if ok {
		result.Bypass = &bypass
	}
	return result, nil
}

// RepairResume resumes by running a fresh sync with the recorded inputs.
func (s *Service) RepairResume(ctx context.Context, dir string) (SyncResult, error) {
	root, _, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return SyncResult{}, err
	}
	tx, ok, err := store.Current(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	if !ok {
		return SyncResult{}, fmt.Errorf("no active transaction")
	}
	if tx.MainIndexMutated {
		return SyncResult{}, fmt.Errorf("transaction %s already mutated the main index; inspect files manually", tx.ID)
	}
	plan, err := s.planner.PlanSync(ctx, reconcile.RepoRoot(root), reconcile.SyncPlanOptions{Force: true})
	if err != nil {
		return SyncResult{}, err
	}
	if !sameStrings(namespaceNames(plan.Namespaces), tx.Namespaces) ||
		!sameStrings(planFilePaths(plan), tx.Targets) {
		return SyncResult{}, fmt.Errorf(
			"config no longer matches recorded transaction %s; run `skeeper repair abort` and sync again",
			tx.ID,
		)
	}
	if err := store.Complete(ctx, tx.ID); err != nil {
		return SyncResult{}, err
	}
	return s.Sync(ctx, root, SyncOptions{Force: true})
}

// RepairAbort aborts a transaction before main-index mutation.
func (s *Service) RepairAbort(ctx context.Context, dir string) error {
	_, _, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return err
	}
	tx, ok, err := store.Current(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return store.Abort(ctx, tx.ID)
}

// RecordBypass records an audited pre-commit bypass.
func (s *Service) RecordBypass(ctx context.Context, dir, reason string) error {
	root, cfg, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return err
	}
	sha, err := s.git.HeadSHA(ctx, root)
	if err != nil {
		sha = statusUnknown
	}
	return store.RecordBypass(ctx, state.Bypass{
		Time:    time.Now().UTC(),
		Env:     cfg.Settings.Hooks.AllowSkipEnv,
		MainSHA: sha,
		Reason:  reason,
	})
}

func mergeDriverOutputPath(root, currentPath string) (string, error) {
	path := filepath.Clean(currentPath)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve merge-driver output path: %w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("merge-driver output path %s is outside repository root", currentPath)
	}
	return path, nil
}

func (s *Service) mergeDriverSeedLock(root string, opts MergeDriverOptions) (lockfile.Lock, error) {
	if opts.CurrentPath == "" {
		return loadLockFromPath(root, lockfile.Filename)
	}
	lock, err := loadLockFromPath(root, opts.CurrentPath)
	if err != nil {
		return lockfile.Lock{}, fmt.Errorf("read merge-driver current lock %s: %w", opts.CurrentPath, err)
	}
	return lock, nil
}

func (s *Service) mergeDriverLock(
	root string,
	cfg config.Config,
	plan reconcile.Plan,
	seed lockfile.Lock,
) (lockfile.Lock, []MergeDriverNamespace, error) {
	records := make([]lockfile.NamespaceRecord, 0, len(plan.Namespaces))
	namespaces := make([]MergeDriverNamespace, 0, len(plan.Namespaces))
	for _, namespace := range plan.Namespaces {
		seedRecord, ok := lockRecord(seed, string(namespace.Name))
		digest, err := lockfile.DigestWorkingTree(root, filePlanPaths(namespace.Files), namespace.StagedContent)
		if err != nil {
			return lockfile.Lock{}, nil, err
		}
		commit := pendingCommit
		if ok && seedRecord.Digest == digest.Digest &&
			seedRecord.Files == digest.Files &&
			seedRecord.Bytes == digest.Bytes {
			commit = seedRecord.Commit
		}
		records = append(records, lockfile.NamespaceRecord{
			Name:          string(namespace.Name),
			SidecarBranch: namespace.Branch,
			Commit:        commit,
			Digest:        digest.Digest,
			Files:         digest.Files,
			Bytes:         digest.Bytes,
		})
		namespaces = append(namespaces, MergeDriverNamespace{
			Name:   string(namespace.Name),
			Branch: namespace.Branch,
			Digest: digest.Digest,
			Files:  digest.Files,
			Bytes:  digest.Bytes,
		})
	}
	return lockfile.Lock{
		Version:      lockfile.Version,
		Sidecar:      lockfile.CanonicalSidecarURL(cfg.Sidecar),
		SourceBranch: plan.SourceBranch,
		Namespaces:   records,
	}, namespaces, nil
}

func loadLockFromPath(root, path string) (lockfile.Lock, error) {
	resolved := filepath.Clean(path)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(root, filepath.FromSlash(resolved))
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return lockfile.Lock{}, err
	}
	var lock lockfile.Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		return lockfile.Lock{}, err
	}
	if err := lockfile.Validate(lock); err != nil {
		return lockfile.Lock{}, err
	}
	return lockfile.Normalize(lock), nil
}

func (s *Service) applySyncPlan(ctx context.Context, plan reconcile.Plan) (SyncResult, error) {
	root := string(plan.Root)
	if err := s.ensureClone(ctx, root, plan.Config.Sidecar); err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{}
	for _, namespace := range plan.Namespaces {
		nsResult, err := s.applyNamespacePlan(ctx, plan, namespace)
		if err != nil {
			return SyncResult{}, err
		}
		result.ChangedFiles += nsResult.ChangedFiles
		if nsResult.Committed {
			result.Committed = true
			if result.Commit == "" {
				result.Commit = nsResult.Commit
			}
		}
		result.Namespaces = append(result.Namespaces, nsResult)
	}
	return result, nil
}

func (s *Service) applyNamespacePlan(
	ctx context.Context,
	plan reconcile.Plan,
	namespace reconcile.NamespacePlan,
) (NamespaceSyncResult, error) {
	root := string(plan.Root)
	sidecarDir := sidecarPath(root)
	if err := s.ensureBranch(ctx, sidecarDir, namespace.Branch); err != nil {
		return NamespaceSyncResult{}, fmt.Errorf("sync namespace %q: %w", namespace.Name, err)
	}
	sidecarFiles, err := findMatchedFiles(
		ctx,
		sidecarStoragePath(root, string(namespace.Name)),
		namespace.Namespace.Patterns,
	)
	if err != nil {
		return NamespaceSyncResult{}, err
	}
	if err := mirrorFiles(root, sidecarStoragePath(root, string(namespace.Name)), namespace, sidecarFiles); err != nil {
		return NamespaceSyncResult{}, fmt.Errorf("mirror namespace %q: %w", namespace.Name, err)
	}
	preCommitDigest, err := lockfile.DigestWorkingTree(root, filePlanPaths(namespace.Files), namespace.StagedContent)
	if err != nil {
		return NamespaceSyncResult{}, err
	}
	committed, err := s.commitAndPushNamespace(ctx, sidecarDir, plan.SourceBranch, namespace, preCommitDigest.Digest)
	if err != nil {
		return NamespaceSyncResult{}, err
	}
	commit, err := s.revParse(ctx, sidecarDir, "HEAD")
	if err != nil {
		return NamespaceSyncResult{}, err
	}
	digest, err := s.locks.DigestResult(ctx, sidecarDir, namespace.Namespace, reconcile.SidecarRef("HEAD"))
	if err != nil {
		return NamespaceSyncResult{}, err
	}
	if digest != preCommitDigest {
		return NamespaceSyncResult{}, fmt.Errorf(
			"digest.parity_mismatch: namespace %s staged digest %s (%d files, %d bytes) "+
				"does not match sidecar digest %s (%d files, %d bytes)",
			namespace.Name,
			preCommitDigest.Digest,
			preCommitDigest.Files,
			preCommitDigest.Bytes,
			digest.Digest,
			digest.Files,
			digest.Bytes,
		)
	}
	return NamespaceSyncResult{
		Name:         string(namespace.Name),
		Branch:       namespace.Branch,
		ChangedFiles: len(namespace.Files),
		Committed:    committed,
		Commit:       commit,
		Digest:       digest.Digest,
		Files:        digest.Files,
		Bytes:        digest.Bytes,
	}, nil
}

func (s *Service) mutateCoverage(
	ctx context.Context,
	dir string,
	targets []string,
	opts MutateOptions,
	adopt bool,
) (MutateResult, error) {
	root, cfg, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return MutateResult{}, err
	}
	var plan reconcile.Plan
	if adopt {
		plan, err = s.planner.PlanAdopt(
			ctx,
			reconcile.RepoRoot(root),
			targets,
			reconcile.AdoptPlanOptions{Force: opts.Force},
		)
	} else {
		plan, err = s.planner.PlanUntrack(
			ctx,
			reconcile.RepoRoot(root),
			targets,
			reconcile.UntrackPlanOptions{Force: opts.Force},
		)
	}
	if err != nil {
		return MutateResult{}, err
	}
	result := MutateResult{Plan: plan, Changed: plan.Targets, DryRun: opts.DryRun}
	if opts.DryRun {
		return result, nil
	}
	if _, err := s.Sync(ctx, root, SyncOptions{Force: true}); err != nil {
		return MutateResult{}, err
	}
	tx := state.Transaction{
		Kind:       string(plan.Kind),
		Root:       root,
		Targets:    targetPaths(plan.Targets),
		Namespaces: targetNamespaces(plan.Targets),
	}
	if err := store.Begin(ctx, tx); err != nil {
		return MutateResult{}, err
	}
	current, _, err := store.Current(ctx)
	if err != nil {
		return MutateResult{}, err
	}
	for _, target := range plan.Targets {
		if !target.Tracked {
			continue
		}
		if _, err := s.runner.Run(ctx, root, "git", "rm", "--cached", "--", target.Path); err != nil {
			return MutateResult{}, fmt.Errorf("remove %s from main index after sidecar push: %w", target.Path, err)
		}
	}
	if err := store.MarkPhase(ctx, current.ID, state.TransactionPhaseMainIndexMutated); err != nil {
		return MutateResult{}, err
	}
	if err := UpdateGitignore(root, cfg.Namespaces); err != nil {
		return MutateResult{}, err
	}
	if _, err := s.runner.Run(ctx, root, "git", "add", ".gitignore", lockfile.Filename); err != nil {
		return MutateResult{}, fmt.Errorf("stage coverage changes: %w", err)
	}
	if err := store.MarkPhase(ctx, current.ID, state.TransactionPhaseLockStaged); err != nil {
		return MutateResult{}, err
	}
	if opts.Commit {
		if _, err := s.commitMain(ctx, root, opts.Message); err != nil {
			return MutateResult{}, err
		}
	}
	if err := store.Complete(ctx, current.ID); err != nil {
		return MutateResult{}, err
	}
	return result, nil
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
	installed, err := s.hooks.Install(ctx, reconcile.RepoRoot(root), hooks.InstallOptions{Config: cfg})
	if err != nil {
		return InitResult{}, err
	}
	return InitResult{
		Root:      root,
		Sidecar:   sidecarPath(root),
		Config:    cfg,
		HookPath:  installed.PreCommit,
		Gitignore: filepath.Join(root, ".gitignore"),
	}, nil
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

func (s *Service) ensureClone(ctx context.Context, root, url string) error {
	sidecarDir := sidecarPath(root)
	if exists(filepath.Join(sidecarDir, ".git")) {
		return s.ensureSidecarCommitIdentity(ctx, root, sidecarDir)
	}
	if exists(sidecarDir) {
		return fmt.Errorf("%s exists but is not a git clone", DirName)
	}
	if _, err := s.runner.Run(ctx, root, "git", "clone", url, DirName); err != nil {
		return fmt.Errorf("clone sidecar into %s: %w", DirName, err)
	}
	if err := s.ensureSidecarCommitIdentity(ctx, root, sidecarDir); err != nil {
		return err
	}
	return nil
}

func (s *Service) ensureSidecarCommitIdentity(ctx context.Context, root, sidecarDir string) error {
	for _, key := range []string{"user.name", "user.email"} {
		sidecarValue, err := s.gitConfigValue(ctx, sidecarDir, true, key)
		if err != nil {
			return fmt.Errorf("read sidecar git config %s: %w", key, err)
		}
		if sidecarValue != "" {
			continue
		}
		projectValue, err := s.gitConfigValue(ctx, root, false, key)
		if err != nil {
			return fmt.Errorf("read project git config %s: %w", key, err)
		}
		if projectValue == "" {
			continue
		}
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "config", "--local", key, projectValue); err != nil {
			return fmt.Errorf("configure sidecar git config %s: %w", key, err)
		}
	}
	return nil
}

func (s *Service) gitConfigValue(ctx context.Context, dir string, local bool, key string) (string, error) {
	args := []string{"config"}
	if local {
		args = append(args, "--local")
	}
	args = append(args, "--get", key)
	result, err := s.runner.Run(ctx, dir, "git", args...)
	if err != nil {
		var commandErr *gitexec.CommandError
		if errors.As(err, &commandErr) && commandErr.ExitCode == 1 && strings.TrimSpace(commandErr.Stderr) == "" {
			return "", nil
		}
		return "", err
	}
	return gitexec.TrimmedStdout(result), nil
}

func (s *Service) ensureBranch(ctx context.Context, sidecarDir, branch string) error {
	if dirty, err := s.dirty(ctx, sidecarDir); err != nil {
		return err
	} else if dirty {
		return fmt.Errorf(
			"%s has local changes; run `skeeper repair status` before switching sidecar branches",
			DirName,
		)
	}
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "fetch", "origin"); err != nil {
		return fmt.Errorf("fetch sidecar origin: %w", err)
	}
	switch {
	case s.git.RefExists(ctx, sidecarDir, "refs/heads/"+branch):
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "switch", branch); err != nil {
			return fmt.Errorf("switch sidecar branch %q: %w", branch, err)
		}
	case s.git.RefExists(ctx, sidecarDir, "refs/remotes/origin/"+branch):
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "switch", "--track", "origin/"+branch); err != nil {
			if _, switchErr := s.runner.Run(ctx, sidecarDir, "git", "switch", branch); switchErr != nil {
				return fmt.Errorf("track sidecar branch %q: %w", branch, err)
			}
		}
	default:
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
	if s.git.RefExists(ctx, sidecarDir, "refs/remotes/origin/"+branch) {
		if _, err := s.runner.Run(ctx, sidecarDir, "git", "rebase", "origin/"+branch); err != nil {
			return fmt.Errorf("rebase sidecar branch %q: %w", branch, err)
		}
	}
	return nil
}

func (s *Service) commitAndPushNamespace(
	ctx context.Context,
	sidecarDir string,
	sourceBranch string,
	namespace reconcile.NamespacePlan,
	digest lockfile.NamespaceDigest,
) (bool, error) {
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "add", "-A"); err != nil {
		return false, fmt.Errorf("stage sidecar changes: %w", err)
	}
	dirty, err := s.dirty(ctx, sidecarDir)
	if err != nil {
		return false, err
	}
	hasHead := s.git.HasHead(ctx, sidecarDir)
	committed := false
	if dirty || !hasHead {
		args := []string{
			"commit",
			"-m",
			fmt.Sprintf("sync namespace %s", namespace.Name),
			"-m",
			fmt.Sprintf("Source-Branch: %s\nNamespace-Digest: %s", sourceBranch, digest),
		}
		if !hasHead && !dirty {
			args = append(args, "--allow-empty")
		}
		if _, err := s.runner.Run(ctx, sidecarDir, "git", args...); err != nil {
			return false, fmt.Errorf("commit sidecar changes for namespace %q: %w", namespace.Name, err)
		}
		committed = true
	}
	if err := s.pushBranch(ctx, sidecarDir, namespace.Branch); err != nil {
		return false, err
	}
	return committed, nil
}

func (s *Service) pushBranch(ctx context.Context, sidecarDir, branch string) error {
	if s.sidecarBranchPushed(ctx, sidecarDir, branch) {
		return nil
	}
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "push", "-u", "origin", branch); err != nil {
		if _, fetchErr := s.runner.Run(ctx, sidecarDir, "git", "fetch", "origin"); fetchErr != nil {
			return fmt.Errorf("push sidecar branch %q: %w", branch, err)
		}
		if s.git.RefExists(ctx, sidecarDir, "refs/remotes/origin/"+branch) {
			if _, rebaseErr := s.runner.Run(ctx, sidecarDir, "git", "rebase", "origin/"+branch); rebaseErr != nil {
				return fmt.Errorf("rebase after push rejection for %q: %w", branch, rebaseErr)
			}
		}
		if _, retryErr := s.runner.Run(ctx, sidecarDir, "git", "push", "-u", "origin", branch); retryErr != nil {
			return fmt.Errorf("push sidecar branch %q: %w", branch, retryErr)
		}
	}
	return nil
}

func (s *Service) sidecarBranchPushed(ctx context.Context, sidecarDir, branch string) bool {
	head, err := s.revParse(ctx, sidecarDir, "HEAD")
	if err != nil {
		return false
	}
	remote, err := s.revParse(ctx, sidecarDir, "refs/remotes/origin/"+branch)
	return err == nil && head == remote
}

func (s *Service) dirty(ctx context.Context, dir string) (bool, error) {
	out, err := s.runner.Run(ctx, dir, "git", "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("read git status: %w", err)
	}
	return strings.TrimSpace(out.Stdout) != "", nil
}

func (s *Service) remoteState(ctx context.Context, sidecarDir, branch string) string {
	dirty, err := s.dirty(ctx, sidecarDir)
	if err != nil {
		return statusUnknown
	}
	if dirty {
		return "local changes"
	}
	if !s.git.HasHead(ctx, sidecarDir) {
		return "no local commits"
	}
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "fetch", "origin"); err != nil {
		return statusUnknown
	}
	remoteRef := "refs/remotes/origin/" + branch
	if !s.git.RefExists(ctx, sidecarDir, remoteRef) {
		return "not pushed"
	}
	ahead, behind, err := s.git.AheadBehind(ctx, sidecarDir, "HEAD", remoteRef)
	if err != nil {
		return statusUnknown
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

func (s *Service) ensureCommit(ctx context.Context, sidecarDir, commit string) error {
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "cat-file", "-e", commit+"^{commit}"); err != nil {
		return fmt.Errorf("sidecar commit %s is missing; run `skeeper sync`: %w", commit, err)
	}
	return nil
}

func (s *Service) treePaths(ctx context.Context, sidecarDir, commit, namespace string) ([]string, error) {
	out, err := s.runner.Run(ctx, sidecarDir, "git", "ls-tree", "-r", "-z", "--name-only", commit, "--", namespace)
	if err != nil {
		return nil, fmt.Errorf("list sidecar paths for namespace %s: %w", namespace, err)
	}
	paths := make([]string, 0)
	for path := range strings.SplitSeq(out.Stdout, "\x00") {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path != "" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *Service) revParse(ctx context.Context, dir, revision string) (string, error) {
	out, err := s.runner.Run(ctx, dir, "git", "rev-parse", revision)
	if err != nil {
		return "", fmt.Errorf("resolve git revision %s: %w", revision, err)
	}
	return strings.TrimSpace(out.Stdout), nil
}

func (s *Service) commitMain(ctx context.Context, root, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("--message is required with --commit")
	}
	if _, err := s.runner.Run(ctx, root, "git", "commit", "-m", message); err != nil {
		return "", fmt.Errorf("commit main repository changes: %w", err)
	}
	return s.revParse(ctx, root, "HEAD")
}

func mirrorFiles(root, sidecarDir string, namespace reconcile.NamespacePlan, sidecarFiles []string) error {
	mainSet := make(map[string]struct{}, len(namespace.Files))
	for _, file := range namespace.Files {
		mainSet[file.Path] = struct{}{}
		dst := filepath.Join(sidecarDir, filepath.FromSlash(file.Path))
		if content, ok := namespace.StagedContent[file.Path]; ok {
			if err := writeStringFile(dst, content, 0o644); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(filepath.Join(root, filepath.FromSlash(file.Path)), dst); err != nil {
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

func writeStringFile(path, content string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent for %s: %w", path, err)
	}
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return file.Close()
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
		return config.Namespace{}, fmt.Errorf("%s matches multiple skeeper namespaces including %s", rel, owner.Name)
	}
}

func inferNamespace(cfg config.Config, name string) (config.Namespace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if len(cfg.Namespaces) != 1 {
			return config.Namespace{}, fmt.Errorf("--namespace is required when multiple namespaces are configured")
		}
		return cfg.Namespaces[0], nil
	}
	for _, namespace := range cfg.Namespaces {
		if namespace.Name == name {
			return namespace, nil
		}
	}
	return config.Namespace{}, fmt.Errorf("namespace %s is not configured", name)
}

func namespaceByName(cfg config.Config, name string) (config.Namespace, error) {
	for _, namespace := range cfg.Namespaces {
		if namespace.Name == name {
			return namespace, nil
		}
	}
	return config.Namespace{}, fmt.Errorf("namespace %s is missing from %s", name, config.Filename)
}

func validateNoOverlap(ctx context.Context, root string, cfg config.Config) error {
	owners := map[string]string{}
	for _, namespace := range cfg.Namespaces {
		files, err := matcher.FindContextWithOptions(
			ctx,
			root,
			namespace.Patterns,
			matcher.Options{RespectGitignore: namespace.RespectsGitignore()},
		)
		if err != nil {
			return err
		}
		for _, file := range files {
			if !namespace.Owns(file) {
				continue
			}
			if previous, ok := owners[file]; ok {
				return fmt.Errorf(
					"%s would be owned by both %s and %s; use exclude to make ownership unique",
					file,
					previous,
					namespace.Name,
				)
			}
			owners[file] = namespace.Name
		}
	}
	return nil
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
	return matcher.FindContextWithOptions(ctx, root, patterns, matcher.Options{RespectGitignore: false})
}

func namespaceRecords(results []NamespaceSyncResult) []lockfile.NamespaceRecord {
	records := make([]lockfile.NamespaceRecord, 0, len(results))
	for _, result := range results {
		records = append(records, lockfile.NamespaceRecord{
			Name:          result.Name,
			SidecarBranch: result.Branch,
			Commit:        result.Commit,
			Digest:        result.Digest,
			Files:         result.Files,
			Bytes:         result.Bytes,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Name < records[j].Name
	})
	return records
}

func namespaceNames(namespaces []reconcile.NamespacePlan) []string {
	names := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		names = append(names, string(namespace.Name))
	}
	return names
}

func planFilePaths(plan reconcile.Plan) []string {
	paths := make([]string, 0)
	for _, namespace := range plan.Namespaces {
		paths = append(paths, filePlanPaths(namespace.Files)...)
	}
	sort.Strings(paths)
	return paths
}

func targetPaths(targets []reconcile.TargetDecision) []string {
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.Path)
	}
	return paths
}

func targetNamespaces(targets []reconcile.TargetDecision) []string {
	seen := map[string]struct{}{}
	for _, target := range targets {
		seen[target.Namespace] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for namespace := range seen {
		names = append(names, namespace)
	}
	sort.Strings(names)
	return names
}

func filePlanPaths(files []reconcile.FilePlan) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return slices.Equal(left, right)
}

func planNamespace(plan reconcile.Plan, name string) (reconcile.NamespacePlan, bool) {
	for _, namespace := range plan.Namespaces {
		if string(namespace.Name) == name {
			return namespace, true
		}
	}
	return reconcile.NamespacePlan{}, false
}

func lockRecord(lock lockfile.Lock, name string) (lockfile.NamespaceRecord, bool) {
	for _, record := range lock.Namespaces {
		if record.Name == name {
			return record, true
		}
	}
	return lockfile.NamespaceRecord{}, false
}

func lockCommitSummary(lock lockfile.Lock) string {
	if len(lock.Namespaces) == 0 {
		return ""
	}
	return shortHash(lock.Namespaces[0].Commit)
}

func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func diagnostic(code, message, namespace, recovery string) reconcile.Diagnostic {
	return reconcile.Diagnostic{
		Code:      code,
		Severity:  "error",
		Message:   message,
		Namespace: namespace,
		Recovery:  recovery,
	}
}

func appendUnique(values []string, next ...string) []string {
	for _, value := range next {
		if !slices.Contains(values, value) {
			values = append(values, value)
		}
	}
	return values
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

// PrintJSON writes a stable JSON payload for CLI renderers.
func PrintJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
