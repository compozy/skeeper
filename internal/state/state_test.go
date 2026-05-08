package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTransactionLifecycleRejectsConcurrentBeginAndWrongIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := New(filepath.Join(t.TempDir(), ".git", "skeeper"))
	tx := Transaction{
		ID:         "tx-1",
		Kind:       "sync",
		Root:       "/repo",
		Targets:    []string{"src/A.md"},
		Namespaces: []string{"project"},
	}

	if err := store.Begin(ctx, tx); err != nil {
		t.Fatalf("begin: %v", err)
	}
	assertFileMode(t, filepath.Join(store.dir, transactionFile), 0o600)

	if err := store.Begin(ctx, Transaction{ID: "tx-2", Kind: "sync"}); err == nil {
		t.Fatal("expected concurrent transaction rejection")
	}
	if err := store.MarkPhase(ctx, "tx-2", TransactionPhaseSidecarPushed); err == nil {
		t.Fatal("expected wrong transaction id rejection")
	}
	if err := store.MarkPhase(ctx, "tx-1", TransactionPhase("unknown")); err == nil {
		t.Fatal("expected unknown transaction phase rejection")
	}

	current, ok, err := store.Current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if !ok || current.ID != "tx-1" || current.Phase != TransactionPhasePlanned {
		t.Fatalf("unexpected current transaction: %#v ok=%v", current, ok)
	}
}

func TestTransactionAbortRefusesAfterMainIndexMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := New(filepath.Join(t.TempDir(), ".git", "skeeper"))
	if err := store.Begin(ctx, Transaction{ID: "tx-1", Kind: "sync"}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := store.MarkPhase(ctx, "tx-1", TransactionPhaseMainIndexMutated); err != nil {
		t.Fatalf("mark phase: %v", err)
	}
	if err := store.Abort(ctx, "tx-1"); err == nil {
		t.Fatal("expected abort rejection after main index mutation")
	}

	current, ok, err := store.Current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if !ok || !current.MainIndexMutated {
		t.Fatalf("expected transaction to remain active after refused abort: %#v ok=%v", current, ok)
	}
}

func TestTransactionCompleteNoActiveIsNoop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := New(filepath.Join(t.TempDir(), ".git", "skeeper"))
	if err := store.Complete(ctx, "missing"); err != nil {
		t.Fatalf("complete no-active: %v", err)
	}
	if err := store.Abort(ctx, "missing"); err != nil {
		t.Fatalf("abort no-active: %v", err)
	}
}

func TestBypassRoundTripAndClear(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := New(filepath.Join(t.TempDir(), ".git", "skeeper"))
	want := Bypass{
		Time:    time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		Env:     "SKEEPER_SKIP",
		MainSHA: "0123456789abcdef0123456789abcdef01234567",
		Reason:  "operator override",
	}

	if err := store.RecordBypass(ctx, want); err != nil {
		t.Fatalf("record bypass: %v", err)
	}
	assertFileMode(t, filepath.Join(store.dir, bypassFile), 0o600)

	got, ok, err := store.Bypass(ctx)
	if err != nil {
		t.Fatalf("read bypass: %v", err)
	}
	if !ok || got != want {
		t.Fatalf("bypass mismatch: got %#v ok=%v want %#v", got, ok, want)
	}

	if err := store.ClearBypass(ctx); err != nil {
		t.Fatalf("clear bypass: %v", err)
	}
	_, ok, err = store.Bypass(ctx)
	if err != nil {
		t.Fatalf("read cleared bypass: %v", err)
	}
	if ok {
		t.Fatal("expected bypass to be cleared")
	}
}

func TestHydrationJournalRoundTrip(t *testing.T) {
	t.Parallel()

	store := New(t.TempDir())
	journal := HydrationJournal{
		SourceBranch: "main",
		Namespaces: map[string]HydrationNamespace{
			"repo": {
				LockCommit: "abc123",
				Files: map[string]HydrationFile{
					"docs/SPEC.md": {SidecarBlob: "blob123", SHA256: "hash", Size: 4},
				},
			},
		},
	}
	if err := store.WriteHydration(context.Background(), journal); err != nil {
		t.Fatalf("write hydration: %v", err)
	}
	got, ok, err := store.LoadHydration(context.Background())
	if err != nil {
		t.Fatalf("load hydration: %v", err)
	}
	if !ok || got.Version != 1 || got.SourceBranch != "main" {
		t.Fatalf("unexpected journal: %#v ok=%v", got, ok)
	}
	if got.Namespaces["repo"].Files["docs/SPEC.md"].SidecarBlob != "blob123" {
		t.Fatalf("hydration file mismatch: %#v", got.Namespaces)
	}
}

func TestRescueCreateListAndRestore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := New(filepath.Join(t.TempDir(), "state"))
	writeStateTestFile(t, root, "docs/SPEC.md", "# Spec\n")
	manifest, err := store.CreateRescue(context.Background(), root, "test", []RescueCandidate{
		{Path: "docs/SPEC.md", Class: "local_only"},
	})
	if err != nil {
		t.Fatalf("create rescue: %v", err)
	}
	if len(manifest.Files) != 1 {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if _, err := os.Stat(filepath.Join(root, "docs/SPEC.md")); !os.IsNotExist(err) {
		t.Fatalf("expected rescued file removed from root, stat err=%v", err)
	}
	list, err := store.ListRescues(context.Background())
	if err != nil {
		t.Fatalf("list rescues: %v", err)
	}
	if len(list) != 1 || list[0].ID != manifest.ID {
		t.Fatalf("unexpected rescue list: %#v", list)
	}
	restored, err := store.RestoreRescue(context.Background(), root, manifest.ID, nil, false)
	if err != nil {
		t.Fatalf("restore rescue: %v", err)
	}
	if len(restored.Files) != 1 {
		t.Fatalf("unexpected restored manifest: %#v", restored)
	}
	assertStateTestFile(t, filepath.Join(root, "docs/SPEC.md"), "# Spec\n")
}

func TestRescueRestoreRejectsTamperedManifestPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := New(filepath.Join(t.TempDir(), "state"))
	writeStateTestFile(t, root, "docs/SPEC.md", "# Spec\n")
	manifest, err := store.CreateRescue(context.Background(), root, "test", []RescueCandidate{
		{Path: "docs/SPEC.md", Class: "local_only"},
	})
	if err != nil {
		t.Fatalf("create rescue: %v", err)
	}
	manifest.Files[0].OriginalPath = "../escape.md"
	if err := store.writeRescueManifest(manifest); err != nil {
		t.Fatalf("rewrite rescue manifest: %v", err)
	}
	if _, err := store.RestoreRescue(context.Background(), root, manifest.ID, nil, false); err == nil {
		t.Fatal("expected tampered manifest restore to fail")
	}
	manifest.Files[0].OriginalPath = "docs/../../escape.md"
	if err := store.writeRescueManifest(manifest); err != nil {
		t.Fatalf("rewrite multi-segment rescue manifest: %v", err)
	}
	if _, err := store.RestoreRescue(context.Background(), root, manifest.ID, nil, false); err == nil {
		t.Fatal("expected multi-segment tampered manifest restore to fail")
	}
}

func writeStateTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func assertStateTestFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("file mismatch: got %q want %q", string(data), want)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode mismatch for %s: got %o want %o", path, got, want)
	}
}
