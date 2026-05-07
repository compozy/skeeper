package inittui

import (
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/sidecar"
)

func TestFormStateOptionsSubmitDefaultDirectoryWithDefaultPatterns(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md", "docs/specs/**"},
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
	if strings.Join(opts.Patterns, ",") != "**/SPEC.md,docs/specs/**" {
		t.Fatalf("unexpected patterns: %#v", opts.Patterns)
	}
}

func TestFormStateOptionsAppendsExtraContextPatterns(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md", "docs/specs/**"},
	})
	state.syncExtraContext = true
	state.extraPatterns = "AGENTS.md\nCLAUDE.md\n.codex/plans/**"

	opts, err := state.options()
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	want := "**/SPEC.md,docs/specs/**,AGENTS.md,CLAUDE.md,.codex/plans/**"
	if strings.Join(opts.Patterns, ",") != want {
		t.Fatalf("unexpected patterns: %#v", opts.Patterns)
	}
}

func TestFormStateOptionsSplitsCommaSeparatedExtraContextPatterns(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.syncExtraContext = true
	state.extraPatterns = "AGENTS.md, CLAUDE.md"

	opts, err := state.options()
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	want := "**/SPEC.md,AGENTS.md,CLAUDE.md"
	if strings.Join(opts.Patterns, ",") != want {
		t.Fatalf("unexpected patterns: %#v", opts.Patterns)
	}
}

func TestFormStateOptionsDeduplicatesExtraContextPatterns(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.syncExtraContext = true
	state.extraPatterns = "**/SPEC.md\n./AGENTS.md\nAGENTS.md"

	opts, err := state.options()
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	want := "**/SPEC.md,AGENTS.md"
	if strings.Join(opts.Patterns, ",") != want {
		t.Fatalf("unexpected patterns: %#v", opts.Patterns)
	}
}

func TestFormStateOptionsRequiresConfirmedExtraContextPatterns(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.syncExtraContext = true
	state.extraPatterns = " , \n "

	if _, err := state.options(); err == nil || !strings.Contains(err.Error(), "extra context globs") {
		t.Fatalf("expected extra context glob error, got %v", err)
	}
}

func TestFormStateOptionsRejectsInvalidExtraContextPattern(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.syncExtraContext = true
	state.extraPatterns = "["

	if _, err := state.options(); err == nil || !strings.Contains(err.Error(), "invalid glob") {
		t.Fatalf("expected invalid glob error, got %v", err)
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
	})

	if _, err := state.options(); err == nil || !strings.Contains(err.Error(), "patterns") {
		t.Fatalf("expected patterns error, got %v", err)
	}
}
