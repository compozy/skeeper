package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStrictConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := `
sidecar: git@github.com:user/project-specs.git
bootstrap: curl -fsSL https://example.com/install.sh | sh
namespaces:
  - name: team/project
    patterns:
      - "**/SPEC.md"
      - "docs/specs/**"
    exclude:
      - "docs/specs/private/**"
`
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Sidecar != "git@github.com:user/project-specs.git" {
		t.Fatalf("unexpected sidecar %q", cfg.Sidecar)
	}
	if cfg.Bootstrap == "" {
		t.Fatal("expected bootstrap to be loaded")
	}
	if len(cfg.Namespaces) != 1 {
		t.Fatalf("expected 1 namespace, got %d", len(cfg.Namespaces))
	}
	if cfg.Namespaces[0].Name != "team/project" {
		t.Fatalf("unexpected namespace %q", cfg.Namespaces[0].Name)
	}
	if len(cfg.Namespaces[0].Patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(cfg.Namespaces[0].Patterns))
	}
	if len(cfg.Namespaces[0].Exclude) != 1 {
		t.Fatalf("expected 1 exclude, got %d", len(cfg.Namespaces[0].Exclude))
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := `
sidecar: git@github.com:user/project-specs.git
namespaces:
  - name: project
    patterns:
      - "**/SPEC.md"
extra: true
`
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(dir); err == nil {
		t.Fatal("expected unknown key error, got nil")
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "missing sidecar",
			cfg: Config{Namespaces: []Namespace{
				{Name: "project", Patterns: []string{"**/SPEC.md"}},
			}},
		},
		{
			name: "missing namespaces",
			cfg:  Config{Sidecar: "git@github.com:user/project-specs.git"},
		},
		{
			name: "missing namespace name",
			cfg: Config{
				Sidecar: "git@github.com:user/project-specs.git",
				Namespaces: []Namespace{
					{Patterns: []string{"**/SPEC.md"}},
				},
			},
		},
		{
			name: "empty pattern",
			cfg: Config{
				Sidecar: "git@github.com:user/project-specs.git",
				Namespaces: []Namespace{
					{Name: "project", Patterns: []string{" "}},
				},
			},
		},
		{
			name: "duplicate namespace",
			cfg: Config{
				Sidecar: "git@github.com:user/project-specs.git",
				Namespaces: []Namespace{
					{Name: "project", Patterns: []string{"**/SPEC.md"}},
					{Name: "project", Patterns: []string{"docs/specs/**"}},
				},
			},
		},
		{
			name: "duplicate pattern",
			cfg: Config{
				Sidecar: "git@github.com:user/project-specs.git",
				Namespaces: []Namespace{
					{Name: "project", Patterns: []string{"**/SPEC.md", "**/SPEC.md"}},
				},
			},
		},
		{
			name: "invalid exclude",
			cfg: Config{
				Sidecar: "git@github.com:user/project-specs.git",
				Namespaces: []Namespace{
					{Name: "project", Patterns: []string{"**/SPEC.md"}, Exclude: []string{"["}},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}
}

func TestNormalizeCanonicalizesPatterns(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Sidecar: "git@github.com:user/project-specs.git",
		Namespaces: []Namespace{
			{
				Name:     " team/project ",
				Patterns: []string{" ./docs/specs/** ", "src\\**\\SPEC.md"},
				Exclude:  []string{" ./docs/specs/private/** "},
			},
		},
	}
	normalized, err := cfg.Normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{"docs/specs/**", "src/**/SPEC.md"}
	if normalized.Namespaces[0].Name != "team/project" {
		t.Fatalf("namespace mismatch: got %q", normalized.Namespaces[0].Name)
	}
	if strings.Join(normalized.Namespaces[0].Patterns, ",") != strings.Join(want, ",") {
		t.Fatalf("patterns mismatch: got %#v want %#v", normalized.Namespaces[0].Patterns, want)
	}
	if strings.Join(normalized.Namespaces[0].Exclude, ",") != "docs/specs/private/**" {
		t.Fatalf("exclude mismatch: got %#v", normalized.Namespaces[0].Exclude)
	}
}

func TestCleanNamespace(t *testing.T) {
	t.Parallel()

	valid := []struct {
		input string
		want  string
	}{
		{input: "", want: ""},
		{input: "project", want: "project"},
		{input: "team/project", want: "team/project"},
		{input: "Project_1.2-3", want: "Project_1.2-3"},
		{input: " project ", want: "project"},
	}
	for _, tc := range valid {
		t.Run("valid "+tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := CleanNamespace(tc.input)
			if err != nil {
				t.Fatalf("clean namespace: %v", err)
			}
			if got != tc.want {
				t.Fatalf("clean namespace mismatch: got %q want %q", got, tc.want)
			}
		})
	}

	invalid := []string{
		"/project",
		"../project",
		"team/../project",
		"./project",
		"team//project",
		"team/project/",
		"team\\project",
		"team/__branches__/project",
		"team/project name",
		".git",
		".hidden/project",
		"HEAD",
		"team/config",
	}
	for _, input := range invalid {
		t.Run("invalid "+input, func(t *testing.T) {
			t.Parallel()
			if _, err := CleanNamespace(input); err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}
}

func TestSaveRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := Config{
		Sidecar:   "git@github.com:user/project-specs.git",
		Bootstrap: "brew install user/tap/skeeper",
		Namespaces: []Namespace{
			{Name: "team/project", Patterns: []string{"**/SPEC.md"}, Exclude: []string{"private/**"}},
		},
	}
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, Filename))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "bootstrap: brew install") {
		t.Fatalf("expected bootstrap in saved config, got:\n%s", string(data))
	}
	if !strings.Contains(string(data), "name: team/project") {
		t.Fatalf("expected namespace in saved config, got:\n%s", string(data))
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if got.Sidecar != cfg.Sidecar || got.Bootstrap != cfg.Bootstrap ||
		len(got.Namespaces) != 1 || got.Namespaces[0].Name != "team/project" ||
		len(got.Namespaces[0].Patterns) != 1 || len(got.Namespaces[0].Exclude) != 1 {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}
