// Package config loads and writes the committed skeeper project config.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// Filename is the committed project config file.
	Filename = ".skeeper.yml"
	// DirectoryBranchSegment separates a sidecar directory namespace from
	// branch-specific sidecar refs.
	DirectoryBranchSegment = "__branches__"
)

// Config describes how a project mirrors spec files into its sidecar repo.
type Config struct {
	Sidecar   string   `yaml:"sidecar"`
	Directory string   `yaml:"directory,omitempty"`
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
	cfg, err = cfg.Normalize()
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Save writes cfg to .skeeper.yml under root.
func Save(root string, cfg Config) error {
	normalized, err := cfg.Normalize()
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(normalized); err != nil {
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
	_, err := c.Normalize()
	return err
}

// Normalize validates cfg and returns the canonical config values that should
// drive runtime behavior.
func (c Config) Normalize() (Config, error) {
	if strings.TrimSpace(c.Sidecar) == "" {
		return Config{}, errors.New("sidecar is required")
	}
	if len(c.Patterns) == 0 {
		return Config{}, errors.New("patterns must contain at least one glob")
	}
	directory, err := CleanDirectory(c.Directory)
	if err != nil {
		return Config{}, err
	}
	seen := make(map[string]struct{}, len(c.Patterns))
	for i, pattern := range c.Patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			return Config{}, fmt.Errorf("patterns[%d] is empty", i)
		}
		if _, ok := seen[trimmed]; ok {
			return Config{}, fmt.Errorf("patterns[%d] duplicates %q", i, trimmed)
		}
		seen[trimmed] = struct{}{}
	}
	c.Directory = directory
	return c, nil
}

// CleanDirectory validates a sidecar directory namespace and returns its
// canonical slash-separated form. The empty string is valid legacy behavior.
func CleanDirectory(directory string) (string, error) {
	trimmed := strings.TrimSpace(directory)
	if trimmed == "" {
		return "", nil
	}
	if strings.Contains(trimmed, "\\") {
		return "", errors.New("directory must use slash-separated path segments")
	}
	if path.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") {
		return "", errors.New("directory must be relative")
	}
	if strings.HasPrefix(trimmed, "./") || strings.HasSuffix(trimmed, "/") ||
		strings.Contains(trimmed, "//") {
		return "", fmt.Errorf("directory %q is not a clean relative path", directory)
	}
	cleaned := path.Clean(trimmed)
	if cleaned != trimmed {
		return "", fmt.Errorf("directory %q is not a clean relative path", directory)
	}
	for segment := range strings.SplitSeq(cleaned, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("directory contains invalid segment %q", segment)
		}
		if strings.HasPrefix(segment, ".") {
			return "", fmt.Errorf("directory segment %q cannot start with dot", segment)
		}
		if segment == DirectoryBranchSegment {
			return "", fmt.Errorf("directory segment %q is reserved", DirectoryBranchSegment)
		}
		if reservedDirectorySegment(segment) {
			return "", fmt.Errorf("directory segment %q is reserved for Git internals", segment)
		}
		if !safeDirectorySegment(segment) {
			return "", fmt.Errorf("directory segment %q contains unsupported characters", segment)
		}
	}
	return cleaned, nil
}

func reservedDirectorySegment(segment string) bool {
	switch strings.ToLower(segment) {
	case "head", "config", "hooks", "index", "info", "logs", "objects", "packed-refs", "refs":
		return true
	default:
		return false
	}
}

func safeDirectorySegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}
