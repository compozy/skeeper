package reconcile

import (
	"sort"
)

// PathClass identifies how one managed path differs between lock, worktree, and local journal state.
type PathClass string

const (
	PathUnchanged            PathClass = "unchanged"
	PathMissingLocal         PathClass = "missing_local"
	PathLocalOnly            PathClass = "local_only"
	PathLocalDeleted         PathClass = "local_deleted"
	PathRemoteDeleted        PathClass = "remote_deleted"
	PathLocalModified        PathClass = "local_modified"
	PathSidecarModified      PathClass = "sidecar_modified"
	PathBothModifiedConflict PathClass = "both_modified_conflict"
	PathLocalDeleteConflict  PathClass = "local_delete_conflict"
	PathRemoteDeleteConflict PathClass = "remote_delete_conflict"
	PathNamespaceRemoved     PathClass = "namespace_removed"
	PathConfigUnowned        PathClass = "config_unowned"
)

// SnapshotFile describes one file in a comparable namespace snapshot.
type SnapshotFile struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Blob    string `json:"blob,omitempty"`
	Content string `json:"-"`
}

// BaseFile describes the last hydrated sidecar state for one path.
type BaseFile struct {
	SidecarBlob string `json:"sidecar_blob,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

// NamespaceDiff summarizes one namespace's per-path lock/worktree diff.
type NamespaceDiff struct {
	Name          string     `json:"name"`
	Branch        string     `json:"branch,omitempty"`
	LockedCommit  string     `json:"locked_commit,omitempty"`
	ExpectedFiles int        `json:"expected_files"`
	ActualFiles   int        `json:"actual_files"`
	ExpectedBytes int64      `json:"expected_bytes"`
	ActualBytes   int64      `json:"actual_bytes"`
	Counts        ClassCount `json:"counts"`
	Paths         []PathDiff `json:"paths,omitempty"`
}

// PathDiff describes one classified path.
type PathDiff struct {
	Path      string    `json:"path"`
	Class     PathClass `json:"class"`
	Locked    *FileSide `json:"locked,omitempty"`
	Local     *FileSide `json:"local,omitempty"`
	Base      *FileSide `json:"base,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
}

// FileSide is the comparable file metadata for one side of a path diff.
type FileSide struct {
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Blob   string `json:"blob,omitempty"`
}

// ClassCount stores per-class counts in a stable JSON shape.
type ClassCount struct {
	Unchanged            int `json:"unchanged"`
	MissingLocal         int `json:"missing_local"`
	LocalOnly            int `json:"local_only"`
	LocalDeleted         int `json:"local_deleted"`
	RemoteDeleted        int `json:"remote_deleted"`
	LocalModified        int `json:"local_modified"`
	SidecarModified      int `json:"sidecar_modified"`
	BothModifiedConflict int `json:"both_modified_conflict"`
	LocalDeleteConflict  int `json:"local_delete_conflict"`
	RemoteDeleteConflict int `json:"remote_delete_conflict"`
	NamespaceRemoved     int `json:"namespace_removed"`
	ConfigUnowned        int `json:"config_unowned"`
}

// DiffSummary aggregates all namespace diffs.
type DiffSummary struct {
	OK         bool            `json:"ok"`
	Counts     ClassCount      `json:"counts"`
	Namespaces []NamespaceDiff `json:"namespaces"`
}

// NamespaceDiffInput contains the comparable data for one namespace.
type NamespaceDiffInput struct {
	Name         string
	Branch       string
	LockedCommit string
	Locked       map[string]SnapshotFile
	Local        map[string]SnapshotFile
	Base         map[string]BaseFile
}

// BuildNamespaceDiff classifies paths for one namespace.
func BuildNamespaceDiff(input NamespaceDiffInput) NamespaceDiff {
	diff := NamespaceDiff{
		Name:         input.Name,
		Branch:       input.Branch,
		LockedCommit: input.LockedCommit,
	}
	paths := map[string]struct{}{}
	for path, file := range input.Locked {
		paths[path] = struct{}{}
		diff.ExpectedFiles++
		diff.ExpectedBytes += file.Size
	}
	for path, file := range input.Local {
		paths[path] = struct{}{}
		diff.ActualFiles++
		diff.ActualBytes += file.Size
	}
	for path := range input.Base {
		paths[path] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		locked, hasLocked := input.Locked[path]
		local, hasLocal := input.Local[path]
		base, hasBase := input.Base[path]
		class := classifyPath(locked, hasLocked, local, hasLocal, base, hasBase)
		pathDiff := PathDiff{
			Path:      path,
			Class:     class,
			Namespace: input.Name,
			Locked:    fileSide(locked, hasLocked),
			Local:     fileSide(local, hasLocal),
			Base:      baseSide(base, hasBase),
		}
		diff.Paths = append(diff.Paths, pathDiff)
		incrementClass(&diff.Counts, class)
	}
	return diff
}

// SummarizeDiff aggregates namespace diffs and marks OK only when every path is unchanged.
func SummarizeDiff(namespaces []NamespaceDiff) DiffSummary {
	summary := DiffSummary{OK: true, Namespaces: namespaces}
	for _, namespace := range namespaces {
		addCounts(&summary.Counts, namespace.Counts)
		if namespace.Counts.MissingLocal != 0 ||
			namespace.Counts.LocalOnly != 0 ||
			namespace.Counts.LocalDeleted != 0 ||
			namespace.Counts.RemoteDeleted != 0 ||
			namespace.Counts.LocalModified != 0 ||
			namespace.Counts.SidecarModified != 0 ||
			namespace.Counts.BothModifiedConflict != 0 ||
			namespace.Counts.LocalDeleteConflict != 0 ||
			namespace.Counts.RemoteDeleteConflict != 0 ||
			namespace.Counts.NamespaceRemoved != 0 ||
			namespace.Counts.ConfigUnowned != 0 {
			summary.OK = false
		}
	}
	return summary
}

// FilterPathDiffs returns paths matching any provided class. Empty classes return every path.
func FilterPathDiffs(paths []PathDiff, classes ...PathClass) []PathDiff {
	if len(classes) == 0 {
		return append([]PathDiff(nil), paths...)
	}
	allowed := make(map[PathClass]struct{}, len(classes))
	for _, class := range classes {
		allowed[class] = struct{}{}
	}
	filtered := make([]PathDiff, 0, len(paths))
	for _, path := range paths {
		if _, ok := allowed[path.Class]; ok {
			filtered = append(filtered, path)
		}
	}
	return filtered
}

func classifyPath(
	locked SnapshotFile,
	hasLocked bool,
	local SnapshotFile,
	hasLocal bool,
	base BaseFile,
	hasBase bool,
) PathClass {
	switch {
	case hasLocked && hasLocal:
		return classifyPresentPath(locked, local, base, hasBase)
	case hasLocked:
		return classifyLocalAbsentPath(locked, base, hasBase)
	case hasLocal:
		return classifySidecarAbsentPath(local, base, hasBase)
	case hasBase:
		return PathUnchanged
	default:
		return PathConfigUnowned
	}
}

func classifyPresentPath(locked SnapshotFile, local SnapshotFile, base BaseFile, hasBase bool) PathClass {
	if locked.SHA256 == local.SHA256 {
		return PathUnchanged
	}
	if !hasComparableBase(base, hasBase) {
		return PathLocalModified
	}
	localChanged := local.SHA256 != base.SHA256
	sidecarChanged := locked.SHA256 != base.SHA256
	switch {
	case localChanged && sidecarChanged:
		return PathBothModifiedConflict
	case sidecarChanged:
		return PathSidecarModified
	default:
		return PathLocalModified
	}
}

func classifyLocalAbsentPath(locked SnapshotFile, base BaseFile, hasBase bool) PathClass {
	if !hasComparableBase(base, hasBase) {
		return PathMissingLocal
	}
	if locked.SHA256 == base.SHA256 {
		return PathLocalDeleted
	}
	return PathLocalDeleteConflict
}

func classifySidecarAbsentPath(local SnapshotFile, base BaseFile, hasBase bool) PathClass {
	if !hasComparableBase(base, hasBase) {
		return PathLocalOnly
	}
	if local.SHA256 == base.SHA256 {
		return PathRemoteDeleted
	}
	return PathRemoteDeleteConflict
}

func hasComparableBase(base BaseFile, hasBase bool) bool {
	return hasBase && base.SHA256 != ""
}

func fileSide(file SnapshotFile, ok bool) *FileSide {
	if !ok {
		return nil
	}
	return &FileSide{SHA256: file.SHA256, Size: file.Size, Blob: file.Blob}
}

func baseSide(file BaseFile, ok bool) *FileSide {
	if !ok {
		return nil
	}
	return &FileSide{SHA256: file.SHA256, Size: file.Size, Blob: file.SidecarBlob}
}

func incrementClass(counts *ClassCount, class PathClass) {
	switch class {
	case PathUnchanged:
		counts.Unchanged++
	case PathMissingLocal:
		counts.MissingLocal++
	case PathLocalOnly:
		counts.LocalOnly++
	case PathLocalDeleted:
		counts.LocalDeleted++
	case PathRemoteDeleted:
		counts.RemoteDeleted++
	case PathLocalModified:
		counts.LocalModified++
	case PathSidecarModified:
		counts.SidecarModified++
	case PathBothModifiedConflict:
		counts.BothModifiedConflict++
	case PathLocalDeleteConflict:
		counts.LocalDeleteConflict++
	case PathRemoteDeleteConflict:
		counts.RemoteDeleteConflict++
	case PathNamespaceRemoved:
		counts.NamespaceRemoved++
	case PathConfigUnowned:
		counts.ConfigUnowned++
	}
}

func addCounts(total *ClassCount, next ClassCount) {
	total.Unchanged += next.Unchanged
	total.MissingLocal += next.MissingLocal
	total.LocalOnly += next.LocalOnly
	total.LocalDeleted += next.LocalDeleted
	total.RemoteDeleted += next.RemoteDeleted
	total.LocalModified += next.LocalModified
	total.SidecarModified += next.SidecarModified
	total.BothModifiedConflict += next.BothModifiedConflict
	total.LocalDeleteConflict += next.LocalDeleteConflict
	total.RemoteDeleteConflict += next.RemoteDeleteConflict
	total.NamespaceRemoved += next.NamespaceRemoved
	total.ConfigUnowned += next.ConfigUnowned
}
