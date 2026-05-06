package inittui

import (
	"testing"

	"github.com/compozy/skeeper/internal/sidecar"
)

func TestModelSubmitsDefaultDirectory(t *testing.T) {
	t.Parallel()

	model := NewModel(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	updated, _ := model.submit()
	model = updated.(Model)

	if !model.submitted {
		t.Fatal("expected form to submit")
	}
	if model.options.SidecarName != "project-specs" {
		t.Fatalf("unexpected sidecar name %q", model.options.SidecarName)
	}
	if model.options.Directory != "project" || !model.options.DirectorySet || model.options.NoDirectory {
		t.Fatalf("unexpected directory options: %#v", model.options)
	}
	if len(model.options.Patterns) != 1 || model.options.Patterns[0] != "**/SPEC.md" {
		t.Fatalf("unexpected patterns: %#v", model.options.Patterns)
	}
}

func TestModelExistingSidecarRequiresNoDirectoryConfirmation(t *testing.T) {
	t.Parallel()

	model := NewModel(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	model.toggleMode()
	input := model.inputs[inputSidecarURL]
	input.SetValue("git@github.com:org/shared-specs.git")
	model.inputs[inputSidecarURL] = input
	directory := model.inputs[inputDirectory]
	directory.SetValue("")
	model.inputs[inputDirectory] = directory

	updated, _ := model.submit()
	model = updated.(Model)
	if model.submitted {
		t.Fatal("expected first submit to stop at warning")
	}
	if !model.confirmNoDirectory {
		t.Fatal("expected no-directory warning confirmation")
	}

	updated, _ = model.submit()
	model = updated.(Model)
	if !model.submitted {
		t.Fatal("expected second submit to continue")
	}
	if model.options.Sidecar != "git@github.com:org/shared-specs.git" {
		t.Fatalf("unexpected sidecar URL %q", model.options.Sidecar)
	}
	if !model.options.NoDirectory || model.options.DirectorySet || model.options.Directory != "" {
		t.Fatalf("unexpected directory opt-out options: %#v", model.options)
	}
}

func TestModelRejectsInvalidDirectoryBeforeSubmit(t *testing.T) {
	t.Parallel()

	model := NewModel(sidecar.InitDefaults{
		SidecarName: "project-specs",
		Visibility:  "private",
		Directory:   "project",
		Patterns:    []string{"**/SPEC.md"},
	})
	directory := model.inputs[inputDirectory]
	directory.SetValue("../outside")
	model.inputs[inputDirectory] = directory

	updated, _ := model.submit()
	model = updated.(Model)
	if model.submitted {
		t.Fatal("did not expect invalid directory to submit")
	}
	if model.errorMessage == "" {
		t.Fatal("expected validation error message")
	}
}
