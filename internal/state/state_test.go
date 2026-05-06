package state

import (
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
