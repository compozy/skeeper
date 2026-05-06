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

func TestSaveRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := Config{
		Sidecar:   "git@github.com:user/project-specs.git",
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
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if got.Sidecar != cfg.Sidecar || got.Bootstrap != cfg.Bootstrap || len(got.Patterns) != 1 {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}
