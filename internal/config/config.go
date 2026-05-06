// Package config loads and writes the committed skeeper project config.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// Filename is the committed project config file.
	Filename = ".skeeper.yml"
)

// Config describes how a project mirrors spec files into its sidecar repo.
type Config struct {
	Sidecar   string   `yaml:"sidecar"`
	Bootstrap string   `yaml:"bootstrap,omitempty"`
	Patterns  []string `yaml:"patterns"`
}

// DefaultPatterns returns the interactive init defaults.
func DefaultPatterns() []string {
	return []string{
		"**/SPEC.md",
		"docs/specs/**",
		".claude/plans/**",
		"**/*.spec.md",
	}
}

// Load reads and validates .skeeper.yml from root.
func Load(root string) (Config, error) {
	path := filepath.Join(root, Filename)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("%s not found", Filename)
		}
		return Config{}, fmt.Errorf("open %s: %w", Filename, err)
	}
	defer file.Close()

	var cfg Config
	dec := yaml.NewDecoder(file)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode %s: %w", Filename, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Save writes cfg to .skeeper.yml under root.
func Save(root string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode %s: %w", Filename, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close yaml encoder: %w", err)
	}

	path := filepath.Join(root, Filename)
	if err := writeFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", Filename, err)
	}
	return nil
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// Validate checks that the config can drive deterministic v1 behavior.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Sidecar) == "" {
		return errors.New("sidecar is required")
	}
	if len(c.Patterns) == 0 {
		return errors.New("patterns must contain at least one glob")
	}
	seen := make(map[string]struct{}, len(c.Patterns))
	for i, pattern := range c.Patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			return fmt.Errorf("patterns[%d] is empty", i)
		}
		if _, ok := seen[trimmed]; ok {
			return fmt.Errorf("patterns[%d] duplicates %q", i, trimmed)
		}
		seen[trimmed] = struct{}{}
	}
	return nil
}
