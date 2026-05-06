package managedblock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceRemovesManagedBlockAndPreservesOtherContent(t *testing.T) {
	t.Parallel()

	content := "before\n# begin\nmanaged\n# end\nafter\n"
	got := Replace(content, "# begin", "# end")
	want := "before\nafter"
	if got != want {
		t.Fatalf("replace mismatch: got %q want %q", got, want)
	}
}

func TestReplaceLeavesMissingEndMarkerUnchanged(t *testing.T) {
	t.Parallel()

	content := "before\n# begin\nmanaged\n"
	if got := Replace(content, "# begin", "# end"); got != content {
		t.Fatalf("expected unchanged content, got %q", got)
	}
}

func TestWriteFileSyncsAndSetsPermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.log")
	if err := WriteFile(path, []byte("private\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode mismatch: got %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "private\n" {
		t.Fatalf("content mismatch: %q", string(data))
	}
}
