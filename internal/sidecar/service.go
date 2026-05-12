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
	Track        []string
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
	Mirror  bool
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

// PullOptions configures a Git-like sidecar pull.
type PullOptions struct {
	JSON  bool
	NoGit bool
}

// PullResult reports a completed sidecar pull.
type PullResult struct {
	OK          bool                   `json:"ok"`
	GitUpdated  bool                   `json:"git_updated"`
	LockUpdated bool                   `json:"lock_updated"`
	Hydrate     HydrateResult          `json:"hydrate"`
	Diagnostics []reconcile.Diagnostic `json:"diagnostics,omitempty"`
}

// SyncWorkflowResult reports the high-level pull + push workflow.
type SyncWorkflowResult struct {
	OK     bool       `json:"ok"`
	Pull   PullResult `json:"pull"`
	Push   SyncResult `json:"push"`
	DryRun bool       `json:"dry_run,omitempty"`
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
	OK          bool                      `json:"ok"`
	Namespaces  []reconcile.NamespaceDiff `json:"namespaces,omitempty"`
	Diagnostics []reconcile.Diagnostic    `json:"diagnostics,omitempty"`
}

// Status describes the current sidecar state.
type Status struct {
	Sidecar     string                 `json:"sidecar"`
	Branch      string                 `json:"branch"`
	OK          bool                   `json:"ok"`
	LockPresent bool                   `json:"lock_present"`
	LockCommit  string                 `json:"lock_commit,omitempty"`
	Namespaces  []NamespaceStatus      `json:"namespaces"`
	Transaction *state.Transaction     `json:"transaction,omitempty"`
	Bypass      *state.Bypass          `json:"bypass,omitempty"`
	HooksOK     bool                   `json:"hooks_ok"`
	Hooks       hooks.CheckResult      `json:"hooks"`
	NextAction  string                 `json:"next_action"`
	Diagnostics []reconcile.Diagnostic `json:"diagnostics,omitempty"`
}

// StatusOptions configures status output and check behavior.
type StatusOptions struct {
	Check bool
	Paths bool
}

// NamespaceStatus describes one namespace's sidecar state.
type NamespaceStatus struct {
	Name            string                   `json:"name"`
	Branch          string                   `json:"branch"`
	LastCommit      string                   `json:"last_commit,omitempty"`
	LastUnix        int64                    `json:"last_unix,omitempty"`
	Remote          string                   `json:"remote"`
	TrackedFiles    int                      `json:"tracked_files"`
	LockedCommit    string                   `json:"locked_commit,omitempty"`
	LockedDigest    lockfile.NamespaceDigest `json:"locked_digest,omitempty"`
	RemoteTip       string                   `json:"remote_tip,omitempty"`
	LocalBranchTip  string                   `json:"local_branch_tip,omitempty"`
	WorktreeHead    string                   `json:"sidecar_worktree_head,omitempty"`
	LockRemoteState string                   `json:"lock_remote_state,omitempty"`
	CloneState      string                   `json:"clone_state,omitempty"`
	Diff            reconcile.ClassCount     `json:"diff"`
	Paths           []reconcile.PathDiff     `json:"paths,omitempty"`
}

// RestoreOptions configures local file restoration from the locked sidecar state.
type RestoreOptions struct {
	DryRun bool
	All    bool
	Paths  []string
}

// RestoreResult reports files restored from the locked sidecar state.
type RestoreResult struct {
	OK          bool                   `json:"ok"`
	Restored    []string               `json:"restored"`
	Skipped     []string               `json:"skipped,omitempty"`
	Rescue      *state.RescueManifest  `json:"rescue,omitempty"`
	DryRun      bool                   `json:"dry_run,omitempty"`
	Diagnostics []reconcile.Diagnostic `json:"diagnostics,omitempty"`
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

// TrackOptions configures public coverage tracking.
type TrackOptions struct {
	Namespace string
	Exclude   []string
	Sync      bool
	DryRun    bool
	JSON      bool
	Force     bool
	Commit    bool
	Message   string
}

// TrackResult reports a public tracking change.
type TrackResult struct {
	ConfigPath string         `json:"config_path"`
	Gitignore  string         `json:"gitignore"`
	Plan       reconcile.Plan `json:"plan"`
	DryRun     bool           `json:"dry_run,omitempty"`
	Synced     bool           `json:"synced,omitempty"`
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

// RepairOptions configures the public repair workflow.
type RepairOptions struct {
	Check bool
	JSON  bool
}

// RepairAction describes one repair step that was taken or recommended.
type RepairAction struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// RepairResult reports health checks, safe repairs, and remaining issues.
type RepairResult struct {
	OK          bool                   `json:"ok"`
	Check       bool                   `json:"check,omitempty"`
	Status      Status                 `json:"status"`
	Verify      VerifyResult           `json:"verify"`
	FSCK        FSCKResult             `json:"fsck"`
	Hooks       hooks.CheckResult      `json:"hooks"`
	Rescues     []state.RescueManifest `json:"rescues,omitempty"`
	Actions     []RepairAction         `json:"actions,omitempty"`
	Diagnostics []reconcile.Diagnostic `json:"diagnostics,omitempty"`
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
	patterns, err := config.NormalizePatterns(append(append([]string{}, opts.Patterns...), opts.Track...))
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
func (s *Service) Hydrate(ctx context.Context, dir string, options ...HydrateOptions) (HydrateResult, error) {
	opts := HydrateOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	return s.hydrate(ctx, dir, opts)
}

// Restore restores explicit paths from the locked sidecar state.
func (s *Service) Restore(ctx context.Context, dir string, opts RestoreOptions) (RestoreResult, error) {
	if err := validateRestoreOptions(opts); err != nil {
		return RestoreResult{}, err
	}
	diff, err := s.diffProject(ctx, dir, "", true)
	if err != nil {
		return RestoreResult{}, err
	}
	result := RestoreResult{OK: true, DryRun: opts.DryRun}
	plan, err := buildRestorePlan(&diff, opts, &result)
	if err != nil {
		return RestoreResult{}, err
	}
	if !result.OK {
		return result, nil
	}
	for _, write := range plan.writes {
		result.Restored = append(result.Restored, write.path)
	}
	sort.Strings(result.Restored)
	sort.Strings(result.Skipped)
	if opts.DryRun {
		return result, nil
	}
	if err := applyRestorePlan(ctx, &diff, plan, &result); err != nil {
		return RestoreResult{}, err
	}
	if err := s.writeHydrationJournal(ctx, &diff); err != nil {
		return RestoreResult{}, err
	}
	return result, nil
}

type restorePlan struct {
	writes     []restoreWrite
	candidates []state.RescueCandidate
}

type restoreWrite struct {
	path    string
	content string
}

func validateRestoreOptions(opts RestoreOptions) error {
	if opts.All && len(opts.Paths) > 0 {
		return fmt.Errorf("--all cannot be combined with explicit paths")
	}
	if !opts.All && len(opts.Paths) == 0 {
		return fmt.Errorf("restore requires at least one path or --all")
	}
	return nil
}

func buildRestorePlan(diff *projectDiff, opts RestoreOptions, result *RestoreResult) (restorePlan, error) {
	targets, err := restoreTargets(diff, opts)
	if err != nil {
		return restorePlan{}, err
	}
	plan := restorePlan{writes: make([]restoreWrite, 0, len(targets))}
	for _, rel := range targets {
		appendRestoreTarget(diff, rel, result, &plan)
	}
	return plan, nil
}

func appendRestoreTarget(diff *projectDiff, rel string, result *RestoreResult, plan *restorePlan) {
	pathDiff, namespace, ok := findPathDiff(diff.summary, rel)
	if !ok {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, diagnostic(
			"restore.path_unmanaged",
			fmt.Sprintf("%s is not tracked in the locked sidecar state", rel),
			"",
			"skeeper status --paths",
		))
		return
	}
	locked, hasLocked := diff.locked[namespace][rel]
	if !hasLocked {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, diagnostic(
			"restore.path_not_locked",
			fmt.Sprintf("%s has no locked sidecar content to restore", rel),
			namespace,
			"skeeper sync",
		))
		return
	}
	if pathDiff.Class == reconcile.PathUnchanged {
		result.Skipped = append(result.Skipped, rel)
		return
	}
	if pathDiff.Local != nil {
		plan.candidates = append(plan.candidates, state.RescueCandidate{Path: rel, Class: string(pathDiff.Class)})
	}
	plan.writes = append(plan.writes, restoreWrite{path: rel, content: locked.Content})
}

func applyRestorePlan(ctx context.Context, diff *projectDiff, plan restorePlan, result *RestoreResult) error {
	if len(plan.candidates) > 0 {
		rescue, err := diff.store.CreateRescue(ctx, diff.root, "restore", plan.candidates)
		if err != nil {
			return err
		}
		if len(rescue.Files) > 0 {
			result.Rescue = &rescue
		}
	}
	for _, write := range plan.writes {
		if err := writeStringFile(filepath.Join(diff.root, filepath.FromSlash(write.path)), write.content); err != nil {
			return err
		}
	}
	return nil
}

// HydrateResult reports files restored by hydrate.
type HydrateResult struct {
	OK             bool                   `json:"ok"`
	Restored       []string               `json:"restored"`
	Skipped        []string               `json:"skipped,omitempty"`
	Rescue         *state.RescueManifest  `json:"rescue,omitempty"`
	Commit         string                 `json:"commit"`
	DryRun         bool                   `json:"dry_run,omitempty"`
	HooksInstalled bool                   `json:"hooks_installed"`
	Plan           reconcile.DiffSummary  `json:"plan"`
	FSCKAfter      *FSCKResult            `json:"fsck_after,omitempty"`
	Diagnostics    []reconcile.Diagnostic `json:"diagnostics,omitempty"`
}

// Sync mirrors main-tree spec files into the sidecar, pushes, writes, and stages skeeper.lock.
func (s *Service) Sync(ctx context.Context, dir string, opts SyncOptions) (SyncResult, error) {
	return s.Push(ctx, dir, opts)
}

// Pull fetches sidecar refs, materializes remote-only managed files, and preserves local drift.
func (s *Service) Pull(ctx context.Context, dir string, opts PullOptions) (PullResult, error) {
	root, cfg, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return PullResult{}, err
	}
	result := PullResult{OK: true}
	if !opts.NoGit {
		updated, err := s.fastForwardMain(ctx, root)
		if err != nil {
			return PullResult{}, err
		}
		result.GitUpdated = updated
	}
	lock, err := s.locks.Load(reconcile.RepoRoot(root))
	if err != nil {
		return PullResult{}, err
	}
	targetLock, changed, err := s.remoteTipLock(ctx, root, cfg, lock)
	if err != nil {
		return PullResult{}, err
	}
	hydrate, err := s.hydrateWithLock(ctx, root, HydrateOptions{KeepLocal: true}, &targetLock)
	if err != nil {
		return PullResult{}, err
	}
	result.Hydrate = hydrate
	result.LockUpdated = changed
	result.OK = hydrate.OK || pullHasOnlyLocalDrift(pullFinalSummary(hydrate))
	if !result.OK {
		result.Diagnostics = append(result.Diagnostics, hydrate.Diagnostics...)
	}
	return result, nil
}

// SyncWorkflow runs the Git-like default workflow: pull remote specs, then push local specs.
func (s *Service) SyncWorkflow(ctx context.Context, dir string, opts SyncOptions) (SyncWorkflowResult, error) {
	if opts.DryRun {
		push, err := s.Push(ctx, dir, opts)
		if err != nil {
			return SyncWorkflowResult{}, err
		}
		return SyncWorkflowResult{OK: true, Push: push, DryRun: true}, nil
	}
	pull, err := s.Pull(ctx, dir, PullOptions{NoGit: true})
	if err != nil {
		return SyncWorkflowResult{}, err
	}
	result := SyncWorkflowResult{OK: pull.OK, Pull: pull}
	if !pull.OK {
		return result, nil
	}
	push, err := s.Push(ctx, dir, opts)
	if err != nil {
		return SyncWorkflowResult{}, err
	}
	result.Push = push
	result.OK = true
	return result, nil
}

func (s *Service) remoteTipLock(
	ctx context.Context,
	root string,
	cfg config.Config,
	lock lockfile.Lock,
) (lockfile.Lock, bool, error) {
	if err := s.ensureClone(ctx, root, cfg.Sidecar); err != nil {
		return lockfile.Lock{}, false, err
	}
	sidecarDir := sidecarPath(root)
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "fetch", "origin"); err != nil {
		return lockfile.Lock{}, false, fmt.Errorf("fetch sidecar origin: %w", err)
	}
	next := lockfile.Normalize(lock)
	changed := false
	for i, record := range next.Namespaces {
		remoteRef := "refs/remotes/origin/" + record.SidecarBranch
		if !s.git.RefExists(ctx, sidecarDir, remoteRef) {
			continue
		}
		remoteTip, err := s.revParse(ctx, sidecarDir, remoteRef)
		if err != nil {
			return lockfile.Lock{}, false, err
		}
		if remoteTip == record.Commit {
			continue
		}
		namespace, err := namespaceByName(cfg, record.Name)
		if err != nil {
			return lockfile.Lock{}, false, err
		}
		digest, err := s.locks.DigestResult(ctx, sidecarDir, namespace, reconcile.SidecarRef(remoteRef))
		if err != nil {
			return lockfile.Lock{}, false, err
		}
		next.Namespaces[i].Commit = remoteTip
		next.Namespaces[i].Digest = digest.Digest
		next.Namespaces[i].Files = digest.Files
		next.Namespaces[i].Bytes = digest.Bytes
		changed = true
	}
	return next, changed, nil
}

func pullHasOnlyLocalDrift(summary reconcile.DiffSummary) bool {
	for _, namespace := range summary.Namespaces {
		counts := namespace.Counts
		if counts.MissingLocal != 0 ||
			counts.SidecarModified != 0 ||
			counts.BothModifiedConflict != 0 ||
			counts.NamespaceRemoved != 0 ||
			counts.ConfigUnowned != 0 {
			return false
		}
	}
	return true
}

func pullFinalSummary(hydrate HydrateResult) reconcile.DiffSummary {
	if hydrate.FSCKAfter == nil {
		return hydrate.Plan
	}
	return reconcile.SummarizeDiff(hydrate.FSCKAfter.Namespaces)
}

// Push mirrors local managed files into the sidecar without pruning remote-only files by default.
func (s *Service) Push(ctx context.Context, dir string, opts SyncOptions) (SyncResult, error) {
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
	result, syncErr := s.applySyncPlan(ctx, plan, opts.Mirror)
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
	if err := s.writeHydrationJournalForLock(ctx, root, store, lock); err != nil {
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
	diff, err := s.diffProject(ctx, dir, opts.SourceBranch, false)
	if err != nil {
		return FSCKResult{}, err
	}
	result := FSCKResult{OK: diff.summary.OK, Namespaces: diff.summary.Namespaces}
	if bypass, ok, err := diff.store.Bypass(ctx); err != nil {
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
	for _, namespace := range diff.summary.Namespaces {
		if namespace.Counts.MissingLocal != 0 ||
			namespace.Counts.LocalOnly != 0 ||
			namespace.Counts.LocalModified != 0 ||
			namespace.Counts.SidecarModified != 0 ||
			namespace.Counts.BothModifiedConflict != 0 ||
			namespace.Counts.NamespaceRemoved != 0 ||
			namespace.Counts.ConfigUnowned != 0 {
			result.Diagnostics = append(result.Diagnostics, diagnostic(
				"fsck.working_tree_drift",
				fmt.Sprintf("namespace %s working tree differs from locked sidecar content", namespace.Name),
				namespace.Name,
				"skeeper status --paths",
			))
		}
	}
	return result, nil
}

// Status returns a remote-aware summary suitable for CLI display and CI checks.
func (s *Service) Status(ctx context.Context, dir string, options ...StatusOptions) (Status, error) {
	opts := StatusOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
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
	status := Status{Sidecar: cfg.Sidecar, Branch: branch, OK: true, HooksOK: true}
	if err := s.loadStatusState(ctx, store, &status); err != nil {
		return Status{}, err
	}
	lock, lockErr := s.locks.Load(reconcile.RepoRoot(root))
	if lockErr == nil {
		status.LockPresent = true
		status.LockCommit = lockCommitSummary(lock)
	} else {
		status.OK = false
		status.Diagnostics = append(status.Diagnostics, diagnostic(
			"lock.missing",
			fmt.Sprintf("%s is missing or invalid; run `skeeper sync`", lockfile.Filename),
			"",
			"skeeper sync",
		))
	}
	diff := s.statusDiff(ctx, root, status.LockPresent)
	if !diff.OK && status.LockPresent {
		status.OK = false
		status.Diagnostics = append(status.Diagnostics, diagnostic(
			"status.working_tree_drift",
			"working-tree specs differ from the locked sidecar state",
			"",
			"skeeper sync",
		))
	}
	for _, namespace := range plan.Namespaces {
		status.Namespaces = append(
			status.Namespaces,
			s.namespaceStatus(ctx, root, namespace, lock, status.LockPresent, diff, opts),
		)
	}
	hookCheck, err := s.hooks.Check(ctx, reconcile.RepoRoot(root))
	if err != nil {
		status.OK = false
		status.HooksOK = false
		status.Diagnostics = append(status.Diagnostics, diagnostic(
			"hooks.check_failed",
			err.Error(),
			"",
			"skeeper repair",
		))
	} else {
		status.Hooks = hookCheck
		status.HooksOK = hookCheck.OK
		if !hookCheck.OK {
			status.OK = false
			for _, message := range hookCheck.Diagnostics {
				status.Diagnostics = append(status.Diagnostics, diagnostic(
					"hooks.unhealthy",
					message,
					"",
					"skeeper repair",
				))
			}
		}
	}
	status.NextAction = statusNextAction(status)
	return status, nil
}

func (s *Service) loadStatusState(ctx context.Context, store *state.Store, status *Status) error {
	if tx, ok, err := store.Current(ctx); err != nil {
		return err
	} else if ok {
		status.Transaction = &tx
		status.OK = false
		status.Diagnostics = append(
			status.Diagnostics,
			diagnostic("repair.transaction_active", "repair transaction is active", "", "skeeper repair"),
		)
	}
	if bypass, ok, err := store.Bypass(ctx); err != nil {
		return err
	} else if ok {
		status.Bypass = &bypass
		status.OK = false
		status.Diagnostics = append(
			status.Diagnostics,
			diagnostic("bypass.active", "strict hook bypass is active; run `skeeper sync`", "", "skeeper sync"),
		)
	}
	return nil
}

func (s *Service) statusDiff(ctx context.Context, root string, lockPresent bool) reconcile.DiffSummary {
	if !lockPresent {
		return reconcile.DiffSummary{}
	}
	diff, err := s.diffProject(ctx, root, "", true)
	if err != nil {
		return reconcile.DiffSummary{}
	}
	return diff.summary
}

func (s *Service) namespaceStatus(
	ctx context.Context,
	root string,
	namespace reconcile.NamespacePlan,
	lock lockfile.Lock,
	lockPresent bool,
	diff reconcile.DiffSummary,
	opts StatusOptions,
) NamespaceStatus {
	status := NamespaceStatus{
		Name:         string(namespace.Name),
		Branch:       namespace.Branch,
		TrackedFiles: len(namespace.Files),
		Remote:       "not checked",
		Diff:         namespaceDiffCounts(diff, string(namespace.Name)),
	}
	if opts.Paths {
		status.Paths = namespaceDiffPaths(diff, string(namespace.Name))
	}
	if lockPresent {
		s.applyLockedStatus(ctx, root, lock, &status)
	}
	if exists(filepath.Join(root, DirName, ".git")) {
		s.applyCloneStatus(ctx, root, namespace.Branch, &status)
	}
	return status
}

func (s *Service) applyLockedStatus(
	ctx context.Context,
	root string,
	lock lockfile.Lock,
	status *NamespaceStatus,
) {
	record, ok := lockRecord(lock, status.Name)
	if !ok {
		return
	}
	status.LockedCommit = shortHash(record.Commit)
	status.LockedDigest = record.Digest
	status.LastCommit = shortHash(record.Commit)
	remoteTip, remoteState := s.lockRemoteState(ctx, sidecarPath(root), record)
	if remoteTip == "" && remoteState == "" {
		return
	}
	status.RemoteTip = shortHash(remoteTip)
	status.LockRemoteState = remoteState
	status.Remote = remoteState
}

func (s *Service) applyCloneStatus(
	ctx context.Context,
	root string,
	branch string,
	status *NamespaceStatus,
) {
	sidecarRoot := sidecarPath(root)
	status.CloneState = s.remoteState(ctx, sidecarRoot, branch)
	if status.Remote == "not checked" {
		status.Remote = status.CloneState
	}
	if tip, err := s.revParse(ctx, sidecarRoot, "refs/heads/"+branch); err == nil {
		status.LocalBranchTip = shortHash(tip)
	}
	if head, err := s.revParse(ctx, sidecarRoot, "HEAD"); err == nil {
		status.WorktreeHead = shortHash(head)
	}
	if info, err := s.git.LastCommit(ctx, sidecarRoot); err == nil {
		status.LastUnix = info.Unix
	}
}

func namespaceDiffCounts(summary reconcile.DiffSummary, name string) reconcile.ClassCount {
	for _, namespace := range summary.Namespaces {
		if namespace.Name == name {
			return namespace.Counts
		}
	}
	return reconcile.ClassCount{}
}

func namespaceDiffPaths(summary reconcile.DiffSummary, name string) []reconcile.PathDiff {
	for _, namespace := range summary.Namespaces {
		if namespace.Name == name {
			return append([]reconcile.PathDiff(nil), namespace.Paths...)
		}
	}
	return nil
}

func statusNextAction(status Status) string {
	if status.Transaction != nil {
		return "Run `skeeper repair` to finish local recovery."
	}
	if status.Bypass != nil {
		return "Run `skeeper sync` to clear the strict hook bypass."
	}
	if !status.LockPresent {
		return "Run `skeeper sync` to create the sidecar lock."
	}
	for _, namespace := range status.Namespaces {
		counts := namespace.Diff
		if counts.BothModifiedConflict != 0 || counts.NamespaceRemoved != 0 || counts.ConfigUnowned != 0 {
			return "Run `skeeper repair` to inspect unresolved managed-file drift."
		}
		if counts.MissingLocal != 0 || counts.SidecarModified != 0 {
			return "Run `skeeper pull` to apply remote sidecar docs."
		}
		if counts.LocalOnly != 0 || counts.LocalModified != 0 {
			return "Run `skeeper sync` to publish local docs and converge with the sidecar."
		}
	}
	if !status.HooksOK {
		return "Run `skeeper repair` to refresh Git integration."
	}
	if !status.OK {
		return "Run `skeeper repair` to inspect Skeeper health."
	}
	return "Working tree is clean and up to date."
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

// Track adds a public managed glob and optionally syncs matching existing files.
func (s *Service) Track(ctx context.Context, dir, glob string, opts TrackOptions) (TrackResult, error) {
	result, err := s.PatternAdd(ctx, dir, glob, PatternAddOptions{
		Namespace:     opts.Namespace,
		Exclude:       opts.Exclude,
		AdoptExisting: opts.Sync,
		DryRun:        opts.DryRun,
		JSON:          opts.JSON,
		Force:         opts.Force,
		Commit:        opts.Commit,
		Message:       opts.Message,
	})
	if err != nil {
		return TrackResult{}, err
	}
	return TrackResult{
		ConfigPath: result.ConfigPath,
		Gitignore:  result.Gitignore,
		Plan:       result.Plan,
		DryRun:     result.DryRun,
		Synced:     opts.Sync && !opts.DryRun,
	}, nil
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
	if err := writeStringFile(outputPath, string(data)); err != nil {
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
			"config no longer matches recorded transaction %s; run `skeeper repair` and sync again",
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

// Repair is the single public recovery entry point for local Skeeper state.
func (s *Service) Repair(ctx context.Context, dir string, opts RepairOptions) (RepairResult, error) {
	root, _, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return RepairResult{}, err
	}
	result, err := s.repairSnapshot(ctx, root, opts)
	if err != nil {
		return RepairResult{}, err
	}
	if opts.Check {
		result.Check = true
		return result, nil
	}
	changed, err := s.applyRepairActions(ctx, root, store, &result)
	if err != nil {
		return RepairResult{}, err
	}
	if !changed {
		return result, nil
	}
	actions := append([]RepairAction(nil), result.Actions...)
	result, err = s.repairSnapshot(ctx, root, opts)
	if err != nil {
		return RepairResult{}, err
	}
	result.Actions = append(actions, result.Actions...)
	return result, nil
}

func (s *Service) applyRepairActions(
	ctx context.Context,
	root string,
	store *state.Store,
	result *RepairResult,
) (bool, error) {
	changed := false
	if result.Status.Transaction != nil && !result.Status.Transaction.MainIndexMutated {
		if _, err := s.RepairResume(ctx, root); err != nil {
			recordRepairFailure(result, "repair.resume_failed", err, "skeeper repair --check")
			return false, nil
		}
		result.Actions = append(result.Actions, RepairAction{
			Kind:    "transaction_resumed",
			Message: "resumed the active transaction with a fresh sync",
		})
		changed = true
	}
	if result.Status.Bypass != nil {
		if _, err := s.SyncWorkflow(ctx, root, SyncOptions{Force: true}); err != nil {
			recordRepairFailure(result, "repair.bypass_sync_failed", err, "skeeper sync")
			return false, nil
		}
		result.Actions = append(result.Actions, RepairAction{
			Kind:    "bypass_synced",
			Message: "ran sync to repair a strict-hook bypass",
		})
		changed = true
	}
	if !result.Hooks.OK {
		if _, err := s.HooksInstall(ctx, root); err != nil {
			recordRepairFailure(result, "repair.hooks_install_failed", err, "skeeper init")
			return false, nil
		}
		result.Actions = append(result.Actions, RepairAction{
			Kind:    "hooks_refreshed",
			Message: "refreshed managed Git hooks and merge-driver configuration",
		})
		changed = true
	}
	if result.Status.Bypass != nil && result.Verify.OK && result.FSCK.OK {
		if err := store.ClearBypass(ctx); err != nil {
			return false, err
		}
		result.Actions = append(result.Actions, RepairAction{
			Kind:    "bypass_cleared",
			Message: "cleared stale strict-hook bypass state after successful validation",
		})
		changed = true
	}
	return changed, nil
}

func recordRepairFailure(result *RepairResult, code string, err error, recovery string) {
	result.OK = false
	result.Diagnostics = append(result.Diagnostics, diagnostic(code, err.Error(), "", recovery))
}

func (s *Service) repairSnapshot(ctx context.Context, root string, opts RepairOptions) (RepairResult, error) {
	status, err := s.Status(ctx, root, StatusOptions{Check: opts.Check, Paths: true})
	if err != nil {
		return RepairResult{}, err
	}
	result := RepairResult{OK: status.OK, Check: opts.Check, Status: status, Hooks: status.Hooks}
	result.Diagnostics = append(result.Diagnostics, status.Diagnostics...)
	if status.LockPresent {
		verify, err := s.Verify(ctx, root, VerifyOptions{})
		if err != nil {
			return RepairResult{}, err
		}
		result.Verify = verify
		if !verify.OK {
			result.OK = false
			result.Diagnostics = append(result.Diagnostics, verify.Diagnostics...)
		}
		fsck, err := s.FSCK(ctx, root, FSCKOptions{})
		if err != nil {
			return RepairResult{}, err
		}
		result.FSCK = fsck
		if !fsck.OK {
			result.OK = false
			result.Diagnostics = append(result.Diagnostics, fsck.Diagnostics...)
		}
	}
	_, _, store, err := s.loadProject(ctx, root)
	if err != nil {
		return RepairResult{}, err
	}
	rescues, err := store.ListRescues(ctx)
	if err != nil {
		return RepairResult{}, err
	}
	result.Rescues = rescues
	if status.Transaction != nil || status.Bypass != nil || !status.HooksOK {
		result.OK = false
	}
	return result, nil
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

func (s *Service) applySyncPlan(ctx context.Context, plan reconcile.Plan, mirror bool) (SyncResult, error) {
	root := string(plan.Root)
	if err := s.ensureClone(ctx, root, plan.Config.Sidecar); err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{}
	for _, namespace := range plan.Namespaces {
		nsResult, err := s.applyNamespacePlan(ctx, plan, namespace, mirror)
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
	mirror bool,
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
	if err := mirrorFiles(
		root,
		sidecarStoragePath(root, string(namespace.Name)),
		namespace,
		sidecarFiles,
		mirror,
	); err != nil {
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
	if mirror && digest != preCommitDigest {
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
			"%s has local changes; run `skeeper repair --check` before switching sidecar branches",
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
		return fmt.Errorf("sidecar commit %s is missing; run `skeeper pull`: %w", commit, err)
	}
	return nil
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

func mirrorFiles(
	root string,
	sidecarDir string,
	namespace reconcile.NamespacePlan,
	sidecarFiles []string,
	mirror bool,
) error {
	mainSet := make(map[string]struct{}, len(namespace.Files))
	for _, file := range namespace.Files {
		mainSet[file.Path] = struct{}{}
		dst := filepath.Join(sidecarDir, filepath.FromSlash(file.Path))
		if content, ok := namespace.StagedContent[file.Path]; ok {
			if err := writeStringFile(dst, content); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(filepath.Join(root, filepath.FromSlash(file.Path)), dst); err != nil {
			return err
		}
	}
	if mirror {
		for _, file := range sidecarFiles {
			if _, ok := mainSet[file]; ok {
				continue
			}
			path := filepath.Join(sidecarDir, filepath.FromSlash(file))
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove sidecar file %s: %w", file, err)
			}
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

func writeStringFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent for %s: %w", path, err)
	}
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
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
	patterns, err := config.NormalizePatterns(append(append([]string{}, opts.Patterns...), opts.Track...))
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

func restoreTargets(diff *projectDiff, opts RestoreOptions) ([]string, error) {
	seen := map[string]struct{}{}
	if opts.All {
		for _, files := range diff.locked {
			for path := range files {
				seen[path] = struct{}{}
			}
		}
	} else {
		for _, raw := range opts.Paths {
			rel, err := resolveProjectPath(diff.root, raw)
			if err != nil {
				return nil, err
			}
			seen[rel] = struct{}{}
		}
	}
	targets := make([]string, 0, len(seen))
	for path := range seen {
		targets = append(targets, path)
	}
	sort.Strings(targets)
	return targets, nil
}

func findPathDiff(summary reconcile.DiffSummary, rel string) (reconcile.PathDiff, string, bool) {
	for _, namespace := range summary.Namespaces {
		for _, path := range namespace.Paths {
			if path.Path == rel {
				return path, namespace.Name, true
			}
		}
	}
	return reconcile.PathDiff{}, "", false
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
