// Package inittui contains the interactive init form.
package inittui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"charm.land/huh/v2"
	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/sidecar"
)

const (
	initModeCreate   = "create"
	initModeExisting = "existing"

	defaultVisibility = "private"
)

type formState struct {
	mode               string
	sidecarName        string
	sidecarURL         string
	visibility         string
	directory          string
	confirmNoDirectory bool
	bootstrap          string
	patterns           string
}

// Run opens the init form and returns the selected options.
func Run(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	defaults sidecar.InitDefaults,
) (sidecar.InitOptions, error) {
	state := newFormState(defaults)
	form := newForm(&state).WithInput(input).WithOutput(output)
	if err := form.RunWithContext(ctx); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return sidecar.InitOptions{}, context.Canceled
		}
		return sidecar.InitOptions{}, fmt.Errorf("run init form: %w", err)
	}
	return state.options()
}

func newFormState(defaults sidecar.InitDefaults) formState {
	visibility := strings.TrimSpace(defaults.Visibility)
	if visibility == "" {
		visibility = defaultVisibility
	}
	return formState{
		mode:        initModeCreate,
		sidecarName: defaults.SidecarName,
		visibility:  visibility,
		directory:   defaults.Directory,
		patterns:    strings.Join(defaults.Patterns, ", "),
	}
}

func newForm(state *formState) *huh.Form {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Mode").
				Options(
					huh.NewOption("Create new sidecar", initModeCreate),
					huh.NewOption("Use existing sidecar", initModeExisting),
				).
				Value(&state.mode),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Sidecar name").
				Placeholder("project-specs").
				Value(&state.sidecarName),
			huh.NewSelect[string]().
				Title("Visibility").
				Options(
					huh.NewOption("Private", "private"),
					huh.NewOption("Public", "public"),
					huh.NewOption("Internal", "internal"),
				).
				Value(&state.visibility),
		).WithHideFunc(func() bool {
			return state.mode != initModeCreate
		}),
		huh.NewGroup(
			huh.NewInput().
				Title("Sidecar URL").
				Placeholder("git@github.com:org/shared-specs.git").
				Value(&state.sidecarURL).
				Validate(func(value string) error {
					if strings.TrimSpace(value) == "" {
						return errors.New("sidecar URL is required when using an existing sidecar")
					}
					return nil
				}),
		).WithHideFunc(func() bool {
			return state.mode != initModeExisting
		}),
		huh.NewGroup(
			huh.NewInput().
				Title("Directory").
				Placeholder("project").
				Value(&state.directory).
				Validate(validateDirectory),
			huh.NewInput().
				Title("Bootstrap").
				Placeholder("brew install compozy/skeeper/skeeper").
				Value(&state.bootstrap),
			huh.NewInput().
				Title("Patterns").
				Placeholder("**/SPEC.md, docs/specs/**").
				Value(&state.patterns).
				Validate(validatePatterns),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Continue without a directory namespace?").
				Description("Shared sidecars can collide at root and push to the same branch.").
				Affirmative("Continue").
				Negative("Go back").
				Value(&state.confirmNoDirectory).
				Validate(func(confirmed bool) error {
					if !confirmed {
						return errors.New("confirm no-directory or enter a directory namespace")
					}
					return nil
				}),
		).WithHideFunc(func() bool {
			return strings.TrimSpace(state.directory) != ""
		}),
	)
}

func validateDirectory(value string) error {
	_, err := config.CleanDirectory(value)
	return err
}

func validatePatterns(value string) error {
	if len(splitPatterns(value)) == 0 {
		return errors.New("patterns must contain at least one glob")
	}
	return nil
}

func (s formState) options() (sidecar.InitOptions, error) {
	directory := strings.TrimSpace(s.directory)
	if directory == "" && !s.confirmNoDirectory {
		return sidecar.InitOptions{}, errors.New("confirm no-directory or enter a directory namespace")
	}
	if directory != "" {
		cleaned, err := config.CleanDirectory(directory)
		if err != nil {
			return sidecar.InitOptions{}, err
		}
		directory = cleaned
	}
	patterns := splitPatterns(s.patterns)
	if len(patterns) == 0 {
		return sidecar.InitOptions{}, errors.New("patterns must contain at least one glob")
	}
	opts := sidecar.InitOptions{
		Directory:    directory,
		DirectorySet: directory != "",
		NoDirectory:  directory == "",
		Bootstrap:    strings.TrimSpace(s.bootstrap),
		Patterns:     patterns,
	}
	if s.mode == initModeExisting {
		opts.Sidecar = strings.TrimSpace(s.sidecarURL)
		if opts.Sidecar == "" {
			return sidecar.InitOptions{}, errors.New("sidecar URL is required when using an existing sidecar")
		}
		return opts, nil
	}
	opts.Visibility = strings.TrimSpace(s.visibility)
	opts.SidecarName = strings.TrimSpace(s.sidecarName)
	return opts, nil
}

func splitPatterns(value string) []string {
	parts := strings.Split(value, ",")
	patterns := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			patterns = append(patterns, trimmed)
		}
	}
	return patterns
}
