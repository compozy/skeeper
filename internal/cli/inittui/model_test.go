package inittui

import (
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/sidecar"
)

func TestFormStateOptionsSubmitDefaultNamespaceWithDefaultPatterns(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Namespace:   "project",
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
	if opts.Namespace != "project" || !opts.NamespaceSet {
		t.Fatalf("unexpected namespace options: %#v", opts)
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
		Namespace:   "project",
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
		Namespace:   "project",
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
		Namespace:   "project",
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
		Namespace:   "project",
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
		Namespace:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.syncExtraContext = true
	state.extraPatterns = "["

	if _, err := state.options(); err == nil || !strings.Contains(err.Error(), "invalid glob") {
		t.Fatalf("expected invalid glob error, got %v", err)
	}
}

func TestFormStateOptionsRequiresNamespace(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Namespace:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.namespace = ""

	if _, err := state.options(); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("expected namespace error, got %v", err)
	}
}

func TestFormStateOptionsExistingSidecarUsesNamespace(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Namespace:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.mode = initModeExisting
	state.sidecarURL = "git@github.com:org/shared-specs.git"
	opts, err := state.options()
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	if opts.Sidecar != "git@github.com:org/shared-specs.git" {
		t.Fatalf("unexpected sidecar URL %q", opts.Sidecar)
	}
	if opts.Namespace != "project" || !opts.NamespaceSet {
		t.Fatalf("unexpected namespace options: %#v", opts)
	}
}

func TestFormStateOptionsRejectsInvalidNamespace(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Namespace:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	state.namespace = "../outside"

	if _, err := state.options(); err == nil {
		t.Fatal("expected invalid namespace error, got nil")
	}
}

func TestFormStateOptionsRequiresExistingSidecarURL(t *testing.T) {
	t.Parallel()

	state := newFormState(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Namespace:   "project",
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
		Namespace:   "project",
	})

	if _, err := state.options(); err == nil || !strings.Contains(err.Error(), "patterns") {
		t.Fatalf("expected patterns error, got %v", err)
	}
}
