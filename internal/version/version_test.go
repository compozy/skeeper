package version

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	t.Run("Should format defaults when build metadata is unset", func(t *testing.T) {
		t.Parallel()
		got := String()
		if !strings.Contains(got, "dev") {
			t.Fatalf("expected default version in output, got %q", got)
		}
		if !strings.Contains(got, "commit=none") {
			t.Fatalf("expected default commit in output, got %q", got)
		}
		if !strings.Contains(got, "date=unknown") {
			t.Fatalf("expected default build date in output, got %q", got)
		}
	})
}

func TestGet(t *testing.T) {
	t.Run("Should expose the current build metadata", func(t *testing.T) {
		t.Parallel()
		info := Get()
		if info.Version != Version {
			t.Fatalf("Version mismatch: %q vs %q", info.Version, Version)
		}
		if info.Commit != Commit {
			t.Fatalf("Commit mismatch: %q vs %q", info.Commit, Commit)
		}
		if info.BuildDate != BuildDate {
			t.Fatalf("BuildDate mismatch: %q vs %q", info.BuildDate, BuildDate)
		}
	})
}
