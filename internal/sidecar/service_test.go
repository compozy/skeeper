package sidecar_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/lockfile"
	"github.com/compozy/skeeper/internal/reconcile"
	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/compozy/skeeper/internal/state"
)

func TestServiceSyncHydrateStatusAndLogWithRealGit(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := sidecar.UpdateGitignore(root, cfg.Namespaces); err != nil {
		t.Fatalf("update gitignore: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename, ".gitignore")
	git(t, root, "commit", "-m", "bootstrap")

	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.Sync(ctx, root, sidecar.SyncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !result.Committed {
		t.Fatal("expected sidecar commit")
	}
	if result.ChangedFiles != 1 {
		t.Fatalf("expected 1 changed file, got %d", result.ChangedFiles)
	}
	assertFile(t, filepath.Join(root, sidecar.DirName, "project/src/auth/SPEC.md"), "# Auth\n")

	logOutput, err := service.Log(ctx, root, "src/auth/SPEC.md")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if !strings.Contains(logOutput, "sync namespace project") {
		t.Fatalf("expected sync history, got %q", logOutput)
	}
	latestLog, err := service.Log(ctx, root, "src/auth/SPEC.md", sidecar.LogOptions{Latest: true})
	if err != nil {
		t.Fatalf("latest log: %v", err)
	}
	for _, want := range []string{"latest:", "locked:", "state: up-to-date"} {
		if !strings.Contains(latestLog, want) {
			t.Fatalf("latest log missing %q:\n%s", want, latestLog)
		}
	}
	fullCommit := gitOutput(t, filepath.Join(root, sidecar.DirName), "log", "-1", "--format=%B")
	if strings.Contains(fullCommit, "Main-Commit:") {
		t.Fatalf("sidecar commits must not contain legacy Main-Commit trailers:\n%s", fullCommit)
	}
	if !strings.Contains(fullCommit, "Namespace-Digest: sha256:") {
		t.Fatalf("expected namespace digest metadata in sidecar commit:\n%s", fullCommit)
	}
	if _, err := os.Stat(filepath.Join(root, "skeeper.lock")); err != nil {
		t.Fatalf("expected skeeper.lock to be written: %v", err)
	}

	status, err := service.Status(ctx, root)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Sidecar != remote || status.Branch != "main" || len(status.Namespaces) != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.Namespaces[0].TrackedFiles != 1 {
		t.Fatalf("expected 1 tracked file, got %#v", status.Namespaces[0])
	}
	if status.Namespaces[0].LastCommit == "" {
		t.Fatal("expected last sidecar commit in status")
	}
	if status.Namespaces[0].LockedCommit == "" {
		t.Fatal("expected locked sidecar commit in status")
	}

	if err := os.RemoveAll(filepath.Join(root, sidecar.DirName)); err != nil {
		t.Fatalf("remove sidecar clone: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove main spec: %v", err)
	}
	if err := os.Remove(filepath.Join(root, ".git", "skeeper", "hydration.json")); err != nil {
		t.Fatalf("remove hydration journal: %v", err)
	}
	hydrated, err := service.Hydrate(ctx, root)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(hydrated.Restored) != 1 || hydrated.Restored[0] != "src/auth/SPEC.md" {
		t.Fatalf("unexpected hydrate result: %#v", hydrated)
	}
	assertFile(t, filepath.Join(root, "src/auth/SPEC.md"), "# Auth\n")

	statusOutput := gitOutput(t, root, "status", "--short", "--ignored", "src/auth/SPEC.md")
	if !strings.Contains(statusOutput, "!! src/auth/SPEC.md") {
		t.Fatalf("expected main repo to ignore spec file, got %q", statusOutput)
	}
}

func TestServiceSyncCopiesProjectIdentityIntoSidecarClone(t *testing.T) {
	isolateGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	git(t, root, "config", "user.name", "Skeeper CI")
	git(t, root, "config", "user.email", "skeeper-ci@example.com")
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	git(t, root, "add", config.Filename, "src/auth/SPEC.md")
	git(t, root, "commit", "-m", "bootstrap")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	sidecarDir := filepath.Join(root, sidecar.DirName)
	if got := gitOutput(t, sidecarDir, "config", "--local", "--get", "user.name"); got != "Skeeper CI" {
		t.Fatalf("sidecar user.name mismatch: got %q", got)
	}
	if got := gitOutput(t, sidecarDir, "config", "--local", "--get", "user.email"); got != "skeeper-ci@example.com" {
		t.Fatalf("sidecar user.email mismatch: got %q", got)
	}
	if got := gitOutput(
		t,
		sidecarDir,
		"log",
		"-1",
		"--format=%an <%ae>",
	); got != "Skeeper CI <skeeper-ci@example.com>" {
		t.Fatalf("sidecar commit author mismatch: got %q", got)
	}
}

func TestServiceVerifyAndFSCKUseLockfile(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	verify, err := service.Verify(ctx, root, sidecar.VerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verify.OK {
		t.Fatalf("expected verify ok: %#v", verify)
	}
	fsck, err := service.FSCK(ctx, root, sidecar.FSCKOptions{})
	if err != nil {
		t.Fatalf("fsck: %v", err)
	}
	if !fsck.OK {
		t.Fatalf("expected fsck ok: %#v", fsck)
	}

	writeFile(t, root, "src/auth/SPEC.md", "# Drift\n")
	fsck, err = service.FSCK(ctx, root, sidecar.FSCKOptions{})
	if err != nil {
		t.Fatalf("fsck drift: %v", err)
	}
	if fsck.OK || len(fsck.Diagnostics) == 0 || fsck.Diagnostics[0].Code != "fsck.working_tree_drift" {
		t.Fatalf("expected fsck drift diagnostic: %#v", fsck)
	}
	if len(fsck.Namespaces) != 1 || len(fsck.Namespaces[0].Paths) == 0 {
		t.Fatalf("expected fsck path-level drift: %#v", fsck)
	}
	if fsck.Namespaces[0].Paths[0].Path != "src/auth/SPEC.md" ||
		fsck.Namespaces[0].Paths[0].Class != "local_modified" {
		t.Fatalf("unexpected fsck path diff: %#v", fsck.Namespaces[0].Paths)
	}

	data, err := os.ReadFile(filepath.Join(root, "skeeper.lock"))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	const badDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	lockText := string(data)
	digestOffset := strings.Index(lockText, "sha256:")
	if digestOffset == -1 {
		t.Fatalf("lock missing digest:\n%s", lockText)
	}
	tampered := lockText[:digestOffset] + badDigest + lockText[digestOffset+len(badDigest):]
	if err := os.WriteFile(filepath.Join(root, "skeeper.lock"), []byte(tampered), 0o644); err != nil {
		t.Fatalf("tamper lock: %v", err)
	}
	verify, err = service.Verify(ctx, root, sidecar.VerifyOptions{})
	if err != nil {
		t.Fatalf("verify tampered: %v", err)
	}
	if verify.OK || len(verify.Diagnostics) == 0 || verify.Diagnostics[0].Code != "lock.digest_mismatch" {
		t.Fatalf("expected verify mismatch diagnostic: %#v", verify)
	}
}

func TestServiceFSCKHandlesDuplicateSidecarBlobs(t *testing.T) {
	t.Run("Should handle duplicate sidecar blobs", func(t *testing.T) {
		setGitIdentity(t)

		ctx := context.Background()
		root := newMainRepo(t)
		remote := newBareRepo(t)
		cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
		bootstrapRepo(t, root, cfg)
		writeFile(t, root, "src/auth/SPEC.md", "# Shared\n")
		writeFile(t, root, "src/billing/SPEC.md", "# Shared\n")

		service := sidecar.New(&gitexec.ExecRunner{})
		if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
			t.Fatalf("sync: %v", err)
		}

		fsck, err := service.FSCK(ctx, root, sidecar.FSCKOptions{})
		if err != nil {
			t.Fatalf("fsck: %v", err)
		}
		if !fsck.OK {
			t.Fatalf("expected duplicate-content specs to fsck clean: %#v", fsck)
		}

		journalPath := filepath.Join(root, ".git", "skeeper", "hydration.json")
		data, err := os.ReadFile(journalPath)
		if err != nil {
			t.Fatalf("read hydration journal: %v", err)
		}
		var journal state.HydrationJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			t.Fatalf("decode hydration journal: %v", err)
		}
		files := journal.Namespaces["project"].Files
		auth := files["src/auth/SPEC.md"]
		billing := files["src/billing/SPEC.md"]
		if auth.SHA256 == "" || billing.SHA256 == "" {
			t.Fatalf("expected duplicate blob entries to keep sha256: %#v", files)
		}
		if auth.SHA256 != billing.SHA256 || auth.SidecarBlob != billing.SidecarBlob {
			t.Fatalf("expected duplicate files to share digest and blob: %#v", files)
		}
	})
}

func TestServiceHydrateBlocksLocalOnlyByDefaultAndPrunesToRescue(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	writeFile(t, root, "src/local/SPEC.md", "# Local only\n")

	blocked, err := service.Hydrate(ctx, root)
	if err != nil {
		t.Fatalf("hydrate blocked result: %v", err)
	}
	if blocked.OK || len(blocked.Diagnostics) == 0 {
		t.Fatalf("expected blocked hydrate result: %#v", blocked)
	}
	if _, err := os.Stat(filepath.Join(root, "src/local/SPEC.md")); err != nil {
		t.Fatalf("blocked hydrate must preserve local file: %v", err)
	}

	pruned, err := service.Hydrate(ctx, root, sidecar.HydrateOptions{PruneLocal: true})
	if err != nil {
		t.Fatalf("hydrate prune: %v", err)
	}
	if !pruned.OK || pruned.Rescue == nil || len(pruned.Rescue.Files) != 1 {
		t.Fatalf("expected prune rescue: %#v", pruned)
	}
	if _, err := os.Stat(filepath.Join(root, "src/local/SPEC.md")); !os.IsNotExist(err) {
		t.Fatalf("expected local-only file moved to rescue, stat err=%v", err)
	}
	rescues, err := service.RescueList(ctx, root)
	if err != nil {
		t.Fatalf("rescue list: %v", err)
	}
	if len(rescues.Rescues) != 1 {
		t.Fatalf("expected one rescue manifest: %#v", rescues)
	}
	restored, err := service.RescueRestore(
		ctx,
		root,
		pruned.Rescue.ID,
		[]string{"src/local/SPEC.md"},
		sidecar.RescueRestoreOptions{},
	)
	if err != nil {
		t.Fatalf("rescue restore: %v", err)
	}
	if len(restored.Files) != 1 {
		t.Fatalf("expected restored file: %#v", restored)
	}
	assertFile(t, filepath.Join(root, "src/local/SPEC.md"), "# Local only\n")
}

func TestServiceReconcileAdoptLocalPublishesLocalOnlyFiles(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	writeFile(t, root, "src/local/SPEC.md", "# Local only\n")

	result, err := service.Reconcile(ctx, root, sidecar.ReconcileOptions{AdoptLocal: true})
	if err != nil {
		t.Fatalf("reconcile adopt local: %v", err)
	}
	if !result.OK || result.Hydrate == nil {
		t.Fatalf("expected successful reconcile: %#v", result)
	}
	assertSidecarFile(t, remote, "project/__branches__/main", "project/src/local/SPEC.md", "# Local only\n")
	fsck, err := service.FSCK(ctx, root, sidecar.FSCKOptions{})
	if err != nil {
		t.Fatalf("fsck after adopt: %v", err)
	}
	if !fsck.OK {
		t.Fatalf("expected fsck ok after adopt: %#v", fsck)
	}
}

func TestServiceFSCKClassifiesConfigUnownedAndNamespaceRemoved(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	cfg.Namespaces[0].Patterns = []string{"docs/**"}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config-unowned config: %v", err)
	}
	fsck, err := service.FSCK(ctx, root, sidecar.FSCKOptions{})
	if err != nil {
		t.Fatalf("fsck config-unowned: %v", err)
	}
	if fsck.OK || fsck.Namespaces[0].Counts.ConfigUnowned != 1 {
		t.Fatalf("expected config_unowned drift: %#v", fsck)
	}

	cfg.Namespaces[0].Name = "other"
	cfg.Namespaces[0].Patterns = []string{"**/SPEC.md"}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save namespace-removed config: %v", err)
	}
	fsck, err = service.FSCK(ctx, root, sidecar.FSCKOptions{})
	if err != nil {
		t.Fatalf("fsck namespace-removed: %v", err)
	}
	if fsck.OK || fsck.Namespaces[0].Counts.NamespaceRemoved != 1 {
		t.Fatalf("expected namespace_removed drift: %#v", fsck)
	}
}

func TestServiceHydratePolicyFailsClosedOnMixedRuleActions(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md", ".codex/ledger/**"})
	cfg.Namespaces[0].Hydrate = config.HydratePolicy{
		OnLocalOnly: "prune_to_rescue",
		Rules: []config.HydrateRule{{
			Pattern:     ".codex/ledger/**",
			OnLocalOnly: "keep",
		}},
	}
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	writeFile(t, root, "src/local/SPEC.md", "# Local only\n")
	writeFile(t, root, ".codex/ledger/session.md", "# Ledger\n")

	result, err := service.Hydrate(ctx, root)
	if err == nil {
		t.Fatalf("expected mixed policy error, got result %#v", result)
	}
	assertFile(t, filepath.Join(root, "src/local/SPEC.md"), "# Local only\n")
	assertFile(t, filepath.Join(root, ".codex/ledger/session.md"), "# Ledger\n")
}

func TestServiceUpdateNoGitBlocksAndPrunesLocalOnly(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	writeFile(t, root, "src/local/SPEC.md", "# Local only\n")

	blocked, err := service.Update(ctx, root, sidecar.UpdateOptions{NoGit: true})
	if err != nil {
		t.Fatalf("update blocked: %v", err)
	}
	if blocked.OK || blocked.Hydrate.Plan.Namespaces[0].Counts.LocalOnly != 1 {
		t.Fatalf("expected update to report hydrate drift: %#v", blocked)
	}

	pruned, err := service.Update(ctx, root, sidecar.UpdateOptions{NoGit: true, Reconcile: "prune-local"})
	if err != nil {
		t.Fatalf("update prune: %v", err)
	}
	if !pruned.OK || pruned.Hydrate.Rescue == nil {
		t.Fatalf("expected update prune success with rescue: %#v", pruned)
	}
	if _, err := os.Stat(filepath.Join(root, "src/local/SPEC.md")); !os.IsNotExist(err) {
		t.Fatalf("expected local-only file moved to rescue, stat err=%v", err)
	}
}

func TestServiceHydrateTheirsRescuesLocalModifiedFile(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Base\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	writeFile(t, root, "src/auth/SPEC.md", "# Local\n")

	result, err := service.Hydrate(ctx, root, sidecar.HydrateOptions{Theirs: true})
	if err != nil {
		t.Fatalf("hydrate theirs: %v", err)
	}
	if !result.OK || result.Rescue == nil || len(result.Rescue.Files) != 1 {
		t.Fatalf("expected successful theirs hydrate with rescue: %#v", result)
	}
	assertFile(t, filepath.Join(root, "src/auth/SPEC.md"), "# Base\n")
	restored, err := service.RescueRestore(
		ctx,
		root,
		result.Rescue.ID,
		[]string{"src/auth/SPEC.md"},
		sidecar.RescueRestoreOptions{Overwrite: true},
	)
	if err != nil {
		t.Fatalf("restore rescued local: %v", err)
	}
	if len(restored.Files) != 1 {
		t.Fatalf("expected rescued local file: %#v", restored)
	}
	assertFile(t, filepath.Join(root, "src/auth/SPEC.md"), "# Local\n")
}

func TestServiceHydrateOursKeepsLocalModifiedAndRestoresMissing(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth base\n")
	writeFile(t, root, "src/billing/SPEC.md", "# Billing base\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	writeFile(t, root, "src/auth/SPEC.md", "# Auth local\n")
	if err := os.Remove(filepath.Join(root, "src/billing/SPEC.md")); err != nil {
		t.Fatalf("remove billing spec: %v", err)
	}

	result, err := service.Hydrate(ctx, root, sidecar.HydrateOptions{Ours: true})
	if err != nil {
		t.Fatalf("hydrate ours: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected ours hydrate to adopt local changes: %#v", result)
	}
	assertFile(t, filepath.Join(root, "src/auth/SPEC.md"), "# Auth local\n")
	if _, err := os.Stat(filepath.Join(root, "src/billing/SPEC.md")); !os.IsNotExist(err) {
		t.Fatalf("expected local billing spec to stay deleted, stat err=%v", err)
	}
	assertSidecarFile(t, remote, "project/__branches__/main", "project/src/auth/SPEC.md", "# Auth local\n")
	assertSidecarMissing(t, remote, "project/__branches__/main", "project/src/billing/SPEC.md")
}

func TestServiceHydrateMergeMaterializesConflictMarkers(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "title: base\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync base: %v", err)
	}
	journalPath := filepath.Join(root, ".git", "skeeper", "hydration.json")
	baseJournal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read base hydration journal: %v", err)
	}

	writeFile(t, root, "src/auth/SPEC.md", "title: sidecar\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync sidecar change: %v", err)
	}
	if err := os.WriteFile(journalPath, baseJournal, 0o600); err != nil {
		t.Fatalf("restore base hydration journal: %v", err)
	}
	writeFile(t, root, "src/auth/SPEC.md", "title: local\n")

	result, err := service.Hydrate(ctx, root, sidecar.HydrateOptions{Merge: true})
	if err != nil {
		t.Fatalf("hydrate merge conflict: %v", err)
	}
	if result.OK || result.FSCKAfter == nil || result.FSCKAfter.OK {
		t.Fatalf("expected unresolved merge markers to keep fsck false: %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "src/auth/SPEC.md"))
	if err != nil {
		t.Fatalf("read merged file: %v", err)
	}
	merged := string(data)
	for _, want := range []string{"<<<<<<<", "title: local", "=======", "title: sidecar", ">>>>>>>"} {
		if !strings.Contains(merged, want) {
			t.Fatalf("merged file missing %q:\n%s", want, merged)
		}
	}
}

func TestServiceVerifyHookDoesNotCreateMissingSidecarClone(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, sidecar.DirName)); err != nil {
		t.Fatalf("remove sidecar clone: %v", err)
	}

	verify, err := service.Verify(ctx, root, sidecar.VerifyOptions{Hook: true})
	if err != nil {
		t.Fatalf("verify hook: %v", err)
	}
	if verify.OK || len(verify.Diagnostics) == 0 || verify.Diagnostics[0].Code != "lock.clone_missing" {
		t.Fatalf("expected clone-missing diagnostic: %#v", verify)
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName)); !os.IsNotExist(err) {
		t.Fatalf("verify hook must not create sidecar clone, stat err=%v", err)
	}
}

func TestServiceAdoptPushesSidecarBeforeRemovingMainIndexTracking(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	git(t, root, "add", "README.md", config.Filename, "src/auth/SPEC.md")
	git(t, root, "commit", "-m", "bootstrap tracked spec")

	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.Adopt(ctx, root, []string{"src/auth/SPEC.md"}, sidecar.MutateOptions{})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if len(result.Changed) != 1 || result.Changed[0].Path != "src/auth/SPEC.md" {
		t.Fatalf("unexpected adopt result: %#v", result)
	}
	if out := gitOutput(t, root, "ls-files", "--", "src/auth/SPEC.md"); out != "" {
		t.Fatalf("expected spec removed from main index, got %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("expected working file to remain: %v", err)
	}
	assertSidecarFile(t, remote, "project/__branches__/main", "project/src/auth/SPEC.md", "# Auth\n")
}

func TestServicePatternAddUpdatesConfigAndGitignore(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"docs/specs/**"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.PatternAdd(ctx, root, "src/**/SPEC.md", sidecar.PatternAddOptions{})
	if err != nil {
		t.Fatalf("pattern add: %v", err)
	}
	if result.ConfigPath == "" || result.Gitignore == "" {
		t.Fatalf("unexpected pattern result: %#v", result)
	}
	reloaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !slices.Contains(reloaded.Namespaces[0].Patterns, "src/**/SPEC.md") {
		t.Fatalf("expected config to include added pattern: %#v", reloaded.Namespaces[0].Patterns)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if !strings.Contains(string(data), "src/**/SPEC.md") {
		t.Fatalf("expected gitignore to include added pattern:\n%s", string(data))
	}
}

func TestServiceTrackAddsGlobAndCanSyncExistingSpecs(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"docs/specs/**"})
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	git(t, root, "add", "-f", "src/auth/SPEC.md")
	git(t, root, "commit", "-m", "tracked spec before skeeper")

	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.Track(ctx, root, "src/**/SPEC.md", sidecar.TrackOptions{Sync: true})
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	if !result.Synced || result.ConfigPath == "" || result.Gitignore == "" {
		t.Fatalf("unexpected track result: %#v", result)
	}
	reloaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !slices.Contains(reloaded.Namespaces[0].Patterns, "src/**/SPEC.md") {
		t.Fatalf("expected config to include tracked glob: %#v", reloaded.Namespaces[0].Patterns)
	}
	if out := gitOutput(t, root, "ls-files", "--", "src/auth/SPEC.md"); out != "" {
		t.Fatalf("expected synced track to remove spec from main index, got %q", out)
	}
	assertSidecarFile(t, remote, "project/__branches__/main", "project/src/auth/SPEC.md", "# Auth\n")
}

func TestServiceRestorePathRestoresLockedContentWithRescue(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	writeFile(t, root, "src/auth/SPEC.md", "# Local drift\n")

	result, err := service.Restore(ctx, root, sidecar.RestoreOptions{Paths: []string{"src/auth/SPEC.md"}})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !result.OK || len(result.Restored) != 1 || result.Rescue == nil || len(result.Rescue.Files) != 1 {
		t.Fatalf("unexpected restore result: %#v", result)
	}
	assertFile(t, filepath.Join(root, "src/auth/SPEC.md"), "# Auth\n")
}

func TestServiceStatusCheckReportsNextActionForDrift(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := service.HooksInstall(ctx, root); err != nil {
		t.Fatalf("install hooks: %v", err)
	}
	writeFile(t, root, "src/auth/SPEC.md", "# Local drift\n")

	status, err := service.Status(ctx, root, sidecar.StatusOptions{Check: true, Paths: true})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.OK || !strings.Contains(status.NextAction, "skeeper sync") {
		t.Fatalf("expected sync next action for drift: %#v", status)
	}
	if len(status.Namespaces) != 1 || len(status.Namespaces[0].Paths) == 0 {
		t.Fatalf("expected path-level drift: %#v", status.Namespaces)
	}
}

func TestServiceRepairRefreshesHooksAndStopsOnDrift(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	repaired, err := service.Repair(ctx, root, sidecar.RepairOptions{})
	if err != nil {
		t.Fatalf("repair hooks: %v", err)
	}
	if !repaired.OK || len(repaired.Actions) == 0 || repaired.Actions[0].Kind != "hooks_refreshed" {
		t.Fatalf("expected hook repair action: %#v", repaired)
	}

	writeFile(t, root, "src/auth/SPEC.md", "# Local drift\n")
	blocked, err := service.Repair(ctx, root, sidecar.RepairOptions{Check: true})
	if err != nil {
		t.Fatalf("repair check drift: %v", err)
	}
	if blocked.OK || len(blocked.Diagnostics) == 0 {
		t.Fatalf("expected repair to stop on ambiguous drift: %#v", blocked)
	}
}

func TestServiceMergeDriverWritesCanonicalLockToCurrentPath(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	rootLock := readFile(t, filepath.Join(root, "skeeper.lock"))
	writeFile(t, root, "merge/base.lock", rootLock)
	writeFile(t, root, "merge/current.lock", rootLock)
	writeFile(t, root, "merge/other.lock", rootLock)

	result, err := service.MergeDriver(ctx, root, sidecar.MergeDriverOptions{
		BasePath:    "merge/base.lock",
		CurrentPath: "merge/current.lock",
		OtherPath:   "merge/other.lock",
	})
	if err != nil {
		t.Fatalf("merge driver: %v", err)
	}
	if !samePath(t, result.OutputPath, filepath.Join(root, "merge/current.lock")) {
		t.Fatalf("unexpected merge output path: %#v", result)
	}
	current := readFile(t, filepath.Join(root, "merge/current.lock"))
	if !strings.Contains(current, `"version": 1`) || strings.Contains(current, "<<<<<<<") {
		t.Fatalf("merge driver did not write canonical lock:\n%s", current)
	}
	if current != rootLock {
		t.Fatalf("merge output must match canonical root lock")
	}
}

func TestServiceMergeDriverMarksChangedLockPending(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	rootLock := readFile(t, filepath.Join(root, "skeeper.lock"))
	writeFile(t, root, "merge/current.lock", rootLock)
	writeFile(t, root, "src/auth/SPEC.md", "# Merged\n")

	if _, err := service.MergeDriver(ctx, root, sidecar.MergeDriverOptions{
		CurrentPath: "merge/current.lock",
	}); err != nil {
		t.Fatalf("merge driver: %v", err)
	}
	data := readFile(t, filepath.Join(root, "merge/current.lock"))
	var merged lockfile.Lock
	if err := json.Unmarshal([]byte(data), &merged); err != nil {
		t.Fatalf("decode merge output: %v", err)
	}
	if len(merged.Namespaces) != 1 || merged.Namespaces[0].Commit != "0000000000000000000000000000000000000000" {
		t.Fatalf("expected pending commit marker, got %#v", merged.Namespaces)
	}
	if err := lockfile.Validate(merged); err == nil {
		t.Fatal("expected pending merge-driver lock to fail validation before hook sync")
	}
}

func TestServiceRecordBypassUsesConfiguredEnvName(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	cfg := singleNamespaceConfig(newBareRepo(t), "project", []string{"**/SPEC.md"})
	cfg.Settings.Hooks.AllowSkipEnv = "MY_SKIP"
	bootstrapRepo(t, root, cfg)

	service := sidecar.New(&gitexec.ExecRunner{})
	if err := service.RecordBypass(ctx, root, "custom bypass"); err != nil {
		t.Fatalf("record bypass: %v", err)
	}
	repair, err := service.RepairStatus(ctx, root)
	if err != nil {
		t.Fatalf("repair status: %v", err)
	}
	if repair.Bypass == nil || repair.Bypass.Env != "MY_SKIP" {
		t.Fatalf("expected configured bypass env, got %#v", repair.Bypass)
	}
}

func TestServiceHookSyncUsesStagedContentForAmend(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Initial\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	git(t, root, "add", "skeeper.lock")
	git(t, root, "commit", "-m", "sync initial lock")

	writeFile(t, root, "src/auth/SPEC.md", "# Amended staged\n")
	git(t, root, "add", "-f", "src/auth/SPEC.md")
	writeFile(t, root, "src/auth/SPEC.md", "# Unstaged worktree\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{Hook: true}); err != nil {
		t.Fatalf("hook sync: %v", err)
	}
	git(t, root, "add", "skeeper.lock")
	git(t, root, "commit", "--amend", "--no-edit")

	assertSidecarFile(t, remote, "project/__branches__/main", "project/src/auth/SPEC.md", "# Amended staged\n")
	if !strings.Contains(gitOutput(t, root, "show", "HEAD:skeeper.lock"), `"version": 1`) {
		t.Fatal("amended commit missing updated skeeper.lock")
	}
}

func TestServiceLockVerifiesAfterRebaseReplay(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	service := sidecar.New(&gitexec.ExecRunner{})

	writeFile(t, root, "src/auth/SPEC.md", "# Initial\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	git(t, root, "add", "skeeper.lock")
	git(t, root, "commit", "-m", "sync initial lock")

	git(t, root, "switch", "-c", "feature/spec-update")
	writeFile(t, root, "src/auth/SPEC.md", "# Feature update\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("feature sync: %v", err)
	}
	git(t, root, "add", "skeeper.lock")
	git(t, root, "commit", "-m", "sync feature lock")

	git(t, root, "switch", "main")
	writeFile(t, root, "README.md", "project\n\nmain change\n")
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-m", "main change")
	git(t, root, "switch", "feature/spec-update")
	git(t, root, "rebase", "main")

	verify, err := service.Verify(ctx, root, sidecar.VerifyOptions{})
	if err != nil {
		t.Fatalf("verify after rebase: %v", err)
	}
	if !verify.OK {
		t.Fatalf("expected rebased lock to verify: %#v", verify)
	}
}

func TestServiceSyncMirrorsDeletes(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename)
	git(t, root, "commit", "-m", "bootstrap")

	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove spec: %v", err)
	}
	result, err := service.Sync(ctx, root, sidecar.SyncOptions{})
	if err != nil {
		t.Fatalf("delete sync: %v", err)
	}
	if !result.Committed {
		t.Fatal("expected delete sync commit")
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName, "project/src/auth/SPEC.md")); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar spec to be removed, stat err=%v", err)
	}
}

func TestServicePullAppliesRemoteDeletesWhenLocalMatchesBase(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	service := sidecar.New(&gitexec.ExecRunner{})
	repoA := newMainRepo(t)
	repoB := newMainRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	bootstrapRepo(t, repoA, cfg)
	bootstrapRepo(t, repoB, cfg)

	writeFile(t, repoA, "src/auth/SPEC.md", "# Auth\n")
	if _, err := service.Sync(ctx, repoA, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync repo A: %v", err)
	}
	lockData, err := os.ReadFile(filepath.Join(repoA, lockfile.Filename))
	if err != nil {
		t.Fatalf("read repo A lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoB, lockfile.Filename), lockData, 0o644); err != nil {
		t.Fatalf("write repo B lock: %v", err)
	}
	writeFile(t, repoB, "src/auth/SPEC.md", "# Auth\n")
	if _, err := service.Hydrate(ctx, repoB); err != nil {
		t.Fatalf("hydrate repo B base: %v", err)
	}

	if err := os.Remove(filepath.Join(repoA, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove repo A spec: %v", err)
	}
	if _, err := service.Sync(ctx, repoA, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync repo A delete: %v", err)
	}

	pulled, err := service.Pull(ctx, repoB, sidecar.PullOptions{NoGit: true})
	if err != nil {
		t.Fatalf("pull repo B: %v", err)
	}
	if !pulled.OK || !pulled.LockUpdated {
		t.Fatalf("expected pull to apply remote delete and update lock: %#v", pulled)
	}
	if _, err := os.Stat(filepath.Join(repoB, "src/auth/SPEC.md")); !os.IsNotExist(err) {
		t.Fatalf("expected repo B spec removed by pull, stat err=%v", err)
	}
}

func TestServicePullRejectsRemoteTipRewind(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	root := newMainRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Auth v1\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	firstTip := gitOutput(t, "", "--git-dir", remote, "rev-parse", "project/__branches__/main")

	writeFile(t, root, "src/auth/SPEC.md", "# Auth v2\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	git(t, "", "--git-dir", remote, "update-ref", "refs/heads/project/__branches__/main", firstTip)

	_, err := service.Pull(ctx, root, sidecar.PullOptions{NoGit: true})
	if err == nil || !strings.Contains(err.Error(), "not a fast-forward") {
		t.Fatalf("expected remote rewind rejection, got %v", err)
	}
	assertFile(t, filepath.Join(root, "src/auth/SPEC.md"), "# Auth v2\n")
}

func TestServiceDiffIgnoresHydrationJournalWithoutSourceBranch(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	root := newMainRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	store := state.New(filepath.Join(root, ".git", "skeeper"))
	journal, ok, err := store.LoadHydration(ctx)
	if err != nil || !ok {
		t.Fatalf("load hydration: ok=%v err=%v", ok, err)
	}
	journal.SourceBranch = ""
	if err := store.WriteHydration(ctx, journal); err != nil {
		t.Fatalf("write legacy hydration: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove spec: %v", err)
	}

	summary, err := service.Diff(ctx, root, sidecar.DiffOptions{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(summary.Namespaces) != 1 || len(summary.Namespaces[0].Paths) != 1 {
		t.Fatalf("unexpected diff summary: %#v", summary)
	}
	path := summary.Namespaces[0].Paths[0]
	if path.Class != reconcile.PathMissingLocal {
		t.Fatalf("expected legacy branchless base to be ignored, got %#v", path)
	}
}

func TestServiceSyncUsesMultipleNamespacesAndSidecarBranches(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	service := sidecar.New(&gitexec.ExecRunner{})
	repoA := newMainRepo(t)
	repoB := newMainRepo(t)

	bootstrapRepo(t, repoA, config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "skills", Patterns: []string{"skills/*.md"}},
			{Name: "repo", Patterns: []string{"**/*.md"}, Exclude: []string{"skills/*.md"}},
		},
	})
	bootstrapRepo(t, repoB, singleNamespaceConfig(remote, "repo-b", []string{"**/SPEC.md"}))

	writeFile(t, repoA, "skills/review.md", "# Skill\n")
	writeFile(t, repoA, "src/auth/SPEC.md", "# Repo A\n")
	if _, err := service.Sync(ctx, repoA, sidecar.SyncOptions{Mirror: true}); err != nil {
		t.Fatalf("sync repo A: %v", err)
	}
	writeFile(t, repoB, "src/auth/SPEC.md", "# Repo B\n")
	if _, err := service.Sync(ctx, repoB, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync repo B: %v", err)
	}

	assertSidecarFile(t, remote, "skills/__branches__/main", "skills/skills/review.md", "# Skill\n")
	assertSidecarFile(t, remote, "repo/__branches__/main", "repo/src/auth/SPEC.md", "# Repo A\n")
	assertSidecarFile(t, remote, "repo-b/__branches__/main", "repo-b/src/auth/SPEC.md", "# Repo B\n")

	status, err := service.Status(ctx, repoA)
	if err != nil {
		t.Fatalf("status repo A: %v", err)
	}
	if len(status.Namespaces) != 2 || status.Namespaces[0].Name != "skills" ||
		status.Namespaces[0].Branch != "skills/__branches__/main" {
		t.Fatalf("unexpected namespaced status: %#v", status)
	}

	logOutput, err := service.Log(ctx, repoA, "src/auth/SPEC.md")
	if err != nil {
		t.Fatalf("log repo A: %v", err)
	}
	if !strings.Contains(logOutput, "sync namespace repo") {
		t.Fatalf("expected repo A sidecar history, got %q", logOutput)
	}

	if err := os.Remove(filepath.Join(repoA, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove repo A spec: %v", err)
	}
	if _, err := service.Sync(ctx, repoA, sidecar.SyncOptions{Mirror: true}); err != nil {
		t.Fatalf("delete sync repo A: %v", err)
	}
	assertSidecarMissing(t, remote, "repo/__branches__/main", "repo/src/auth/SPEC.md")
	assertSidecarFile(t, remote, "repo-b/__branches__/main", "repo-b/src/auth/SPEC.md", "# Repo B\n")

	if err := os.RemoveAll(filepath.Join(repoB, sidecar.DirName)); err != nil {
		t.Fatalf("remove repo B sidecar clone: %v", err)
	}
	if err := os.Remove(filepath.Join(repoB, "src/auth/SPEC.md")); err != nil {
		t.Fatalf("remove repo B spec: %v", err)
	}
	if err := os.Remove(filepath.Join(repoB, ".git", "skeeper", "hydration.json")); err != nil {
		t.Fatalf("remove repo B hydration journal: %v", err)
	}
	hydrated, err := service.Hydrate(ctx, repoB)
	if err != nil {
		t.Fatalf("hydrate repo B: %v", err)
	}
	if len(hydrated.Restored) != 1 || hydrated.Restored[0] != "src/auth/SPEC.md" {
		t.Fatalf("unexpected hydrated files: %#v", hydrated)
	}
	assertFile(t, filepath.Join(repoB, "src/auth/SPEC.md"), "# Repo B\n")
}

func TestServiceSyncRejectsOverlappingNamespaceOwnership(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "docs", Patterns: []string{"docs/**"}},
			{Name: "specs", Patterns: []string{"docs/**/*.md"}},
		},
	})
	writeFile(t, root, "docs/auth/SPEC.md", "# Auth\n")

	_, err := sidecar.New(&gitexec.ExecRunner{}).Sync(ctx, root, sidecar.SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "multiple skeeper namespaces") {
		t.Fatalf("expected namespace overlap error, got %v", err)
	}
}

func TestServiceSyncMovesFileWhenNamespaceExcludeChangesOwnership(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "repo", []string{"**/*.md"}))
	writeFile(t, root, "skills/review.md", "# Skill\n")
	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{Mirror: true}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	assertSidecarFile(t, remote, "repo/__branches__/main", "repo/skills/review.md", "# Skill\n")

	cfg := config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "skills", Patterns: []string{"skills/*.md"}},
			{Name: "repo", Patterns: []string{"**/*.md"}, Exclude: []string{"skills/*.md"}},
		},
	}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save updated config: %v", err)
	}
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{Mirror: true}); err != nil {
		t.Fatalf("ownership migration sync: %v", err)
	}
	assertSidecarMissing(t, remote, "repo/__branches__/main", "repo/skills/review.md")
	assertSidecarFile(t, remote, "skills/__branches__/main", "skills/skills/review.md", "# Skill\n")
}

func TestServiceSyncRequiresRepairAfterFailedPushBeforeRetry(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "repo", Patterns: []string{"docs/*.md"}},
			{Name: "skills", Patterns: []string{"skills/*.md"}},
		},
	})
	service := sidecar.New(&gitexec.ExecRunner{})
	writeFile(t, root, "skills/review.md", "# Skill\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	assertSidecarFile(t, remote, "skills/__branches__/main", "skills/skills/review.md", "# Skill\n")

	sidecarDir := filepath.Join(root, sidecar.DirName)
	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	git(t, sidecarDir, "remote", "set-url", "origin", missingRemote)
	writeFile(t, root, "docs/SPEC.md", "# Repo\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err == nil {
		t.Fatal("expected sync to fail while pushing repo namespace")
	}
	repair, err := service.RepairStatus(ctx, root)
	if err != nil {
		t.Fatalf("repair status: %v", err)
	}
	if repair.Transaction != nil {
		t.Fatalf("expected preflight failure to avoid transaction journal, got %#v", repair.Transaction)
	}

	git(t, sidecarDir, "remote", "set-url", "origin", remote)
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("retry sync: %v", err)
	}
	assertSidecarFile(t, remote, "repo/__branches__/main", "repo/docs/SPEC.md", "# Repo\n")
	assertSidecarFile(t, remote, "skills/__branches__/main", "skills/skills/review.md", "# Skill\n")
	assertSidecarMissing(t, remote, "skills/__branches__/main", "repo/docs/SPEC.md")
}

func TestServiceRepairResumeRejectsConfigDrift(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "repo", []string{"docs/*.md"}))
	writeFile(t, root, "docs/current.md", "# Current\n")

	store := state.New(filepath.Join(root, ".git", "skeeper"))
	if err := store.Begin(ctx, state.Transaction{
		ID:         "tx-1",
		Kind:       "sync",
		Root:       root,
		Namespaces: []string{"repo"},
		Targets:    []string{"docs/previous.md"},
	}); err != nil {
		t.Fatalf("begin transaction: %v", err)
	}

	_, err := sidecar.New(&gitexec.ExecRunner{}).RepairResume(ctx, root)
	if err == nil || !strings.Contains(err.Error(), "config no longer matches recorded transaction") {
		t.Fatalf("expected config drift rejection, got %v", err)
	}
}

func TestServiceHookSyncBlocksOnNamespaceSpecificFailure(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	bootstrapRepo(t, root, config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "repo", Patterns: []string{"docs/*.md"}},
			{Name: "skills", Patterns: []string{"skills/*.md"}},
		},
	})
	service := sidecar.New(&gitexec.ExecRunner{})
	writeFile(t, root, "skills/review.md", "# Skill\n")
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	sidecarDir := filepath.Join(root, sidecar.DirName)
	git(t, sidecarDir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	writeFile(t, root, "docs/SPEC.md", "# Repo\n")
	_, err := service.Sync(ctx, root, sidecar.SyncOptions{Hook: true})
	if err == nil {
		t.Fatal("expected strict hook sync to fail")
	}
	if !strings.Contains(err.Error(), "repo") || !strings.Contains(err.Error(), "fetch sidecar origin") {
		t.Fatalf("expected namespace-specific strict failure, got %v", err)
	}
}

func TestServicePushRejectsStaleRemoteAndSyncWorkflowPulls(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	root := newMainRepo(t)
	cfg := config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{
			{Name: "repo-a", Patterns: []string{"**/SPEC.md"}},
		},
	}
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	external := filepath.Join(t.TempDir(), "external")
	git(t, "", "clone", remote, external)
	git(t, external, "switch", "--track", "origin/repo-a/__branches__/main")
	writeFile(t, external, "repo-a/external/SPEC.md", "# External\n")
	git(t, external, "add", "repo-a/external/SPEC.md")
	git(t, external, "commit", "-m", "external sidecar update")
	git(t, external, "push", "origin", "repo-a/__branches__/main")

	_, err := service.Push(ctx, root, sidecar.SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "run `skeeper pull` before `skeeper push`") {
		t.Fatalf("expected stale push rejection, got %v", err)
	}
	result, err := service.SyncWorkflow(ctx, root, sidecar.SyncOptions{})
	if err != nil {
		t.Fatalf("sync workflow: %v", err)
	}
	if result.Push.Committed {
		t.Fatal("expected sync workflow to converge without a deletion commit")
	}
	assertFile(t, filepath.Join(root, "external/SPEC.md"), "# External\n")
	assertSidecarFile(t, remote, "repo-a/__branches__/main", "repo-a/external/SPEC.md", "# External\n")
}

func TestServiceSyncWorkflowCreatesInitialLockWhenMissing(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	root := newMainRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	git(t, root, "add", "src/auth/SPEC.md")
	git(t, root, "commit", "-m", "bootstrap specs")

	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.SyncWorkflow(ctx, root, sidecar.SyncOptions{})
	if err != nil {
		t.Fatalf("sync workflow: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected initial sync to be ok: %#v", result)
	}
	if !result.Push.Committed || result.Push.ChangedFiles != 1 {
		t.Fatalf("expected initial sync to push one file: %#v", result.Push)
	}
	if _, err := os.Stat(filepath.Join(root, "skeeper.lock")); err != nil {
		t.Fatalf("expected initial sync to write skeeper.lock: %v", err)
	}
	assertSidecarFile(t, remote, "project/__branches__/main", "project/src/auth/SPEC.md", "# Auth\n")
}

func TestServiceSyncWorkflowRejectsMissingLockWhenRemoteBranchExists(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	seed := newMainRepo(t)
	bootstrapRepo(t, seed, cfg)
	writeFile(t, seed, "src/auth/SPEC.md", "# Seed\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, seed, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}

	root := newMainRepo(t)
	bootstrapRepo(t, root, cfg)
	writeFile(t, root, "src/auth/SPEC.md", "# Local\n")
	git(t, root, "add", "src/auth/SPEC.md")
	git(t, root, "commit", "-m", "bootstrap local specs")

	_, err := service.SyncWorkflow(ctx, root, sidecar.SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "restore skeeper.lock before pushing") {
		t.Fatalf("expected missing-lock remote-base rejection, got %v", err)
	}
	assertSidecarFile(t, remote, "project/__branches__/main", "project/src/auth/SPEC.md", "# Seed\n")
}

func TestServicePushRejectsUnexpectedLocalSidecarCommits(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	remote := newBareRepo(t)
	root := newMainRepo(t)
	bootstrapRepo(t, root, singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"}))
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	sidecarDir := filepath.Join(root, sidecar.DirName)
	writeFile(t, sidecarDir, "project/rogue/SPEC.md", "# Rogue\n")
	git(t, sidecarDir, "add", "project/rogue/SPEC.md")
	git(t, sidecarDir, "commit", "-m", "rogue local sidecar commit")
	writeFile(t, root, "src/auth/SPEC.md", "# Auth v2\n")

	_, err := service.Push(ctx, root, sidecar.SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "local commits outside expected push base") {
		t.Fatalf("expected local sidecar commit rejection, got %v", err)
	}
	assertSidecarMissing(t, remote, "project/__branches__/main", "project/rogue/SPEC.md")
}

func TestServiceStatusReportsRemoteState(t *testing.T) {
	setGitIdentity(t)

	tests := []struct {
		name  string
		want  string
		setup func(*testing.T, statusFixture)
	}{
		{
			name: "not pushed",
			want: "not pushed",
		},
		{
			name: "in sync",
			want: "in sync",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				git(t, fixture.sidecarDir, "push", "-u", "origin", "project/__branches__/main")
			},
		},
		{
			name: "ahead",
			want: "ahead by 1 commit(s)",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				git(t, fixture.sidecarDir, "push", "-u", "origin", "project/__branches__/main")
				commitSidecarFile(t, fixture.sidecarDir, "local/SPEC.md", "# Local\n", "local sidecar update")
			},
		},
		{
			name: "behind",
			want: "behind by 1 commit(s)",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				git(t, fixture.sidecarDir, "push", "-u", "origin", "project/__branches__/main")
				base := gitOutput(t, fixture.sidecarDir, "rev-parse", "HEAD")
				remoteCommit := commitFromCurrentTree(t, fixture.sidecarDir, base, "remote sidecar update")
				git(t, fixture.sidecarDir, "push", "origin", remoteCommit+":refs/heads/project/__branches__/main")
			},
		},
		{
			name: "diverged",
			want: "diverged (ahead 1, behind 1)",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				git(t, fixture.sidecarDir, "push", "-u", "origin", "project/__branches__/main")
				base := gitOutput(t, fixture.sidecarDir, "rev-parse", "HEAD")
				commitSidecarFile(t, fixture.sidecarDir, "local/SPEC.md", "# Local\n", "local sidecar update")
				remoteCommit := commitFromCurrentTree(t, fixture.sidecarDir, base, "remote sidecar update")
				git(t, fixture.sidecarDir, "push", "origin", remoteCommit+":refs/heads/project/__branches__/main")
			},
		},
		{
			name: "unknown fetch failure",
			want: "unknown",
			setup: func(t *testing.T, fixture statusFixture) {
				t.Helper()
				missingRemote := filepath.Join(t.TempDir(), "missing.git")
				git(t, fixture.sidecarDir, "remote", "set-url", "origin", missingRemote)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newStatusFixture(t)
			if tt.setup != nil {
				tt.setup(t, fixture)
			}
			status, err := fixture.service.Status(context.Background(), fixture.root)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if len(status.Namespaces) != 1 {
				t.Fatalf("expected one namespace status, got %#v", status.Namespaces)
			}
			if status.Namespaces[0].Remote != tt.want {
				t.Fatalf("remote state mismatch: got %q want %q", status.Namespaces[0].Remote, tt.want)
			}
		})
	}
}

func TestServiceHookSyncReturnsCloneFailure(t *testing.T) {
	root := newMainRepo(t)
	cfg := singleNamespaceConfig(filepath.Join(t.TempDir(), "missing.git"), "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	_, err := service.Sync(context.Background(), root, sidecar.SyncOptions{Hook: true})
	if err == nil {
		t.Fatal("expected strict hook sync to fail")
	}
	if !strings.Contains(err.Error(), "clone sidecar") {
		t.Fatalf("expected clone failure, got %v", err)
	}
}

func singleNamespaceConfig(sidecarURL, name string, patterns []string) config.Config {
	return config.Config{
		Sidecar: sidecarURL,
		Namespaces: []config.Namespace{
			{Name: name, Patterns: patterns},
		},
	}
}

func bootstrapRepo(t *testing.T, root string, cfg config.Config) {
	t.Helper()
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename)
	git(t, root, "commit", "-m", "bootstrap")
}

func assertSidecarFile(t *testing.T, remote, branch, path, want string) {
	t.Helper()
	got := gitOutput(t, "", "--git-dir", remote, "show", branch+":"+path)
	want = strings.TrimSuffix(want, "\n")
	if got != want {
		t.Fatalf("sidecar file %s:%s mismatch: got %q want %q", branch, path, got, want)
	}
}

func assertSidecarMissing(t *testing.T, remote, branch, path string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "--git-dir", remote, "show", branch+":"+path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected %s:%s to be absent, got %q", branch, path, string(out))
	}
}

func TestServiceHookSyncReportsStateWriteFailure(t *testing.T) {
	root := newMainRepo(t)
	cfg := singleNamespaceConfig(filepath.Join(t.TempDir(), "missing.git"), "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "skeeper"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write state path blocker: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	_, err := service.Sync(context.Background(), root, sidecar.SyncOptions{Hook: true})
	if err == nil {
		t.Fatal("expected strict hook sync to return state error")
	}
	if !strings.Contains(err.Error(), "transaction") && !strings.Contains(err.Error(), "skeeper") {
		t.Fatalf("expected state failure, got %v", err)
	}
}

func TestServiceInitUsesExistingCompatibleConfigIdempotently(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := config.Config{
		Sidecar:   remote,
		Bootstrap: "brew install skeeper",
		Namespaces: []config.Namespace{
			{Name: "project", Patterns: []string{"**/SPEC.md"}},
		},
	}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	result, err := service.Init(ctx, root, sidecar.InitOptions{
		SidecarName: "sidecar",
		Bootstrap:   "brew install skeeper",
		Patterns:    []string{"**/SPEC.md"},
	})
	if err != nil {
		t.Fatalf("idempotent init: %v", err)
	}
	if result.Config.Sidecar != remote {
		t.Fatalf("expected existing sidecar to remain %q, got %q", remote, result.Config.Sidecar)
	}
	reloaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.Sidecar != cfg.Sidecar || reloaded.Bootstrap != cfg.Bootstrap ||
		len(reloaded.Namespaces) != 1 ||
		!sameStrings(reloaded.Namespaces[0].Patterns, cfg.Namespaces[0].Patterns) {
		t.Fatalf("config changed unexpectedly: %#v", reloaded)
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName, ".git")); err != nil {
		t.Fatalf("expected sidecar clone to exist: %v", err)
	}
	hook, err := os.ReadFile(filepath.Join(root, ".git", "hooks", "pre-commit"))
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if !strings.Contains(string(hook), "skeeper internal pre-commit") {
		t.Fatalf("expected hook to be installed, got %s", string(hook))
	}
}

func TestServiceInitUsesExistingSidecarURLAndDefaultNamespace(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)

	result, err := sidecar.New(&gitexec.ExecRunner{}).Init(ctx, root, sidecar.InitOptions{
		Sidecar:  remote,
		Patterns: []string{"**/SPEC.md"},
	})
	if err != nil {
		t.Fatalf("init with existing sidecar URL: %v", err)
	}
	wantNamespace := sidecar.DefaultNamespace(filepath.Base(root))
	if result.Config.Sidecar != remote || len(result.Config.Namespaces) != 1 ||
		result.Config.Namespaces[0].Name != wantNamespace {
		t.Fatalf("unexpected config: %#v", result.Config)
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName, ".git")); err != nil {
		t.Fatalf("expected sidecar clone: %v", err)
	}
	reloaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(reloaded.Namespaces) != 1 || reloaded.Namespaces[0].Name != wantNamespace {
		t.Fatalf("expected namespace %q, got %#v", wantNamespace, reloaded.Namespaces)
	}
}

func TestServiceInitRejectsInvalidPatternsBeforeSideEffects(t *testing.T) {
	root := newMainRepo(t)
	remote := filepath.Join(t.TempDir(), "shared-specs.git")

	_, err := sidecar.New(&gitexec.ExecRunner{}).Init(context.Background(), root, sidecar.InitOptions{
		Sidecar:  remote,
		Patterns: []string{"["},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid glob") {
		t.Fatalf("expected invalid glob error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, config.Filename)); !os.IsNotExist(statErr) {
		t.Fatalf("expected config not to be written, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, sidecar.DirName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected sidecar clone not to be created, stat err=%v", statErr)
	}
}

func TestServiceInitRejectsIncompatibleExistingConfig(t *testing.T) {
	root := newMainRepo(t)
	cfg := singleNamespaceConfig(filepath.Join(t.TempDir(), "sidecar.git"), "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	_, err := service.Init(context.Background(), root, sidecar.InitOptions{
		SidecarName: "other-specs",
		Patterns:    []string{"docs/**"},
	})
	if err == nil {
		t.Fatal("expected incompatible config error, got nil")
	}
	if !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("expected incompatible config error, got %v", err)
	}
	got, err := config.Load(root)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got.Sidecar != cfg.Sidecar || len(got.Namespaces) != 1 ||
		got.Namespaces[0].Patterns[0] != cfg.Namespaces[0].Patterns[0] {
		t.Fatalf("config was modified: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(root, sidecar.DirName)); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar dir not to be created, stat err=%v", err)
	}
}

func TestServiceStatusDoesNotRequireCloneAndLogClones(t *testing.T) {
	root := newMainRepo(t)
	cfg := singleNamespaceConfig(filepath.Join(t.TempDir(), "sidecar.git"), "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Status(context.Background(), root); err != nil {
		t.Fatalf("status should report config state without sidecar clone: %v", err)
	}
	if _, err := service.Log(
		context.Background(),
		root,
		"src/auth/SPEC.md",
	); err == nil ||
		!strings.Contains(err.Error(), "clone sidecar") {
		t.Fatalf("expected log to attempt sidecar clone, got %v", err)
	}
}

func TestServiceLogRejectsPathOutsideProjectRoot(t *testing.T) {
	setGitIdentity(t)

	ctx := context.Background()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename)
	git(t, root, "commit", "-m", "bootstrap")
	if _, err := sidecar.New(&gitexec.ExecRunner{}).Sync(ctx, root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	_, err := sidecar.New(&gitexec.ExecRunner{}).Log(ctx, root, "../outside/SPEC.md")
	if err == nil {
		t.Fatal("expected path traversal error, got nil")
	}
	if !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("expected outside-root error, got %v", err)
	}
}

type statusFixture struct {
	root       string
	remote     string
	sidecarDir string
	service    *sidecar.Service
}

func newStatusFixture(t *testing.T) statusFixture {
	t.Helper()
	root := newMainRepo(t)
	remote := newBareRepo(t)
	cfg := singleNamespaceConfig(remote, "project", []string{"**/SPEC.md"})
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeFile(t, root, "README.md", "project\n")
	git(t, root, "add", "README.md", config.Filename)
	git(t, root, "commit", "-m", "bootstrap")

	sidecarDir := filepath.Join(root, sidecar.DirName)
	git(t, "", "init", "-b", "main", sidecarDir)
	git(t, sidecarDir, "remote", "add", "origin", remote)
	git(t, sidecarDir, "switch", "-c", "project/__branches__/main")
	commitSidecarFile(t, sidecarDir, "project/src/auth/SPEC.md", "# Auth\n", "initial sidecar sync")
	return statusFixture{
		root:       root,
		remote:     remote,
		sidecarDir: sidecarDir,
		service:    sidecar.New(&gitexec.ExecRunner{}),
	}
}

func commitSidecarFile(t *testing.T, root, rel, content, message string) string {
	t.Helper()
	writeFile(t, root, rel, content)
	git(t, root, "add", rel)
	git(t, root, "commit", "-m", message)
	return gitOutput(t, root, "rev-parse", "HEAD")
}

func commitFromCurrentTree(t *testing.T, root, parent, message string) string {
	t.Helper()
	tree := gitOutput(t, root, "write-tree")
	return gitOutput(t, root, "commit-tree", tree, "-p", parent, "-m", message)
}

func newMainRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	return root
}

func newBareRepo(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "sidecar.git")
	git(t, "", "init", "--bare", "--initial-branch=main", remote)
	return remote
}

func setGitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "skeeper tests")
	t.Setenv("GIT_AUTHOR_EMAIL", "skeeper@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "skeeper tests")
	t.Setenv("GIT_COMMITTER_EMAIL", "skeeper@example.com")
}

func isolateGitIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.useConfigOnly")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	unsetEnv(t, "EMAIL")
	unsetEnv(t, "GIT_AUTHOR_NAME")
	unsetEnv(t, "GIT_AUTHOR_EMAIL")
	unsetEnv(t, "GIT_COMMITTER_NAME")
	unsetEnv(t, "GIT_COMMITTER_EMAIL")
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	value, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if ok {
			if err := os.Setenv(key, value); err != nil {
				t.Fatalf("restore %s: %v", key, err)
			}
			return
		}
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("restore unset %s: %v", key, err)
		}
	})
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitOutput(t, dir, args...)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data := readFile(t, path)
	if data != want {
		t.Fatalf("file %s mismatch: got %q want %q", path, data, want)
	}
}

func samePath(t *testing.T, left, right string) bool {
	t.Helper()
	leftEval, err := filepath.EvalSymlinks(left)
	if err != nil {
		t.Fatalf("eval %s: %v", left, err)
	}
	rightEval, err := filepath.EvalSymlinks(right)
	if err != nil {
		t.Fatalf("eval %s: %v", right, err)
	}
	return leftEval == rightEval
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
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
