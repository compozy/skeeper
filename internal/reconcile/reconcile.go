// Package reconcile builds skeeper operation plans from project state.
package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/matcher"
)

// RepoRoot is a normalized repository root path.
type RepoRoot string

// NamespaceName is a configured sidecar namespace name.
type NamespaceName string

// SidecarRef is a sidecar Git ref or commit.
type SidecarRef string

// PlanKind identifies the operation a plan supports.
type PlanKind string

const (
	// PlanKindSync mirrors owned files into the sidecar.
	PlanKindSync PlanKind = "sync"
	// PlanKindAdopt moves main-repo tracked files under sidecar coverage.
	PlanKindAdopt PlanKind = "adopt"
	// PlanKindUntrack removes main-repo index tracking while preserving sidecar coverage.
	PlanKindUntrack PlanKind = "untrack"
	// PlanKindPattern updates namespace ownership patterns.
	PlanKindPattern PlanKind = "pattern"
	// PlanKindVerify validates skeeper.lock against the sidecar remote.
	PlanKindVerify PlanKind = "verify"
	// PlanKindFSCK compares the working tree against skeeper.lock.
	PlanKindFSCK PlanKind = "fsck"
)

// Planner constructs operation plans.
type Planner interface {
	PlanSync(ctx context.Context, root RepoRoot, opts SyncPlanOptions) (Plan, error)
	PlanAdopt(ctx context.Context, root RepoRoot, targets []string, opts AdoptPlanOptions) (Plan, error)
	PlanUntrack(ctx context.Context, root RepoRoot, targets []string, opts UntrackPlanOptions) (Plan, error)
	PlanPattern(ctx context.Context, root RepoRoot, glob string, opts PatternPlanOptions) (Plan, error)
	PlanVerify(ctx context.Context, root RepoRoot, opts VerifyPlanOptions) (Plan, error)
	PlanFSCK(ctx context.Context, root RepoRoot, opts FSCKPlanOptions) (Plan, error)
}

// Plan describes the reconciled operation.
type Plan struct {
	Kind         PlanKind         `json:"kind"`
	Root         RepoRoot         `json:"root"`
	SourceBranch string           `json:"source_branch"`
	SidecarURL   string           `json:"sidecar_url"`
	Config       config.Config    `json:"-"`
	Namespaces   []NamespacePlan  `json:"namespaces"`
	Operations   []Operation      `json:"operations"`
	Warnings     []Diagnostic     `json:"warnings,omitempty"`
	Failures     []Diagnostic     `json:"failures,omitempty"`
	Guardrails   GuardrailReport  `json:"guardrails"`
	Targets      []TargetDecision `json:"targets,omitempty"`
}

// NamespacePlan describes one namespace in a plan.
type NamespacePlan struct {
	Name          NamespaceName     `json:"name"`
	Branch        string            `json:"branch"`
	Namespace     config.Namespace  `json:"-"`
	Files         []FilePlan        `json:"files"`
	StagedContent map[string]string `json:"-"`
}

// FilePlan describes one owned main-repo path.
type FilePlan struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Staged bool   `json:"staged,omitempty"`
}

// Operation describes one planned side effect.
type Operation struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Path      string `json:"path,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Diagnostic describes a stable machine-readable warning or failure.
type Diagnostic struct {
	Code      string `json:"code"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	Path      string `json:"path,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Recovery  string `json:"recovery,omitempty"`
}

// GuardrailReport summarizes broad-plan thresholds.
type GuardrailReport struct {
	Files         int   `json:"files"`
	Bytes         int64 `json:"bytes"`
	MaxFiles      int   `json:"max_files"`
	MaxBytes      int64 `json:"max_bytes"`
	RequiresForce bool  `json:"requires_force"`
}

// TargetDecision describes an adopted or untracked target.
type TargetDecision struct {
	Path      string `json:"path"`
	Namespace string `json:"namespace"`
	Tracked   bool   `json:"tracked"`
}

// SyncPlanOptions configures sync planning.
type SyncPlanOptions struct {
	Staged bool
	Force  bool
}

// AdoptPlanOptions configures adopt planning.
type AdoptPlanOptions struct {
	Force bool
}

// UntrackPlanOptions configures untrack planning.
type UntrackPlanOptions struct {
	Force bool
}

// PatternPlanOptions configures pattern planning.
type PatternPlanOptions struct {
	Namespace     string
	Exclude       []string
	AdoptExisting bool
	Force         bool
}

// VerifyPlanOptions configures verify planning.
type VerifyPlanOptions struct {
	SourceBranch string
}

// FSCKPlanOptions configures fsck planning.
type FSCKPlanOptions struct {
	SourceBranch string
}

// DefaultPlanner is the production planner.
type DefaultPlanner struct {
	runner gitexec.Runner
	git    *gitexec.Git
}

var _ Planner = (*DefaultPlanner)(nil)

// NewPlanner returns a production planner.
func NewPlanner(runner gitexec.Runner) *DefaultPlanner {
	return &DefaultPlanner{runner: runner, git: gitexec.NewGit()}
}

// PlanSync builds a sync plan.
func (p *DefaultPlanner) PlanSync(ctx context.Context, root RepoRoot, opts SyncPlanOptions) (Plan, error) {
	return p.planOwned(ctx, root, PlanKindSync, opts.Staged, opts.Force)
}

// PlanAdopt builds an adoption plan.
func (p *DefaultPlanner) PlanAdopt(
	ctx context.Context,
	root RepoRoot,
	targets []string,
	opts AdoptPlanOptions,
) (Plan, error) {
	plan, err := p.planOwned(ctx, root, PlanKindAdopt, false, opts.Force)
	if err != nil {
		return Plan{}, err
	}
	decisions, err := p.targetDecisions(ctx, plan, targets)
	if err != nil {
		return Plan{}, err
	}
	plan.Targets = decisions
	for _, decision := range decisions {
		plan.Operations = append(plan.Operations, Operation{
			Kind:      "git.rm.cached",
			Namespace: decision.Namespace,
			Path:      decision.Path,
		})
	}
	return plan, nil
}

// PlanUntrack builds an untrack plan.
func (p *DefaultPlanner) PlanUntrack(
	ctx context.Context,
	root RepoRoot,
	targets []string,
	opts UntrackPlanOptions,
) (Plan, error) {
	plan, err := p.planOwned(ctx, root, PlanKindUntrack, false, opts.Force)
	if err != nil {
		return Plan{}, err
	}
	decisions, err := p.targetDecisions(ctx, plan, targets)
	if err != nil {
		return Plan{}, err
	}
	plan.Targets = decisions
	for _, decision := range decisions {
		plan.Operations = append(plan.Operations, Operation{
			Kind:      "git.rm.cached",
			Namespace: decision.Namespace,
			Path:      decision.Path,
		})
	}
	return plan, nil
}

// PlanPattern builds a pattern update plan.
func (p *DefaultPlanner) PlanPattern(
	ctx context.Context,
	root RepoRoot,
	glob string,
	opts PatternPlanOptions,
) (Plan, error) {
	plan, err := p.planOwned(ctx, root, PlanKindPattern, false, opts.Force)
	if err != nil {
		return Plan{}, err
	}
	if _, err := config.NormalizePatterns([]string{glob}); err != nil {
		return Plan{}, err
	}
	namespace := strings.TrimSpace(opts.Namespace)
	if namespace == "" && len(plan.Config.Namespaces) == 1 {
		namespace = plan.Config.Namespaces[0].Name
	}
	if namespace == "" {
		return Plan{}, fmt.Errorf("--namespace is required when multiple namespaces are configured")
	}
	if _, err := config.CleanNamespace(namespace); err != nil {
		return Plan{}, err
	}
	plan.Operations = append(plan.Operations, Operation{
		Kind:      "config.pattern.add",
		Namespace: namespace,
		Path:      glob,
	})
	return plan, nil
}

// PlanVerify builds a verify plan.
func (p *DefaultPlanner) PlanVerify(ctx context.Context, root RepoRoot, opts VerifyPlanOptions) (Plan, error) {
	plan, err := p.planOwned(ctx, root, PlanKindVerify, false, true)
	if err != nil {
		return Plan{}, err
	}
	if opts.SourceBranch != "" {
		plan.SourceBranch = opts.SourceBranch
	}
	return plan, nil
}

// PlanFSCK builds an fsck plan.
func (p *DefaultPlanner) PlanFSCK(ctx context.Context, root RepoRoot, opts FSCKPlanOptions) (Plan, error) {
	plan, err := p.planOwned(ctx, root, PlanKindFSCK, false, true)
	if err != nil {
		return Plan{}, err
	}
	if opts.SourceBranch != "" {
		plan.SourceBranch = opts.SourceBranch
	}
	return plan, nil
}

func (p *DefaultPlanner) planOwned(
	ctx context.Context,
	root RepoRoot,
	kind PlanKind,
	staged bool,
	force bool,
) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	rootPath := string(root)
	cfg, err := config.Load(rootPath)
	if err != nil {
		return Plan{}, err
	}
	branch, err := p.git.CurrentBranch(ctx, rootPath)
	if err != nil {
		return Plan{}, err
	}
	stagedState, err := p.loadStagedState(ctx, rootPath, staged)
	if err != nil {
		return Plan{}, err
	}
	namespaces, err := p.planNamespaces(ctx, rootPath, cfg, branch, staged, stagedState)
	if err != nil {
		return Plan{}, err
	}
	guardrails := guardrailReport(namespaces, cfg.Settings)
	if guardrails.RequiresForce && !force {
		return Plan{}, fmt.Errorf(
			"plan touches %d files/%d bytes; rerun with --force to exceed configured guardrails",
			guardrails.Files,
			guardrails.Bytes,
		)
	}
	plan := Plan{
		Kind:         kind,
		Root:         root,
		SourceBranch: branch,
		SidecarURL:   cfg.Sidecar,
		Config:       cfg,
		Namespaces:   namespaces,
		Guardrails:   guardrails,
	}
	for _, namespace := range namespaces {
		plan.Operations = append(plan.Operations, Operation{
			Kind:      "sidecar.mirror",
			Namespace: string(namespace.Name),
			Message:   fmt.Sprintf("mirror %d file(s)", len(namespace.Files)),
		})
	}
	return plan, nil
}

type stagedState struct {
	content map[string]string
	set     map[string]struct{}
}

func (p *DefaultPlanner) loadStagedState(ctx context.Context, root string, staged bool) (stagedState, error) {
	state := stagedState{content: map[string]string{}, set: map[string]struct{}{}}
	if !staged {
		return state, nil
	}
	content, err := p.stagedBlobs(ctx, root)
	if err != nil {
		return stagedState{}, err
	}
	state.content = content
	for path := range content {
		state.set[path] = struct{}{}
	}
	return state, nil
}

func (p *DefaultPlanner) planNamespaces(
	ctx context.Context,
	root string,
	cfg config.Config,
	branch string,
	staged bool,
	stagedState stagedState,
) ([]NamespacePlan, error) {
	owners := map[string]string{}
	namespaces := make([]NamespacePlan, 0, len(cfg.Namespaces))
	for _, namespace := range cfg.Namespaces {
		plan, err := p.planNamespace(ctx, root, branch, namespace, staged, stagedState, owners)
		if err != nil {
			return nil, err
		}
		namespaces = append(namespaces, plan)
	}
	return namespaces, nil
}

func (p *DefaultPlanner) planNamespace(
	ctx context.Context,
	root string,
	branch string,
	namespace config.Namespace,
	staged bool,
	stagedState stagedState,
	owners map[string]string,
) (NamespacePlan, error) {
	files, err := matcher.FindContextWithOptions(
		ctx,
		root,
		namespace.Patterns,
		matcher.Options{RespectGitignore: namespace.RespectsGitignore()},
	)
	if err != nil {
		return NamespacePlan{}, err
	}
	if staged {
		files = append(files, matchingStagedPaths(namespace, stagedState.content)...)
		files = uniqueSorted(files)
	}
	planned, nsStaged, err := planFiles(root, namespace, files, stagedState, owners)
	if err != nil {
		return NamespacePlan{}, err
	}
	return NamespacePlan{
		Name:          NamespaceName(namespace.Name),
		Branch:        BranchName(namespace.Name, branch),
		Namespace:     namespace,
		Files:         planned,
		StagedContent: nsStaged,
	}, nil
}

func planFiles(
	root string,
	namespace config.Namespace,
	files []string,
	stagedState stagedState,
	owners map[string]string,
) ([]FilePlan, map[string]string, error) {
	planned := make([]FilePlan, 0, len(files))
	nsStagedContent := map[string]string{}
	for _, file := range files {
		if !namespace.Owns(file) {
			continue
		}
		if previous, ok := owners[file]; ok {
			return nil, nil, fmt.Errorf(
				"%s matches multiple skeeper namespaces: %s, %s",
				file,
				previous,
				namespace.Name,
			)
		}
		owners[file] = namespace.Name
		_, isStaged := stagedState.set[file]
		planned = append(planned, FilePlan{
			Path:   file,
			Size:   fileSize(root, file, stagedState.content),
			Staged: isStaged,
		})
		if isStaged {
			nsStagedContent[file] = stagedState.content[file]
		}
	}
	return planned, nsStagedContent, nil
}

func (p *DefaultPlanner) targetDecisions(ctx context.Context, plan Plan, targets []string) ([]TargetDecision, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one path or glob is required")
	}
	expanded, err := expandTargets(ctx, string(plan.Root), targets)
	if err != nil {
		return nil, err
	}
	if len(expanded) == 0 {
		return nil, fmt.Errorf("targets did not match any files")
	}
	owners := map[string]string{}
	for _, namespace := range plan.Namespaces {
		for _, file := range namespace.Files {
			owners[file.Path] = string(namespace.Name)
		}
	}
	tracked, err := p.trackedSet(ctx, string(plan.Root), expanded)
	if err != nil {
		return nil, err
	}
	decisions := make([]TargetDecision, 0, len(expanded))
	for _, path := range expanded {
		namespace, ok := owners[path]
		if !ok {
			return nil, fmt.Errorf("%s is not owned by any skeeper namespace", path)
		}
		if !tracked[path] {
			return nil, fmt.Errorf("%s is not tracked by git", path)
		}
		decisions = append(decisions, TargetDecision{
			Path:      path,
			Namespace: namespace,
			Tracked:   tracked[path],
		})
	}
	return decisions, nil
}

func (p *DefaultPlanner) trackedSet(ctx context.Context, root string, paths []string) (map[string]bool, error) {
	result := make(map[string]bool, len(paths))
	if len(paths) == 0 {
		return result, nil
	}
	args := append([]string{"ls-files", "-z", "--"}, paths...)
	out, err := p.runner.Run(ctx, root, "git", args...)
	if err != nil {
		return nil, fmt.Errorf("read tracked target state: %w", err)
	}
	for path := range strings.SplitSeq(out.Stdout, "\x00") {
		if path != "" {
			result[filepath.ToSlash(path)] = true
		}
	}
	return result, nil
}

func (p *DefaultPlanner) stagedBlobs(ctx context.Context, root string) (map[string]string, error) {
	out, err := p.runner.Run(ctx, root, "git", "diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR")
	if err != nil {
		return nil, fmt.Errorf("read staged paths: %w", err)
	}
	blobs := map[string]string{}
	for raw := range strings.SplitSeq(out.Stdout, "\x00") {
		path := filepath.ToSlash(strings.TrimSpace(raw))
		if path == "" {
			continue
		}
		blob, err := p.runner.Run(ctx, root, "git", "show", ":"+path)
		if err != nil {
			return nil, fmt.Errorf("read staged blob %s: %w", path, err)
		}
		blobs[path] = blob.Stdout
	}
	return blobs, nil
}

// BranchName returns the deterministic sidecar branch for a namespace/source branch pair.
func BranchName(namespace, sourceBranch string) string {
	return namespace + "/" + config.NamespaceBranchSegment + "/" + sourceBranch
}

func matchingStagedPaths(namespace config.Namespace, staged map[string]string) []string {
	files := make([]string, 0)
	for path := range staged {
		if namespace.Owns(path) {
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files
}

func fileSize(root, rel string, staged map[string]string) int64 {
	if content, ok := staged[rel]; ok {
		return int64(len(content))
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

func guardrailReport(namespaces []NamespacePlan, settings config.Settings) GuardrailReport {
	report := GuardrailReport{
		MaxFiles: settings.Guardrails.MaxFiles,
		MaxBytes: settings.Guardrails.MaxBytes,
	}
	for _, namespace := range namespaces {
		for _, file := range namespace.Files {
			report.Files++
			report.Bytes += file.Size
		}
	}
	report.RequiresForce = report.Files > report.MaxFiles || report.Bytes > report.MaxBytes
	return report
}

func expandTargets(ctx context.Context, root string, targets []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, target := range targets {
		normalized := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(target)), "./")
		if normalized == "" {
			return nil, fmt.Errorf("target cannot be empty")
		}
		if hasGlobMeta(normalized) {
			matches, err := walkGlob(ctx, root, normalized)
			if err != nil {
				return nil, err
			}
			for _, match := range matches {
				seen[match] = struct{}{}
			}
			continue
		}
		rel, err := cleanRelative(root, normalized)
		if err != nil {
			return nil, err
		}
		seen[rel] = struct{}{}
	}
	return sortedKeys(seen), nil
}

func walkGlob(ctx context.Context, root, pattern string) ([]string, error) {
	matches := map[string]struct{}{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "." {
			return nil
		}
		first, _, _ := strings.Cut(rel, "/")
		if entry.IsDir() && (first == ".git" || first == ".skeeper") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		ok, err := doublestar.PathMatch(pattern, rel)
		if err != nil {
			return err
		}
		if ok {
			matches[rel] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("expand target glob %q: %w", pattern, err)
	}
	return sortedKeys(matches), nil
}

func cleanRelative(root, candidate string) (string, error) {
	path := candidate
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, filepath.FromSlash(path))
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the project root", candidate)
	}
	return filepath.ToSlash(filepath.Clean(rel)), nil
}

func hasGlobMeta(value string) bool {
	return strings.ContainsAny(value, "*?[{")
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	return sortedKeys(seen)
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}
