package sidecar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/hooks"
	"github.com/compozy/skeeper/internal/lockfile"
	"github.com/compozy/skeeper/internal/reconcile"
	"github.com/compozy/skeeper/internal/state"
)

// HydrateOptions configures safe lock materialization into the working tree.
type HydrateOptions struct {
	DryRun     bool
	JSON       bool
	KeepLocal  bool
	AdoptLocal bool
	PruneLocal bool
	Merge      bool
	Ours       bool
	Theirs     bool
}

// ReconcileOptions configures explicit drift reconciliation.
type ReconcileOptions struct {
	DryRun     bool
	JSON       bool
	AdoptLocal bool
	PruneLocal bool
	Merge      bool
	Ours       bool
	Theirs     bool
}

// ReconcileResult reports a reconciliation plan or mutation.
type ReconcileResult struct {
	OK          bool                   `json:"ok"`
	DryRun      bool                   `json:"dry_run,omitempty"`
	Plan        reconcile.DiffSummary  `json:"plan"`
	Hydrate     *HydrateResult         `json:"hydrate,omitempty"`
	Sync        *SyncResult            `json:"sync,omitempty"`
	Rescue      *state.RescueManifest  `json:"rescue,omitempty"`
	Diagnostics []reconcile.Diagnostic `json:"diagnostics,omitempty"`
}

// DiffOptions configures working-tree diff output.
type DiffOptions struct {
	JSON      bool
	Namespace string
	Classes   []reconcile.PathClass
}

// RescueListResult reports known rescue manifests.
type RescueListResult struct {
	Rescues []state.RescueManifest `json:"rescues"`
}

// RescueRestoreOptions configures rescue restore.
type RescueRestoreOptions struct {
	JSON      bool
	Overwrite bool
}

// UpdateOptions configures the high-level agent update workflow.
type UpdateOptions struct {
	JSON      bool
	NoGit     bool
	Reconcile string
	Ours      bool
	Theirs    bool
}

// UpdateResult reports update workflow steps.
type UpdateResult struct {
	OK             bool          `json:"ok"`
	GitUpdated     bool          `json:"git_updated"`
	Verify         VerifyResult  `json:"verify"`
	Hydrate        HydrateResult `json:"hydrate"`
	FSCK           FSCKResult    `json:"fsck"`
	HooksInstalled bool          `json:"hooks_installed"`
	HooksOK        bool          `json:"hooks_ok"`
}

type projectDiff struct {
	root    string
	cfg     config.Config
	store   *state.Store
	lock    lockfile.Lock
	plan    reconcile.Plan
	summary reconcile.DiffSummary
	locked  map[string]map[string]reconcile.SnapshotFile
	local   map[string]map[string]reconcile.SnapshotFile
}

func (s *Service) hydrate(ctx context.Context, dir string, opts HydrateOptions) (HydrateResult, error) {
	return s.hydrateWithLock(ctx, dir, opts, nil)
}

func (s *Service) hydrateWithLock(
	ctx context.Context,
	dir string,
	opts HydrateOptions,
	targetLock *lockfile.Lock,
) (HydrateResult, error) {
	diff, err := s.diffProjectWithLock(ctx, dir, "", true, targetLock)
	if err != nil {
		return HydrateResult{}, err
	}
	result := HydrateResult{
		OK:     diff.summary.OK,
		Commit: lockCommitSummary(diff.lock),
		DryRun: opts.DryRun,
		Plan:   diff.summary,
	}
	if err := validateHydrateOptions(opts); err != nil {
		return HydrateResult{}, err
	}
	opts, err = applyHydratePolicy(diff.cfg, diff.summary, opts)
	if err != nil {
		return HydrateResult{}, err
	}
	if err := validateHydrateOptions(opts); err != nil {
		return HydrateResult{}, err
	}
	blocked := hydrateBlockedPaths(diff.summary, opts)
	if len(blocked) > 0 {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, diagnostic(
			"hydrate.blocked",
			fmt.Sprintf("hydrate blocked by %d local managed file(s)", len(blocked)),
			"",
			"skeeper status --paths",
		))
		return result, nil
	}
	if opts.DryRun {
		result.OK = true
		return result, nil
	}
	if opts.PruneLocal || opts.Theirs {
		rescue, err := s.rescueForHydrate(ctx, &diff, opts)
		if err != nil {
			return HydrateResult{}, err
		}
		if len(rescue.Files) > 0 {
			result.Rescue = &rescue
		}
	}
	restored, skipped, err := s.applyHydrateFiles(ctx, &diff, opts)
	if err != nil {
		return HydrateResult{}, err
	}
	result.Restored = restored
	result.Skipped = skipped
	installed, err := s.hooks.Install(ctx, reconcile.RepoRoot(diff.root), hooks.InstallOptions{Config: diff.cfg})
	if err != nil {
		return HydrateResult{}, err
	}
	result.HooksInstalled = installed.PreCommit != ""
	if err := s.writeHydrationJournal(ctx, &diff); err != nil {
		return HydrateResult{}, err
	}
	if targetLock != nil {
		if err := s.locks.Write(reconcile.RepoRoot(diff.root), diff.lock); err != nil {
			return HydrateResult{}, err
		}
	}
	if err := s.syncAfterHydrate(ctx, &diff, opts, &result); err != nil {
		return HydrateResult{}, err
	}
	fsck, err := s.FSCK(ctx, diff.root, FSCKOptions{})
	if err != nil {
		return HydrateResult{}, err
	}
	result.FSCKAfter = &fsck
	result.OK = fsck.OK
	return result, nil
}

func (s *Service) syncAfterHydrate(
	ctx context.Context,
	diff *projectDiff,
	opts HydrateOptions,
	result *HydrateResult,
) error {
	if !opts.AdoptLocal && !opts.Ours {
		return nil
	}
	syncResult, err := s.Sync(ctx, diff.root, SyncOptions{Force: true, ResolveOurs: opts.Ours})
	if err != nil {
		return err
	}
	result.Commit = syncResult.Commit
	return nil
}

// Reconcile reports or applies an explicit local/locked reconciliation.
func (s *Service) Reconcile(ctx context.Context, dir string, opts ReconcileOptions) (ReconcileResult, error) {
	mutate := opts.AdoptLocal || opts.PruneLocal || opts.Merge || opts.Ours || opts.Theirs
	diff, err := s.diffProject(ctx, dir, "", mutate)
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{OK: diff.summary.OK, DryRun: opts.DryRun, Plan: diff.summary}
	if !opts.AdoptLocal && !opts.PruneLocal && !opts.Merge && !opts.Ours && !opts.Theirs {
		return result, nil
	}
	if opts.DryRun {
		return result, nil
	}
	hydrateOpts := HydrateOptions{
		AdoptLocal: opts.AdoptLocal,
		PruneLocal: opts.PruneLocal,
		Merge:      opts.Merge,
		Ours:       opts.Ours,
		Theirs:     opts.Theirs,
		KeepLocal:  opts.AdoptLocal || opts.Ours,
	}
	hydrate, err := s.hydrate(ctx, dir, hydrateOpts)
	if err != nil {
		return ReconcileResult{}, err
	}
	result.Hydrate = &hydrate
	result.Rescue = hydrate.Rescue
	result.OK = hydrate.OK
	return result, nil
}

// Diff returns a rich working-tree diff against the lock.
func (s *Service) Diff(ctx context.Context, dir string, opts DiffOptions) (reconcile.DiffSummary, error) {
	diff, err := s.diffProject(ctx, dir, "", true)
	if err != nil {
		return reconcile.DiffSummary{}, err
	}
	if opts.Namespace == "" && len(opts.Classes) == 0 {
		return diff.summary, nil
	}
	filtered := make([]reconcile.NamespaceDiff, 0, len(diff.summary.Namespaces))
	for _, namespace := range diff.summary.Namespaces {
		if opts.Namespace != "" && namespace.Name != opts.Namespace {
			continue
		}
		namespace.Paths = reconcile.FilterPathDiffs(namespace.Paths, opts.Classes...)
		namespace.Counts = reconcile.ClassCount{}
		for _, path := range namespace.Paths {
			incrementNamespaceCount(&namespace.Counts, path.Class)
		}
		filtered = append(filtered, namespace)
	}
	return reconcile.SummarizeDiff(filtered), nil
}

// RescueList returns local rescue manifests.
func (s *Service) RescueList(ctx context.Context, dir string) (RescueListResult, error) {
	_, _, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return RescueListResult{}, err
	}
	rescues, err := store.ListRescues(ctx)
	if err != nil {
		return RescueListResult{}, err
	}
	return RescueListResult{Rescues: rescues}, nil
}

// RescueRestore restores files from a rescue manifest.
func (s *Service) RescueRestore(
	ctx context.Context,
	dir string,
	id string,
	paths []string,
	opts RescueRestoreOptions,
) (state.RescueManifest, error) {
	root, _, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return state.RescueManifest{}, err
	}
	return store.RestoreRescue(ctx, root, id, paths, opts.Overwrite)
}

// Update runs the high-level safe clone update workflow.
func (s *Service) Update(ctx context.Context, dir string, opts UpdateOptions) (UpdateResult, error) {
	root, _, _, err := s.loadProject(ctx, dir)
	if err != nil {
		return UpdateResult{}, err
	}
	result := UpdateResult{OK: true}
	if !opts.NoGit {
		updated, err := s.fastForwardMain(ctx, root)
		if err != nil {
			return UpdateResult{}, err
		}
		result.GitUpdated = updated
	}
	verify, err := s.Verify(ctx, root, VerifyOptions{})
	if err != nil {
		return UpdateResult{}, err
	}
	result.Verify = verify
	if !verify.OK {
		result.OK = false
		return result, nil
	}
	hydrateOpts := HydrateOptions{}
	switch opts.Reconcile {
	case "", "report":
	case "keep-local":
		hydrateOpts.KeepLocal = true
	case "adopt-local":
		hydrateOpts.KeepLocal = true
		hydrateOpts.AdoptLocal = true
	case "prune-local":
		hydrateOpts.PruneLocal = true
	case "merge":
		hydrateOpts.Merge = true
	default:
		return UpdateResult{}, fmt.Errorf("unknown reconcile mode %q", opts.Reconcile)
	}
	hydrateOpts.Ours = opts.Ours
	hydrateOpts.Theirs = opts.Theirs
	hydrate, err := s.hydrate(ctx, root, hydrateOpts)
	if err != nil {
		return UpdateResult{}, err
	}
	result.Hydrate = hydrate
	if !hydrate.OK {
		result.OK = false
		return result, nil
	}
	fsck, err := s.FSCK(ctx, root, FSCKOptions{})
	if err != nil {
		return UpdateResult{}, err
	}
	result.FSCK = fsck
	if !fsck.OK {
		result.OK = false
		return result, nil
	}
	installed, err := s.HooksInstall(ctx, root)
	if err != nil {
		return UpdateResult{}, err
	}
	result.HooksInstalled = installed.PreCommit != ""
	check, err := s.HooksCheck(ctx, root)
	if err != nil {
		return UpdateResult{}, err
	}
	result.HooksOK = check.OK
	result.OK = result.OK && check.OK
	return result, nil
}

func (s *Service) diffProject(ctx context.Context, dir string, sourceBranch string, mutate bool) (projectDiff, error) {
	return s.diffProjectWithLock(ctx, dir, sourceBranch, mutate, nil)
}

func (s *Service) diffProjectWithLock(
	ctx context.Context,
	dir string,
	sourceBranch string,
	mutate bool,
	targetLock *lockfile.Lock,
) (projectDiff, error) {
	root, cfg, store, err := s.loadProject(ctx, dir)
	if err != nil {
		return projectDiff{}, err
	}
	if err := s.prepareDiffSidecar(ctx, root, cfg, mutate); err != nil {
		return projectDiff{}, err
	}
	var lock lockfile.Lock
	if targetLock != nil {
		lock = lockfile.Normalize(*targetLock)
		if err := lockfile.Validate(lock); err != nil {
			return projectDiff{}, err
		}
	} else {
		lock, err = s.locks.Load(reconcile.RepoRoot(root))
		if err != nil {
			return projectDiff{}, err
		}
	}
	sidecarDir := sidecarPath(root)
	plan, err := s.planner.PlanFSCK(
		ctx,
		reconcile.RepoRoot(root),
		reconcile.FSCKPlanOptions{SourceBranch: sourceBranch},
	)
	if err != nil {
		return projectDiff{}, err
	}
	journal, ok, err := store.LoadHydration(ctx)
	if err != nil {
		return projectDiff{}, err
	}
	if !ok {
		journal = state.HydrationJournal{Namespaces: map[string]state.HydrationNamespace{}}
	}
	locked, local, namespaceDiffs, err := s.diffNamespaces(ctx, root, sidecarDir, lock, plan, journal)
	if err != nil {
		return projectDiff{}, err
	}
	sort.Slice(namespaceDiffs, func(i, j int) bool {
		return namespaceDiffs[i].Name < namespaceDiffs[j].Name
	})
	return projectDiff{
		root:    root,
		cfg:     cfg,
		store:   store,
		lock:    lock,
		plan:    plan,
		summary: reconcile.SummarizeDiff(namespaceDiffs),
		locked:  locked,
		local:   local,
	}, nil
}

func (s *Service) prepareDiffSidecar(ctx context.Context, root string, cfg config.Config, mutate bool) error {
	if mutate {
		if err := s.ensureClone(ctx, root, cfg.Sidecar); err != nil {
			return err
		}
		if _, err := s.runner.Run(ctx, sidecarPath(root), "git", "fetch", "origin"); err != nil {
			return fmt.Errorf("fetch sidecar origin: %w", err)
		}
		return nil
	}
	if !exists(filepath.Join(root, DirName, ".git")) {
		return fmt.Errorf("sidecar clone is missing; run `skeeper sync`")
	}
	return nil
}

func (s *Service) diffNamespaces(
	ctx context.Context,
	root string,
	sidecarDir string,
	lock lockfile.Lock,
	plan reconcile.Plan,
	journal state.HydrationJournal,
) (
	map[string]map[string]reconcile.SnapshotFile,
	map[string]map[string]reconcile.SnapshotFile,
	[]reconcile.NamespaceDiff,
	error,
) {
	locked := map[string]map[string]reconcile.SnapshotFile{}
	local := map[string]map[string]reconcile.SnapshotFile{}
	namespaceDiffs := make([]reconcile.NamespaceDiff, 0, len(lock.Namespaces))
	for _, record := range lock.Namespaces {
		if err := s.ensureCommit(ctx, sidecarDir, record.Commit); err != nil {
			return nil, nil, nil, err
		}
		lockedSnapshot, err := s.sidecarSnapshot(ctx, sidecarDir, record.Commit, record.Name)
		if err != nil {
			return nil, nil, nil, err
		}
		locked[record.Name] = lockedSnapshot
		namespacePlan, hasPlan := planNamespace(plan, record.Name)
		if hasPlan {
			localSnapshot, err := localSnapshot(root, namespacePlan)
			if err != nil {
				return nil, nil, nil, err
			}
			local[record.Name] = localSnapshot
		} else {
			local[record.Name] = map[string]reconcile.SnapshotFile{}
		}
		base := baseSnapshot(journal, lock.SourceBranch, record.Name)
		nsDiff := reconcile.BuildNamespaceDiff(reconcile.NamespaceDiffInput{
			Name:         record.Name,
			Branch:       record.SidecarBranch,
			LockedCommit: record.Commit,
			Locked:       locked[record.Name],
			Local:        local[record.Name],
			Base:         base,
		})
		if hasPlan {
			markConfigUnowned(&nsDiff, namespacePlan.Namespace)
		} else {
			markNamespaceRemoved(&nsDiff)
		}
		namespaceDiffs = append(namespaceDiffs, nsDiff)
	}
	return locked, local, namespaceDiffs, nil
}

func (s *Service) sidecarSnapshot(
	ctx context.Context,
	sidecarDir string,
	commit string,
	namespace string,
) (map[string]reconcile.SnapshotFile, error) {
	out, err := s.runner.Run(ctx, sidecarDir, "git", "ls-tree", "-r", "-z", "--long", commit, "--", namespace)
	if err != nil {
		return nil, fmt.Errorf("list sidecar tree for namespace %s: %w", namespace, err)
	}
	files := map[string]reconcile.SnapshotFile{}
	objects := make([]string, 0)
	objectPaths := map[string][]string{}
	prefix := namespace + "/"
	for raw := range strings.SplitSeq(out.Stdout, "\x00") {
		if raw == "" {
			continue
		}
		header, path, ok := strings.Cut(raw, "\t")
		if !ok {
			return nil, fmt.Errorf("parse sidecar tree record %q", raw)
		}
		fields := strings.Fields(header)
		if len(fields) < 4 || fields[1] != "blob" {
			continue
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path), prefix)
		if rel == path {
			continue
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse sidecar blob size for %s: %w", rel, err)
		}
		object := fields[2]
		files[rel] = reconcile.SnapshotFile{Path: rel, Size: size, Blob: object}
		objects = append(objects, object)
		objectPaths[object] = append(objectPaths[object], rel)
	}
	if len(objects) == 0 {
		return files, nil
	}
	contents, err := s.catFileBatch(ctx, sidecarDir, objects)
	if err != nil {
		return nil, err
	}
	for object, content := range contents {
		for _, rel := range objectPaths[object] {
			file := files[rel]
			file.Content = content
			file.Size = int64(len(content))
			file.SHA256 = sha256String(content)
			files[rel] = file
		}
	}
	return files, nil
}

func (s *Service) catFileBatch(ctx context.Context, sidecarDir string, objects []string) (map[string]string, error) {
	var input strings.Builder
	for _, object := range objects {
		input.WriteString(object)
		input.WriteByte('\n')
	}
	out, err := s.runner.RunWithInput(
		ctx,
		sidecarDir,
		input.String(),
		"git",
		"cat-file",
		"--batch",
	)
	if err != nil {
		return nil, fmt.Errorf("read sidecar blobs with git cat-file: %w", err)
	}
	result := make(map[string]string, len(objects))
	offset := 0
	for range objects {
		newline := strings.IndexByte(out.Stdout[offset:], '\n')
		if newline == -1 {
			return nil, fmt.Errorf("parse git cat-file batch header at offset %d", offset)
		}
		header := out.Stdout[offset : offset+newline]
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("parse git cat-file batch header %q", header)
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse git cat-file blob size: %w", err)
		}
		start := offset + newline + 1
		end := start + int(size)
		if end > len(out.Stdout) {
			return nil, fmt.Errorf("git cat-file blob content truncated at offset %d", start)
		}
		result[fields[0]] = out.Stdout[start:end]
		offset = end
		if offset < len(out.Stdout) && out.Stdout[offset] == '\n' {
			offset++
		}
	}
	return result, nil
}

func localSnapshot(root string, namespace reconcile.NamespacePlan) (map[string]reconcile.SnapshotFile, error) {
	files := make(map[string]reconcile.SnapshotFile, len(namespace.Files))
	for _, file := range namespace.Files {
		content, ok := namespace.StagedContent[file.Path]
		if !ok {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.Path)))
			if err != nil {
				return nil, fmt.Errorf("read %s for diff: %w", file.Path, err)
			}
			content = string(data)
		}
		files[file.Path] = reconcile.SnapshotFile{
			Path:    file.Path,
			Size:    int64(len(content)),
			SHA256:  sha256String(content),
			Content: content,
		}
	}
	return files, nil
}

func baseSnapshot(
	journal state.HydrationJournal,
	sourceBranch string,
	namespace string,
) map[string]reconcile.BaseFile {
	result := map[string]reconcile.BaseFile{}
	if journal.SourceBranch != sourceBranch {
		return result
	}
	ns, ok := journal.Namespaces[namespace]
	if !ok {
		return result
	}
	for path, file := range ns.Files {
		result[path] = reconcile.BaseFile{
			SidecarBlob: file.SidecarBlob,
			SHA256:      file.SHA256,
			Size:        file.Size,
		}
	}
	return result
}

func markConfigUnowned(diff *reconcile.NamespaceDiff, namespace config.Namespace) {
	diff.Counts = reconcile.ClassCount{}
	for i := range diff.Paths {
		if diff.Paths[i].Locked != nil && !namespace.Owns(diff.Paths[i].Path) {
			diff.Paths[i].Class = reconcile.PathConfigUnowned
		}
		incrementNamespaceCount(&diff.Counts, diff.Paths[i].Class)
	}
}

func markNamespaceRemoved(diff *reconcile.NamespaceDiff) {
	diff.Counts = reconcile.ClassCount{}
	for i := range diff.Paths {
		diff.Paths[i].Class = reconcile.PathNamespaceRemoved
		incrementNamespaceCount(&diff.Counts, diff.Paths[i].Class)
	}
}

func incrementNamespaceCount(counts *reconcile.ClassCount, class reconcile.PathClass) {
	switch class {
	case reconcile.PathUnchanged:
		counts.Unchanged++
	case reconcile.PathMissingLocal:
		counts.MissingLocal++
	case reconcile.PathLocalOnly:
		counts.LocalOnly++
	case reconcile.PathLocalDeleted:
		counts.LocalDeleted++
	case reconcile.PathRemoteDeleted:
		counts.RemoteDeleted++
	case reconcile.PathLocalModified:
		counts.LocalModified++
	case reconcile.PathSidecarModified:
		counts.SidecarModified++
	case reconcile.PathBothModifiedConflict:
		counts.BothModifiedConflict++
	case reconcile.PathLocalDeleteConflict:
		counts.LocalDeleteConflict++
	case reconcile.PathRemoteDeleteConflict:
		counts.RemoteDeleteConflict++
	case reconcile.PathNamespaceRemoved:
		counts.NamespaceRemoved++
	case reconcile.PathConfigUnowned:
		counts.ConfigUnowned++
	}
}

func validateHydrateOptions(opts HydrateOptions) error {
	rules := []hydrateOptionRule{
		{invalidOursTheirs, "--ours and --theirs are mutually exclusive"},
		{invalidPruneCombination, "--prune-local cannot be combined with other resolution flags"},
		{invalidMergeCombination, "--merge cannot be combined with keep, adopt, or prune"},
		{invalidAdoptCombination, "--adopt-local cannot be combined with merge, ours, theirs, or prune"},
		{invalidKeepCombination, "hydrate resolution flags are mutually exclusive"},
	}
	for _, rule := range rules {
		if rule.Invalid(opts) {
			return fmt.Errorf("%s", rule.message)
		}
	}
	return nil
}

type hydrateOptionRule struct {
	Invalid func(HydrateOptions) bool
	message string
}

func invalidOursTheirs(opts HydrateOptions) bool {
	return opts.Ours && opts.Theirs
}

func invalidPruneCombination(opts HydrateOptions) bool {
	return opts.PruneLocal && (opts.KeepLocal || opts.AdoptLocal || opts.Merge || opts.Ours || opts.Theirs)
}

func invalidMergeCombination(opts HydrateOptions) bool {
	return opts.Merge && (opts.KeepLocal || opts.AdoptLocal || opts.PruneLocal || opts.Ours || opts.Theirs)
}

func invalidAdoptCombination(opts HydrateOptions) bool {
	return opts.AdoptLocal && (opts.PruneLocal || opts.Merge || opts.Ours || opts.Theirs)
}

func invalidKeepCombination(opts HydrateOptions) bool {
	return opts.KeepLocal && !opts.AdoptLocal && (opts.PruneLocal || opts.Merge || opts.Ours || opts.Theirs)
}

func hydrateBlockedPaths(summary reconcile.DiffSummary, opts HydrateOptions) []reconcile.PathDiff {
	blocked := make([]reconcile.PathDiff, 0)
	for _, namespace := range summary.Namespaces {
		for _, path := range namespace.Paths {
			if hydrateCanProceed(path.Class, opts) {
				continue
			}
			blocked = append(blocked, path)
		}
	}
	return blocked
}

func hydrateCanProceed(class reconcile.PathClass, opts HydrateOptions) bool {
	switch class {
	case reconcile.PathUnchanged,
		reconcile.PathMissingLocal,
		reconcile.PathSidecarModified,
		reconcile.PathRemoteDeleted:
		return true
	case reconcile.PathLocalOnly:
		return hydrateAllowsLocalOnly(opts)
	case reconcile.PathLocalDeleted:
		return hydrateAllowsLocalDeleted(opts)
	case reconcile.PathLocalModified:
		return hydrateAllowsLocalModified(opts)
	case reconcile.PathBothModifiedConflict:
		return hydrateAllowsConflict(opts)
	case reconcile.PathLocalDeleteConflict, reconcile.PathRemoteDeleteConflict:
		return hydrateAllowsDeleteConflict(opts)
	case reconcile.PathConfigUnowned, reconcile.PathNamespaceRemoved:
		return hydrateAllowsUnowned(opts)
	default:
		return false
	}
}

func hydrateAllowsLocalOnly(opts HydrateOptions) bool {
	return opts.KeepLocal || opts.AdoptLocal || opts.PruneLocal || opts.Ours
}

func hydrateAllowsLocalDeleted(opts HydrateOptions) bool {
	return opts.KeepLocal || opts.AdoptLocal || opts.Ours || opts.Theirs
}

func hydrateAllowsLocalModified(opts HydrateOptions) bool {
	return opts.KeepLocal || opts.AdoptLocal || opts.Ours || opts.Theirs || opts.Merge
}

func hydrateAllowsConflict(opts HydrateOptions) bool {
	return opts.Ours || opts.Theirs || opts.Merge
}

func hydrateAllowsDeleteConflict(opts HydrateOptions) bool {
	return opts.Ours || opts.Theirs
}

func hydrateAllowsUnowned(opts HydrateOptions) bool {
	return opts.PruneLocal || opts.Theirs || opts.Ours
}

func (s *Service) rescueForHydrate(
	ctx context.Context,
	diff *projectDiff,
	opts HydrateOptions,
) (state.RescueManifest, error) {
	candidates := make([]state.RescueCandidate, 0)
	for _, namespace := range diff.summary.Namespaces {
		for _, path := range namespace.Paths {
			if path.Local == nil {
				continue
			}
			if opts.Theirs && path.Class != reconcile.PathUnchanged &&
				path.Class != reconcile.PathSidecarModified {
				candidates = append(candidates, state.RescueCandidate{Path: path.Path, Class: string(path.Class)})
				continue
			}
			if opts.PruneLocal {
				switch path.Class {
				case reconcile.PathLocalOnly, reconcile.PathConfigUnowned, reconcile.PathNamespaceRemoved:
					candidates = append(candidates, state.RescueCandidate{Path: path.Path, Class: string(path.Class)})
				}
			}
		}
	}
	return diff.store.CreateRescue(ctx, diff.root, "hydrate", candidates)
}

func (s *Service) applyHydrateFiles(
	ctx context.Context,
	diff *projectDiff,
	opts HydrateOptions,
) ([]string, []string, error) {
	restored := make([]string, 0)
	skipped := make([]string, 0)
	for _, namespace := range diff.summary.Namespaces {
		for _, path := range namespace.Paths {
			action, err := s.applyHydratePath(ctx, diff, opts, namespace.Name, path)
			if err != nil {
				return nil, nil, err
			}
			switch action {
			case hydratePathRestored:
				restored = append(restored, path.Path)
			case hydratePathSkipped:
				skipped = append(skipped, path.Path)
			}
		}
	}
	sort.Strings(restored)
	sort.Strings(skipped)
	return restored, skipped, nil
}

type hydratePathAction int

const (
	hydratePathSkipped hydratePathAction = iota
	hydratePathRestored
)

func (s *Service) applyHydratePath(
	ctx context.Context,
	diff *projectDiff,
	opts HydrateOptions,
	namespace string,
	path reconcile.PathDiff,
) (hydratePathAction, error) {
	locked := diff.locked[namespace]
	file, hasLocked := locked[path.Path]
	switch path.Class {
	case reconcile.PathUnchanged:
		return hydratePathSkipped, nil
	case reconcile.PathMissingLocal, reconcile.PathSidecarModified:
		return writeLockedHydrateFile(diff.root, path.Path, file, hasLocked)
	case reconcile.PathLocalModified:
		return hydrateLocalModified(diff.root, opts, path.Path, file)
	case reconcile.PathLocalOnly, reconcile.PathConfigUnowned, reconcile.PathNamespaceRemoved:
		return hydrateLocalOnly(diff.root, opts, path.Path)
	case reconcile.PathLocalDeleted:
		return hydrateLocalDeleted(diff.root, opts, path.Path, file, hasLocked)
	case reconcile.PathRemoteDeleted:
		return hydrateRemoteDeleted(diff.root, opts, path.Path)
	case reconcile.PathLocalDeleteConflict:
		return hydrateLocalDeleteConflict(diff.root, opts, path.Path, file, hasLocked)
	case reconcile.PathRemoteDeleteConflict:
		return hydrateRemoteDeleteConflict(diff.root, opts, path.Path)
	case reconcile.PathBothModifiedConflict:
		return s.hydrateBothModified(ctx, diff, opts, namespace, path, file)
	default:
		return hydratePathSkipped, nil
	}
}

func writeLockedHydrateFile(
	root string,
	path string,
	file reconcile.SnapshotFile,
	hasLocked bool,
) (hydratePathAction, error) {
	if !hasLocked {
		return hydratePathSkipped, nil
	}
	if err := writeStringFile(filepath.Join(root, filepath.FromSlash(path)), file.Content); err != nil {
		return hydratePathSkipped, err
	}
	return hydratePathRestored, nil
}

func hydrateLocalModified(
	root string,
	opts HydrateOptions,
	path string,
	file reconcile.SnapshotFile,
) (hydratePathAction, error) {
	if !opts.Theirs {
		return hydratePathSkipped, nil
	}
	return writeLockedHydrateFile(root, path, file, true)
}

func hydrateLocalOnly(root string, opts HydrateOptions, path string) (hydratePathAction, error) {
	if !opts.PruneLocal && !opts.Theirs {
		return hydratePathSkipped, nil
	}
	if err := removeLocalFile(root, path); err != nil {
		return hydratePathSkipped, err
	}
	return hydratePathRestored, nil
}

func hydrateLocalDeleted(
	root string,
	opts HydrateOptions,
	path string,
	file reconcile.SnapshotFile,
	hasLocked bool,
) (hydratePathAction, error) {
	if !opts.Theirs {
		return hydratePathSkipped, nil
	}
	return writeLockedHydrateFile(root, path, file, hasLocked)
}

func hydrateRemoteDeleted(root string, opts HydrateOptions, path string) (hydratePathAction, error) {
	if opts.Ours {
		return hydratePathSkipped, nil
	}
	if err := removeLocalFile(root, path); err != nil {
		return hydratePathSkipped, err
	}
	return hydratePathRestored, nil
}

func hydrateLocalDeleteConflict(
	root string,
	opts HydrateOptions,
	path string,
	file reconcile.SnapshotFile,
	hasLocked bool,
) (hydratePathAction, error) {
	if !opts.Theirs {
		return hydratePathSkipped, nil
	}
	return writeLockedHydrateFile(root, path, file, hasLocked)
}

func hydrateRemoteDeleteConflict(root string, opts HydrateOptions, path string) (hydratePathAction, error) {
	if !opts.Theirs {
		return hydratePathSkipped, nil
	}
	if err := removeLocalFile(root, path); err != nil {
		return hydratePathSkipped, err
	}
	return hydratePathRestored, nil
}

func (s *Service) hydrateBothModified(
	ctx context.Context,
	diff *projectDiff,
	opts HydrateOptions,
	namespace string,
	path reconcile.PathDiff,
	file reconcile.SnapshotFile,
) (hydratePathAction, error) {
	switch {
	case opts.Theirs:
		return writeLockedHydrateFile(diff.root, path.Path, file, true)
	case opts.Merge:
		merged, err := s.mergePath(ctx, diff, namespace, path)
		if err != nil {
			return hydratePathSkipped, err
		}
		if err := writeStringFile(filepath.Join(diff.root, filepath.FromSlash(path.Path)), merged); err != nil {
			return hydratePathSkipped, err
		}
		return hydratePathRestored, nil
	default:
		return hydratePathSkipped, nil
	}
}

func removeLocalFile(root, rel string) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove local file %s: %w", rel, err)
	}
	return nil
}

func (s *Service) mergePath(
	ctx context.Context,
	diff *projectDiff,
	namespace string,
	path reconcile.PathDiff,
) (string, error) {
	if path.Base == nil || path.Base.Blob == "" {
		return "", fmt.Errorf("cannot merge %s without hydration journal base", path.Path)
	}
	locked := diff.locked[namespace][path.Path]
	local := diff.local[namespace][path.Path]
	baseContent, err := s.catFileBatch(ctx, sidecarPath(diff.root), []string{path.Base.Blob})
	if err != nil {
		return "", err
	}
	base := baseContent[path.Base.Blob]
	tmp, err := os.MkdirTemp("", "skeeper-merge-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	basePath := filepath.Join(tmp, "base")
	localPath := filepath.Join(tmp, "local")
	lockedPath := filepath.Join(tmp, "locked")
	mergeInputs := map[string]string{
		basePath:   base,
		localPath:  local.Content,
		lockedPath: locked.Content,
	}
	for filePath, content := range mergeInputs {
		if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
			return "", fmt.Errorf("write merge input: %w", err)
		}
	}
	out, err := s.runner.Run(ctx, tmp, "git", "merge-file", "-p", localPath, basePath, lockedPath)
	if err != nil {
		var commandErr *gitexec.CommandError
		if errors.As(err, &commandErr) && commandErr.ExitCode > 0 && commandErr.ExitCode < 128 && out.Stdout != "" {
			return out.Stdout, nil
		}
		return "", fmt.Errorf("merge conflict for %s: %w", path.Path, err)
	}
	return out.Stdout, nil
}

func (s *Service) writeHydrationJournal(ctx context.Context, diff *projectDiff) error {
	journal := state.HydrationJournal{
		Version:      1,
		SourceBranch: diff.lock.SourceBranch,
		Namespaces:   map[string]state.HydrationNamespace{},
	}
	for _, record := range diff.lock.Namespaces {
		namespace := state.HydrationNamespace{
			LockCommit: record.Commit,
			Files:      map[string]state.HydrationFile{},
		}
		for path, file := range diff.locked[record.Name] {
			namespace.Files[path] = state.HydrationFile{
				SidecarBlob: file.Blob,
				SHA256:      file.SHA256,
				Size:        file.Size,
			}
		}
		journal.Namespaces[record.Name] = namespace
	}
	return diff.store.WriteHydration(ctx, journal)
}

func (s *Service) writeHydrationJournalForLock(
	ctx context.Context,
	root string,
	store *state.Store,
	lock lockfile.Lock,
) error {
	sidecarDir := sidecarPath(root)
	journal := state.HydrationJournal{
		Version:      1,
		SourceBranch: lock.SourceBranch,
		Namespaces:   map[string]state.HydrationNamespace{},
	}
	for _, record := range lock.Namespaces {
		snapshot, err := s.sidecarSnapshot(ctx, sidecarDir, record.Commit, record.Name)
		if err != nil {
			return err
		}
		namespace := state.HydrationNamespace{
			LockCommit: record.Commit,
			Files:      map[string]state.HydrationFile{},
		}
		for path, file := range snapshot {
			namespace.Files[path] = state.HydrationFile{
				SidecarBlob: file.Blob,
				SHA256:      file.SHA256,
				Size:        file.Size,
			}
		}
		journal.Namespaces[record.Name] = namespace
	}
	return store.WriteHydration(ctx, journal)
}

type hydratePolicyIntent string

const (
	hydratePolicyNone   hydratePolicyIntent = ""
	hydratePolicyKeep   hydratePolicyIntent = "keep"
	hydratePolicyAdopt  hydratePolicyIntent = "adopt"
	hydratePolicyPrune  hydratePolicyIntent = "prune_to_rescue"
	hydratePolicyMerge  hydratePolicyIntent = "merge"
	hydratePolicyTheirs hydratePolicyIntent = "overwrite_to_rescue"
)

func applyHydratePolicy(
	cfg config.Config,
	summary reconcile.DiffSummary,
	opts HydrateOptions,
) (HydrateOptions, error) {
	if hasExplicitHydrateResolution(opts) {
		return opts, nil
	}
	selected := hydratePolicyNone
	for _, namespace := range summary.Namespaces {
		cfgNamespace, err := namespaceByName(cfg, namespace.Name)
		if err != nil {
			continue
		}
		for _, path := range namespace.Paths {
			intent, err := hydratePolicyForPath(cfgNamespace, path)
			if err != nil {
				return HydrateOptions{}, err
			}
			if intent == hydratePolicyNone {
				continue
			}
			if selected != hydratePolicyNone && selected != intent {
				return HydrateOptions{}, fmt.Errorf(
					"hydrate policy resolves mixed actions (%s and %s); use an explicit CLI mode",
					selected,
					intent,
				)
			}
			selected = intent
		}
	}
	return applyPolicyIntent(opts, selected), nil
}

func hasExplicitHydrateResolution(opts HydrateOptions) bool {
	return opts.KeepLocal || opts.AdoptLocal || opts.PruneLocal || opts.Merge || opts.Ours || opts.Theirs
}

func hydratePolicyForPath(namespace config.Namespace, path reconcile.PathDiff) (hydratePolicyIntent, error) {
	switch path.Class {
	case reconcile.PathLocalOnly, reconcile.PathConfigUnowned, reconcile.PathNamespaceRemoved:
		return localOnlyPolicyIntent(effectiveLocalOnlyPolicy(namespace, path.Path)), nil
	case reconcile.PathLocalModified:
		return modifiedPolicyIntent(effectiveModifiedPolicy(namespace, path.Path)), nil
	case reconcile.PathBothModifiedConflict:
		value := effectiveModifiedPolicy(namespace, path.Path)
		intent := hydratePolicyIntent(value)
		switch intent {
		case hydratePolicyNone, hydratePolicyMerge, hydratePolicyTheirs:
			return intent, nil
		case hydratePolicyKeep, hydratePolicyAdopt:
			return hydratePolicyNone, fmt.Errorf(
				"hydrate policy %q cannot resolve conflict for %s; inspect the path with `skeeper status --paths`",
				value,
				path.Path,
			)
		default:
			return hydratePolicyNone, nil
		}
	default:
		return hydratePolicyNone, nil
	}
}

func effectiveLocalOnlyPolicy(namespace config.Namespace, path string) string {
	value := namespace.Hydrate.OnLocalOnly
	for _, rule := range namespace.Hydrate.Rules {
		matched, err := doublestar.PathMatch(rule.Pattern, path)
		if err == nil && matched && rule.OnLocalOnly != "" {
			value = rule.OnLocalOnly
		}
	}
	return value
}

func effectiveModifiedPolicy(namespace config.Namespace, path string) string {
	value := namespace.Hydrate.OnModified
	for _, rule := range namespace.Hydrate.Rules {
		matched, err := doublestar.PathMatch(rule.Pattern, path)
		if err == nil && matched && rule.OnModified != "" {
			value = rule.OnModified
		}
	}
	return value
}

func localOnlyPolicyIntent(value string) hydratePolicyIntent {
	switch value {
	case "keep":
		return hydratePolicyKeep
	case "adopt":
		return hydratePolicyAdopt
	case "prune_to_rescue":
		return hydratePolicyPrune
	default:
		return hydratePolicyNone
	}
}

func modifiedPolicyIntent(value string) hydratePolicyIntent {
	switch value {
	case "keep":
		return hydratePolicyKeep
	case "adopt":
		return hydratePolicyAdopt
	case "merge":
		return hydratePolicyMerge
	case "overwrite_to_rescue":
		return hydratePolicyTheirs
	default:
		return hydratePolicyNone
	}
}

func applyPolicyIntent(opts HydrateOptions, intent hydratePolicyIntent) HydrateOptions {
	switch intent {
	case hydratePolicyKeep:
		opts.KeepLocal = true
	case hydratePolicyAdopt:
		opts.AdoptLocal = true
		opts.KeepLocal = true
	case hydratePolicyPrune:
		opts.PruneLocal = true
	case hydratePolicyMerge:
		opts.Merge = true
	case hydratePolicyTheirs:
		opts.Theirs = true
	}
	return opts
}

func (s *Service) fastForwardMain(ctx context.Context, root string) (bool, error) {
	if dirty, err := s.dirty(ctx, root); err != nil {
		return false, err
	} else if dirty {
		return false, fmt.Errorf("main repository has local changes; commit or stash before `skeeper pull`")
	}
	branch, err := s.git.CurrentBranch(ctx, root)
	if err != nil {
		return false, err
	}
	if _, err := s.runner.Run(ctx, root, "git", "fetch", "origin", branch); err != nil {
		var commandErr *gitexec.CommandError
		if errors.As(err, &commandErr) && commandErr.ExitCode == 128 && isMissingUpstream(commandErr.Stderr) {
			return false, nil
		}
		return false, fmt.Errorf("fetch origin/%s: %w", branch, err)
	}
	remoteRef := "refs/remotes/origin/" + branch
	if !s.git.RefExists(ctx, root, remoteRef) {
		return false, nil
	}
	head, err := s.revParse(ctx, root, "HEAD")
	if err != nil {
		return false, err
	}
	remote, err := s.revParse(ctx, root, remoteRef)
	if err != nil {
		return false, err
	}
	if head == remote {
		return false, nil
	}
	if _, err := s.runner.Run(ctx, root, "git", "merge-base", "--is-ancestor", "HEAD", remoteRef); err != nil {
		return false, fmt.Errorf("origin/%s is not a fast-forward of HEAD", branch)
	}
	if _, err := s.runner.Run(ctx, root, "git", "merge", "--ff-only", remoteRef); err != nil {
		return false, fmt.Errorf("fast-forward main repository: %w", err)
	}
	return true, nil
}

func isMissingUpstream(stderr string) bool {
	normalized := strings.ToLower(stderr)
	return strings.Contains(normalized, "couldn't find remote ref") ||
		strings.Contains(normalized, "could not find remote ref") ||
		strings.Contains(normalized, "no such ref")
}

func (s *Service) lockRemoteState(
	ctx context.Context,
	sidecarDir string,
	record lockfile.NamespaceRecord,
) (string, string) {
	if !exists(filepath.Join(sidecarDir, ".git")) {
		return "", "clone_missing"
	}
	refspec := fmt.Sprintf(
		"refs/heads/%s:refs/remotes/origin/%s",
		record.SidecarBranch,
		record.SidecarBranch,
	)
	if _, err := s.runner.Run(ctx, sidecarDir, "git", "fetch", "origin", refspec); err != nil {
		return "", statusUnknown
	}
	remoteRef := "refs/remotes/origin/" + record.SidecarBranch
	if !s.git.RefExists(ctx, sidecarDir, remoteRef) {
		return "", "remote_missing"
	}
	remoteTip, err := s.revParse(ctx, sidecarDir, remoteRef)
	if err != nil {
		return "", statusUnknown
	}
	if remoteTip == record.Commit {
		return remoteTip, "verified"
	}
	return remoteTip, "remote_mismatch"
}

func sha256String(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
