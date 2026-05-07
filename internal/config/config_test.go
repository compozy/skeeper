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
directory: team/project
bootstrap: curl -fsSL https://example.com/install.sh | sh
patterns:
  - "**/SPEC.md"
  - "docs/specs/**"
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
	if cfg.Directory != "team/project" {
		t.Fatalf("unexpected directory %q", cfg.Directory)
	}
	if len(cfg.Patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(cfg.Patterns))
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := `
sidecar: git@github.com:user/project-specs.git
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
			cfg:  Config{Patterns: []string{"**/SPEC.md"}},
		},
		{
			name: "missing patterns",
			cfg:  Config{Sidecar: "git@github.com:user/project-specs.git"},
		},
		{
			name: "empty pattern",
			cfg:  Config{Sidecar: "git@github.com:user/project-specs.git", Patterns: []string{" "}},
		},
		{
			name: "duplicate pattern",
			cfg: Config{
				Sidecar:  "git@github.com:user/project-specs.git",
				Patterns: []string{"**/SPEC.md", "**/SPEC.md"},
			},
		},
		{
			name: "invalid pattern",
			cfg: Config{
				Sidecar:  "git@github.com:user/project-specs.git",
				Patterns: []string{"["},
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
		Sidecar:  "git@github.com:user/project-specs.git",
		Patterns: []string{" ./docs/specs/** ", "src\\**\\SPEC.md"},
	}
	normalized, err := cfg.Normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{"docs/specs/**", "src/**/SPEC.md"}
	if strings.Join(normalized.Patterns, ",") != strings.Join(want, ",") {
		t.Fatalf("patterns mismatch: got %#v want %#v", normalized.Patterns, want)
	}
}

func TestCleanDirectory(t *testing.T) {
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
			got, err := CleanDirectory(tc.input)
			if err != nil {
				t.Fatalf("clean directory: %v", err)
			}
			if got != tc.want {
				t.Fatalf("clean directory mismatch: got %q want %q", got, tc.want)
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
			if _, err := CleanDirectory(input); err == nil {
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
		Directory: "team/project",
		Bootstrap: "brew install user/tap/skeeper",
		Patterns:  []string{"**/SPEC.md"},
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
	if !strings.Contains(string(data), "directory: team/project") {
		t.Fatalf("expected directory in saved config, got:\n%s", string(data))
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if got.Sidecar != cfg.Sidecar || got.Directory != cfg.Directory ||
		got.Bootstrap != cfg.Bootstrap || len(got.Patterns) != 1 {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}
