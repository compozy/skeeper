package inittui

import (
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/sidecar"
)

func TestFormStateOptionsSubmitDefaultDirectory(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	opts, err := state.options()
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	if opts.SidecarName != "project-specs" {
		t.Fatalf("unexpected sidecar name %q", opts.SidecarName)
	}
	if opts.Visibility != "private" {
		t.Fatalf("unexpected visibility %q", opts.Visibility)
	}
	if opts.Directory != "project" || !opts.DirectorySet || opts.NoDirectory {
		t.Fatalf("unexpected directory options: %#v", opts)
	}
	if len(opts.Patterns) != 1 || opts.Patterns[0] != "**/SPEC.md" {
		t.Fatalf("unexpected patterns: %#v", opts.Patterns)
	}
}

func TestFormStateOptionsExistingSidecarRequiresNoDirectoryConfirmation(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.mode = initModeExisting
	state.sidecarURL = "git@github.com:org/shared-specs.git"
	state.directory = ""

	if _, err := state.options(); err == nil || !strings.Contains(err.Error(), "confirm no-directory") {
		t.Fatalf("expected no-directory confirmation error, got %v", err)
	}

	state.confirmNoDirectory = true
	opts, err := state.options()
	if err != nil {
		t.Fatalf("options after confirmation: %v", err)
	}
	if opts.Sidecar != "git@github.com:org/shared-specs.git" {
		t.Fatalf("unexpected sidecar URL %q", opts.Sidecar)
	}
	if !opts.NoDirectory || opts.DirectorySet || opts.Directory != "" {
		t.Fatalf("unexpected directory opt-out options: %#v", opts)
	}
}

func TestFormStateOptionsRejectsInvalidDirectory(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.directory = "../outside"

	if _, err := state.options(); err == nil {
		t.Fatal("expected invalid directory error, got nil")
	}
}

func TestFormStateOptionsRequiresExistingSidecarURL(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.mode = initModeExisting

	if _, err := state.options(); err == nil || !strings.Contains(err.Error(), "sidecar URL is required") {
		t.Fatalf("expected sidecar URL error, got %v", err)
	}
}

func TestFormStateOptionsRequiresPatterns(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.patterns = " , "

	if _, err := state.options(); err == nil || !strings.Contains(err.Error(), "patterns") {
		t.Fatalf("expected patterns error, got %v", err)
	}
}
