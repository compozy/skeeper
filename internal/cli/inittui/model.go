// Package inittui contains the interactive init form.
package inittui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/sidecar"
)

const (
	modeCreate mode = iota
	modeExisting

	fieldMode field = iota
	fieldSidecar
	fieldVisibility
	fieldDirectory
	fieldBootstrap
	fieldPatterns
	fieldSubmit

	inputSidecarName inputID = iota
	inputSidecarURL
	inputDirectory
	inputBootstrap
	inputPatterns
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	focusStyle   = lipgloss.NewStyle().Bold(true)
	warningStyle = lipgloss.NewStyle().Bold(true)
)

type mode int

type field int

type inputID int

// Model is the Bubble Tea model for the init form.
type Model struct {
	mode               mode
	visibility         string
	defaults           sidecar.InitDefaults
	inputs             map[inputID]textinput.Model
	focus              field
	confirmNoDirectory bool
	errorMessage       string
	canceled           bool
	submitted          bool
	options            sidecar.InitOptions
}

// Run opens the init form and returns the selected options.
func Run(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	defaults sidecar.InitDefaults,
) (sidecar.InitOptions, error) {
	program := tea.NewProgram(
		NewModel(defaults),
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
	)
	result, err := program.Run()
	if err != nil {
		return sidecar.InitOptions{}, fmt.Errorf("run init form: %w", err)
	}
	model, ok := result.(Model)
	if !ok {
		return sidecar.InitOptions{}, errors.New("init form returned unexpected model")
	}
	if model.canceled {
		return sidecar.InitOptions{}, context.Canceled
	}
	if !model.submitted {
		return sidecar.InitOptions{}, errors.New("init form closed before submission")
	}
	return model.options, nil
}

// NewModel returns an initialized Bubble Tea model for tests and Run.
func NewModel(defaults sidecar.InitDefaults) Model {
	inputs := map[inputID]textinput.Model{
		inputSidecarName: newInput("project-specs", defaults.SidecarName),
		inputSidecarURL:  newInput("git@github.com:org/shared-specs.git", ""),
		inputDirectory:   newInput("project", defaults.Directory),
		inputBootstrap:   newInput("brew install compozy/skeeper/skeeper", ""),
		inputPatterns:    newInput("**/SPEC.md, docs/specs/**", strings.Join(defaults.Patterns, ", ")),
	}
	first := inputs[inputSidecarName]
	first.Focus()
	inputs[inputSidecarName] = first
	visibility := defaults.Visibility
	if visibility == "" {
		visibility = "private"
	}
	return Model{
		mode:       modeCreate,
		visibility: visibility,
		defaults:   defaults,
		inputs:     inputs,
		focus:      fieldSidecar,
	}
}

func newInput(placeholder, value string) textinput.Model {
	input := textinput.New()
	input.Placeholder = placeholder
	input.SetWidth(56)
	input.SetValue(value)
	return input
}

// Init starts cursor blinking.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles key events and active text input updates.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.String() {
		case "ctrl+c", "esc":
			m.canceled = true
			return m, tea.Quit
		case "tab", "down":
			return m.move(1), nil
		case "shift+tab", "up":
			return m.move(-1), nil
		case "left", "right", " ":
			if m.focus == fieldMode {
				m.toggleMode()
				return m, nil
			}
			if m.focus == fieldVisibility {
				m.toggleVisibility(msg.String())
				return m, nil
			}
		case "enter":
			if m.focus == fieldSubmit {
				return m.submit()
			}
			return m.move(1), nil
		}
	}
	id, ok := m.focusedInput()
	if !ok {
		return m, nil
	}
	input, cmd := m.inputs[id].Update(msg)
	m.inputs[id] = input
	m.confirmNoDirectory = false
	m.errorMessage = ""
	return m, cmd
}

// View renders the init form.
func (m Model) View() tea.View {
	var b strings.Builder
	b.WriteString(titleStyle.Render("skeeper init"))
	b.WriteString("\n\n")
	b.WriteString(m.renderMode())
	b.WriteString("\n")
	b.WriteString(m.renderSidecar())
	b.WriteString("\n")
	if m.mode == modeCreate {
		b.WriteString(m.renderVisibility())
		b.WriteString("\n")
	}
	b.WriteString(m.renderInput(fieldDirectory, "Directory", inputDirectory))
	b.WriteString("\n")
	b.WriteString(m.renderInput(fieldBootstrap, "Bootstrap", inputBootstrap))
	b.WriteString("\n")
	b.WriteString(m.renderInput(fieldPatterns, "Patterns", inputPatterns))
	b.WriteString("\n\n")
	if m.confirmNoDirectory {
		b.WriteString(
			warningStyle.Render(
				"Warning: without directory, shared sidecars use the root path and source branch directly.",
			),
		)
		b.WriteString("\nPress enter again to continue without a namespace.\n\n")
	}
	if m.errorMessage != "" {
		b.WriteString(warningStyle.Render(m.errorMessage))
		b.WriteString("\n\n")
	}
	submit := "Submit"
	if m.focus == fieldSubmit {
		submit = focusStyle.Render("> " + submit)
	} else {
		submit = "  " + submit
	}
	b.WriteString(submit)
	b.WriteString("\n\n")
	b.WriteString("tab/down: next  shift+tab/up: previous  space: toggle\n")
	b.WriteString("enter: submit  esc: cancel\n")
	return tea.NewView(b.String())
}

func (m Model) renderMode() string {
	value := "[create new sidecar] use existing sidecar"
	if m.mode == modeExisting {
		value = "create new sidecar [use existing sidecar]"
	}
	return m.renderField(fieldMode, "Mode", value)
}

func (m Model) renderSidecar() string {
	if m.mode == modeExisting {
		return m.renderInput(fieldSidecar, "Sidecar URL", inputSidecarURL)
	}
	return m.renderInput(fieldSidecar, "Sidecar name", inputSidecarName)
}

func (m Model) renderVisibility() string {
	return m.renderField(fieldVisibility, "Visibility", m.visibility)
}

func (m Model) renderInput(target field, label string, id inputID) string {
	return m.renderField(target, label, m.inputs[id].View())
}

func (m Model) renderField(target field, label, value string) string {
	prefix := "  "
	if m.focus == target {
		prefix = "> "
		label = focusStyle.Render(label)
	}
	return fmt.Sprintf("%s%s: %s", prefix, label, value)
}

func (m Model) move(delta int) Model {
	fields := m.focusableFields()
	current := 0
	for i, candidate := range fields {
		if candidate == m.focus {
			current = i
			break
		}
	}
	next := (current + delta + len(fields)) % len(fields)
	m.focus = fields[next]
	m.applyInputFocus()
	return m
}

func (m *Model) toggleMode() {
	if m.mode == modeCreate {
		m.mode = modeExisting
	} else {
		m.mode = modeCreate
	}
	m.confirmNoDirectory = false
	m.errorMessage = ""
	m.applyInputFocus()
}

func (m *Model) toggleVisibility(key string) {
	values := []string{"private", "public", "internal"}
	index := 0
	for i, value := range values {
		if value == m.visibility {
			index = i
			break
		}
	}
	if key == "left" {
		index = (index - 1 + len(values)) % len(values)
	} else {
		index = (index + 1) % len(values)
	}
	m.visibility = values[index]
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	directory := strings.TrimSpace(m.inputs[inputDirectory].Value())
	if directory == "" && !m.confirmNoDirectory {
		m.confirmNoDirectory = true
		m.errorMessage = ""
		return m, nil
	}
	if directory != "" {
		cleaned, err := config.CleanDirectory(directory)
		if err != nil {
			m.errorMessage = err.Error()
			m.focus = fieldDirectory
			m.applyInputFocus()
			return m, nil
		}
		directory = cleaned
	}
	patterns := splitPatterns(m.inputs[inputPatterns].Value())
	if len(patterns) == 0 {
		m.errorMessage = "patterns must contain at least one glob"
		m.focus = fieldPatterns
		m.applyInputFocus()
		return m, nil
	}
	opts := sidecar.InitOptions{
		Directory:    directory,
		DirectorySet: directory != "",
		NoDirectory:  directory == "",
		Bootstrap:    strings.TrimSpace(m.inputs[inputBootstrap].Value()),
		Patterns:     patterns,
	}
	if m.mode == modeExisting {
		opts.Sidecar = strings.TrimSpace(m.inputs[inputSidecarURL].Value())
		if opts.Sidecar == "" {
			m.errorMessage = "sidecar URL is required when using an existing sidecar"
			m.focus = fieldSidecar
			m.applyInputFocus()
			return m, nil
		}
	} else {
		opts.Visibility = m.visibility
		opts.SidecarName = strings.TrimSpace(m.inputs[inputSidecarName].Value())
	}
	m.options = opts
	m.submitted = true
	return m, tea.Quit
}

func (m Model) focusableFields() []field {
	if m.mode == modeCreate {
		return []field{
			fieldMode,
			fieldSidecar,
			fieldVisibility,
			fieldDirectory,
			fieldBootstrap,
			fieldPatterns,
			fieldSubmit,
		}
	}
	return []field{
		fieldMode,
		fieldSidecar,
		fieldDirectory,
		fieldBootstrap,
		fieldPatterns,
		fieldSubmit,
	}
}

func (m *Model) applyInputFocus() {
	for id := range m.inputs {
		input := m.inputs[id]
		if focused, ok := m.focusedInput(); ok && focused == id {
			input.Focus()
		} else {
			input.Blur()
		}
		m.inputs[id] = input
	}
}

func (m Model) focusedInput() (inputID, bool) {
	switch m.focus {
	case fieldSidecar:
		if m.mode == modeExisting {
			return inputSidecarURL, true
		}
		return inputSidecarName, true
	case fieldDirectory:
		return inputDirectory, true
	case fieldBootstrap:
		return inputBootstrap, true
	case fieldPatterns:
		return inputPatterns, true
	default:
		return 0, false
	}
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
