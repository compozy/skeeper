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
	mode             string
	sidecarName      string
	sidecarURL       string
	visibility       string
	namespace        string
	bootstrap        string
	defaultPatterns  []string
	syncExtraContext bool
	extraPatterns    string
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
		mode:            initModeCreate,
		sidecarName:     defaults.SidecarName,
		visibility:      visibility,
		namespace:       defaults.Namespace,
		defaultPatterns: append([]string(nil), defaults.Patterns...),
	}
}

func newForm(state *formState) *huh.Form {
	return huh.NewForm(
		modeGroup(state),
		createSidecarGroup(state),
		existingSidecarGroup(state),
		projectSettingsGroup(state),
		extraContextGroup(state),
	)
}

func modeGroup(state *formState) *huh.Group {
	return huh.NewGroup(
		huh.NewSelect[string]().
			Title("Mode").
			Options(
				huh.NewOption("Create new sidecar", initModeCreate),
				huh.NewOption("Use existing sidecar", initModeExisting),
			).
			Value(&state.mode),
	)
}

func createSidecarGroup(state *formState) *huh.Group {
	return huh.NewGroup(
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
	})
}

func existingSidecarGroup(state *formState) *huh.Group {
	return huh.NewGroup(
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
	})
}

func projectSettingsGroup(state *formState) *huh.Group {
	return huh.NewGroup(
		huh.NewInput().
			Title("Namespace").
			Placeholder("project").
			Value(&state.namespace).
			Validate(validateNamespace),
		huh.NewInput().
			Title("Bootstrap").
			Placeholder("brew install compozy/skeeper/skeeper").
			Value(&state.bootstrap),
		huh.NewConfirm().
			Title("Sync extra context folders?").
			Description("Default spec globs are already included. Add only extra context you explicitly want in the sidecar.").
			Affirmative("Add globs").
			Negative("Skip").
			Value(&state.syncExtraContext),
	)
}

func extraContextGroup(state *formState) *huh.Group {
	return huh.NewGroup(
		huh.NewText().
			Title("Extra context globs").
			Description("One glob per line. Examples: AGENTS.md, CLAUDE.md, .codex/plans/**").
			Placeholder("AGENTS.md\nCLAUDE.md\n.codex/plans/**").
			Lines(4).
			Value(&state.extraPatterns).
			Validate(validateExtraPatterns),
	).WithHideFunc(func() bool {
		return !state.syncExtraContext
	})
}

func validateNamespace(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("namespace is required")
	}
	_, err := config.CleanNamespace(value)
	return err
}

func validateExtraPatterns(value string) error {
	if len(splitPatterns(value)) == 0 {
		return errors.New("extra context globs must contain at least one glob")
	}
	_, err := normalizePatternsAllowDuplicates(splitPatterns(value))
	return err
}

func (s formState) options() (sidecar.InitOptions, error) {
	namespace := strings.TrimSpace(s.namespace)
	cleaned, err := config.CleanNamespace(namespace)
	if err != nil {
		return sidecar.InitOptions{}, err
	}
	if cleaned == "" {
		return sidecar.InitOptions{}, errors.New("namespace is required")
	}
	namespace = cleaned
	patterns, err := config.NormalizePatterns(s.defaultPatterns)
	if err != nil {
		return sidecar.InitOptions{}, err
	}
	if len(patterns) == 0 {
		return sidecar.InitOptions{}, errors.New("patterns must contain at least one glob")
	}
	if s.syncExtraContext {
		extraPatterns := splitPatterns(s.extraPatterns)
		if len(extraPatterns) == 0 {
			return sidecar.InitOptions{}, errors.New("extra context globs must contain at least one glob")
		}
		extraPatterns, err = normalizePatternsAllowDuplicates(extraPatterns)
		if err != nil {
			return sidecar.InitOptions{}, err
		}
		patterns = appendUniquePatterns(patterns, extraPatterns)
	}
	opts := sidecar.InitOptions{
		Namespace:    namespace,
		NamespaceSet: true,
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
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	patterns := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			patterns = append(patterns, trimmed)
		}
	}
	return patterns
}

func appendUniquePatterns(patterns, extraPatterns []string) []string {
	seen := make(map[string]struct{}, len(patterns)+len(extraPatterns))
	for _, pattern := range patterns {
		seen[pattern] = struct{}{}
	}
	for _, pattern := range extraPatterns {
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		patterns = append(patterns, pattern)
	}
	return patterns
}

func normalizePatternsAllowDuplicates(patterns []string) ([]string, error) {
	normalized := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		cleaned, err := config.NormalizePatterns([]string{pattern})
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, cleaned...)
	}
	return normalized, nil
}
