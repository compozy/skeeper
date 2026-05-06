package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueLifecycleAndLog(t *testing.T) {
	t.Parallel()

	store := New(filepath.Join(t.TempDir(), ".git", "skeeper"))
	initial, err := store.Queue()
	if err != nil {
		t.Fatalf("read initial queue: %v", err)
	}
	if len(initial) != 0 {
		t.Fatalf("expected empty queue, got %d", len(initial))
	}

	entry := Entry{Time: time.Now().UTC(), Reason: "network", MainSHA: "abc123"}
	if err := store.Enqueue(entry); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	assertFileMode(t, filepath.Join(store.dir, queueFile), 0o600)
	queue, err := store.Queue()
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if len(queue) != 1 || queue[0].Reason != "network" {
		t.Fatalf("unexpected queue: %#v", queue)
	}
	if err := store.AppendLog("queued sync: network"); err != nil {
		t.Fatalf("append log: %v", err)
	}
	assertFileMode(t, filepath.Join(store.dir, logFile), 0o600)
	if err := store.ClearQueue(); err != nil {
		t.Fatalf("clear queue: %v", err)
	}
	cleared, err := store.Queue()
	if err != nil {
		t.Fatalf("read cleared queue: %v", err)
	}
	if len(cleared) != 0 {
		t.Fatalf("expected cleared queue, got %d", len(cleared))
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
