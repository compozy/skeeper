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
