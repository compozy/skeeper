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
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

const (
	// Filename is the committed project config file.
	Filename = ".skeeper.yml"
	// NamespaceBranchSegment separates a sidecar namespace from branch-specific
	// sidecar refs.
	NamespaceBranchSegment = "__branches__"
	// DefaultGuardrailMaxFiles is the default broad-plan file threshold.
	DefaultGuardrailMaxFiles = 100
	// DefaultGuardrailMaxBytes is the default broad-plan byte threshold.
	DefaultGuardrailMaxBytes int64 = 10_485_760
	// DefaultPrePushTimeout is the default timeout for the read-only pre-push gate.
	DefaultPrePushTimeout = 30 * time.Second
	// DefaultAllowSkipEnv is the audited strict pre-commit bypass variable.
	DefaultAllowSkipEnv = "SKEEPER_SKIP"
)

// Config describes how a project mirrors spec files into its sidecar repo.
type Config struct {
	Sidecar    string      `yaml:"sidecar"`
	Bootstrap  string      `yaml:"bootstrap,omitempty"`
	Settings   Settings    `yaml:"settings,omitempty"`
	Namespaces []Namespace `yaml:"namespaces"`
}

// Settings describes operational defaults outside namespace ownership rules.
type Settings struct {
	Guardrails GuardrailSettings `yaml:"guardrails,omitempty"`
	Hooks      HookSettings      `yaml:"hooks,omitempty"`
}

// GuardrailSettings bounds broad mutating plans.
type GuardrailSettings struct {
	MaxFiles int   `yaml:"max_files,omitempty"`
	MaxBytes int64 `yaml:"max_bytes,omitempty"`
}

// HookSettings configures managed Git hooks.
type HookSettings struct {
	PrePushTimeout string `yaml:"pre_push_timeout,omitempty"`
	AllowSkipEnv   string `yaml:"allow_skip_env,omitempty"`
}

// Namespace routes a set of project files into one sidecar namespace.
type Namespace struct {
	Name             string        `yaml:"name"`
	Patterns         []string      `yaml:"patterns"`
	Exclude          []string      `yaml:"exclude,omitempty"`
	RespectGitignore *bool         `yaml:"respect_gitignore,omitempty"`
	Hydrate          HydratePolicy `yaml:"hydrate,omitempty"`
}

// HydratePolicy configures namespace defaults for safe materialization.
type HydratePolicy struct {
	OnLocalOnly string        `yaml:"on_local_only,omitempty"`
	OnModified  string        `yaml:"on_modified,omitempty"`
	Rules       []HydrateRule `yaml:"rules,omitempty"`
}

// HydrateRule configures hydrate defaults for a subset of paths.
type HydrateRule struct {
	Pattern     string `yaml:"pattern"`
	OnLocalOnly string `yaml:"on_local_only,omitempty"`
	OnModified  string `yaml:"on_modified,omitempty"`
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
	hasExplicitSettings := hasSettings(cfg.Settings)
	normalized, err := cfg.Normalize()
	if err != nil {
		return err
	}
	if !hasExplicitSettings {
		normalized.Settings = Settings{}
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

func hasSettings(settings Settings) bool {
	return settings.Guardrails.MaxFiles != 0 ||
		settings.Guardrails.MaxBytes != 0 ||
		settings.Hooks.PrePushTimeout != "" ||
		settings.Hooks.AllowSkipEnv != ""
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
	settings, err := NormalizeSettings(c.Settings)
	if err != nil {
		return Config{}, err
	}
	namespaces, err := NormalizeNamespaces(c.Namespaces)
	if err != nil {
		return Config{}, err
	}
	if len(namespaces) == 0 {
		return Config{}, errors.New("namespaces must contain at least one namespace")
	}
	c.Settings = settings
	c.Namespaces = namespaces
	return c, nil
}

// NormalizeSettings validates and fills operational defaults.
func NormalizeSettings(settings Settings) (Settings, error) {
	if settings.Guardrails.MaxFiles < 0 {
		return Settings{}, errors.New("settings.guardrails.max_files must be non-negative")
	}
	if settings.Guardrails.MaxFiles == 0 {
		settings.Guardrails.MaxFiles = DefaultGuardrailMaxFiles
	}
	if settings.Guardrails.MaxBytes < 0 {
		return Settings{}, errors.New("settings.guardrails.max_bytes must be non-negative")
	}
	if settings.Guardrails.MaxBytes == 0 {
		settings.Guardrails.MaxBytes = DefaultGuardrailMaxBytes
	}
	if strings.TrimSpace(settings.Hooks.PrePushTimeout) == "" {
		settings.Hooks.PrePushTimeout = DefaultPrePushTimeout.String()
	}
	if _, err := time.ParseDuration(settings.Hooks.PrePushTimeout); err != nil {
		return Settings{}, fmt.Errorf("settings.hooks.pre_push_timeout: %w", err)
	}
	if strings.TrimSpace(settings.Hooks.AllowSkipEnv) == "" {
		settings.Hooks.AllowSkipEnv = DefaultAllowSkipEnv
	}
	if !validEnvName(settings.Hooks.AllowSkipEnv) {
		return Settings{}, fmt.Errorf(
			"settings.hooks.allow_skip_env %q is not a valid environment variable name",
			settings.Hooks.AllowSkipEnv,
		)
	}
	return settings, nil
}

// NormalizeNamespaces validates and canonicalizes namespace routing rules.
func NormalizeNamespaces(namespaces []Namespace) ([]Namespace, error) {
	normalized := make([]Namespace, 0, len(namespaces))
	seen := make(map[string]struct{}, len(namespaces))
	for i, namespace := range namespaces {
		name, err := CleanNamespace(namespace.Name)
		if err != nil {
			return nil, fmt.Errorf("namespaces[%d].name: %w", i, err)
		}
		if name == "" {
			return nil, fmt.Errorf("namespaces[%d].name is required", i)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("namespaces[%d].name duplicates %q", i, name)
		}
		seen[name] = struct{}{}
		patterns, err := normalizePatternsField("patterns", namespace.Patterns)
		if err != nil {
			return nil, fmt.Errorf("namespaces[%d]: %w", i, err)
		}
		if len(patterns) == 0 {
			return nil, fmt.Errorf("namespaces[%d].patterns must contain at least one glob", i)
		}
		for _, pattern := range patterns {
			if strings.HasPrefix(pattern, "!") {
				return nil, fmt.Errorf(
					"namespaces[%d].patterns rejects negative glob %q; use exclude instead",
					i,
					pattern,
				)
			}
		}
		exclude, err := normalizePatternsField("exclude", namespace.Exclude)
		if err != nil {
			return nil, fmt.Errorf("namespaces[%d]: %w", i, err)
		}
		hydrate, err := NormalizeHydratePolicy(namespace.Hydrate)
		if err != nil {
			return nil, fmt.Errorf("namespaces[%d].hydrate: %w", i, err)
		}
		normalized = append(normalized, Namespace{
			Name:             name,
			Patterns:         patterns,
			Exclude:          exclude,
			RespectGitignore: normalizeRespectGitignore(namespace.RespectGitignore),
			Hydrate:          hydrate,
		})
	}
	return normalized, nil
}

// NormalizeHydratePolicy validates and canonicalizes hydrate defaults.
func NormalizeHydratePolicy(policy HydratePolicy) (HydratePolicy, error) {
	if err := validateLocalOnlyPolicy(policy.OnLocalOnly); err != nil {
		return HydratePolicy{}, fmt.Errorf("on_local_only: %w", err)
	}
	if err := validateModifiedPolicy(policy.OnModified); err != nil {
		return HydratePolicy{}, fmt.Errorf("on_modified: %w", err)
	}
	for i, rule := range policy.Rules {
		patterns, err := normalizePatternsField("rules.pattern", []string{rule.Pattern})
		if err != nil {
			return HydratePolicy{}, fmt.Errorf("rules[%d]: %w", i, err)
		}
		rule.Pattern = patterns[0]
		if err := validateLocalOnlyPolicy(rule.OnLocalOnly); err != nil {
			return HydratePolicy{}, fmt.Errorf("rules[%d].on_local_only: %w", i, err)
		}
		if err := validateModifiedPolicy(rule.OnModified); err != nil {
			return HydratePolicy{}, fmt.Errorf("rules[%d].on_modified: %w", i, err)
		}
		policy.Rules[i] = rule
	}
	return policy, nil
}

func validateLocalOnlyPolicy(value string) error {
	switch value {
	case "", "fail", "keep", "adopt", "prune_to_rescue":
		return nil
	default:
		return fmt.Errorf("unsupported policy %q", value)
	}
}

func validateModifiedPolicy(value string) error {
	switch value {
	case "", "fail", "keep", "merge", "adopt", "overwrite_to_rescue":
		return nil
	default:
		return fmt.Errorf("unsupported policy %q", value)
	}
}

// NormalizePatterns validates and canonicalizes doublestar path globs while
// preserving their order.
func NormalizePatterns(patterns []string) ([]string, error) {
	return normalizePatternsField("patterns", patterns)
}

func normalizePatternsField(field string, patterns []string) ([]string, error) {
	normalized := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for i, pattern := range patterns {
		cleaned := strings.TrimSpace(filepath.ToSlash(pattern))
		cleaned = strings.ReplaceAll(cleaned, "\\", "/")
		cleaned = strings.TrimPrefix(cleaned, "./")
		if cleaned == "" {
			return nil, fmt.Errorf("%s[%d] is empty", field, i)
		}
		if !doublestar.ValidatePathPattern(cleaned) {
			return nil, fmt.Errorf("%s[%d] is invalid glob %q", field, i, pattern)
		}
		if _, ok := seen[cleaned]; ok {
			return nil, fmt.Errorf("%s[%d] duplicates %q", field, i, cleaned)
		}
		seen[cleaned] = struct{}{}
		normalized = append(normalized, cleaned)
	}
	return normalized, nil
}

func normalizeRespectGitignore(value *bool) *bool {
	if value == nil || *value {
		return nil
	}
	falseValue := false
	return &falseValue
}

// CleanNamespace validates a sidecar namespace and returns its canonical
// slash-separated form.
func CleanNamespace(namespace string) (string, error) {
	trimmed := strings.TrimSpace(namespace)
	if trimmed == "" {
		return "", nil
	}
	if strings.Contains(trimmed, "\\") {
		return "", errors.New("namespace must use slash-separated path segments")
	}
	if path.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") {
		return "", errors.New("namespace must be relative")
	}
	if strings.HasPrefix(trimmed, "./") || strings.HasSuffix(trimmed, "/") ||
		strings.Contains(trimmed, "//") {
		return "", fmt.Errorf("namespace %q is not a clean relative path", namespace)
	}
	cleaned := path.Clean(trimmed)
	if cleaned != trimmed {
		return "", fmt.Errorf("namespace %q is not a clean relative path", namespace)
	}
	for segment := range strings.SplitSeq(cleaned, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("namespace contains invalid segment %q", segment)
		}
		if strings.HasPrefix(segment, ".") {
			return "", fmt.Errorf("namespace segment %q cannot start with dot", segment)
		}
		if segment == NamespaceBranchSegment {
			return "", fmt.Errorf("namespace segment %q is reserved", NamespaceBranchSegment)
		}
		if reservedDirectorySegment(segment) {
			return "", fmt.Errorf("namespace segment %q is reserved for Git internals", segment)
		}
		if !safeDirectorySegment(segment) {
			return "", fmt.Errorf("namespace segment %q contains unsupported characters", segment)
		}
	}
	return cleaned, nil
}

// Owns reports whether path belongs to namespace after applying excludes.
func (n Namespace) Owns(path string) bool {
	return n.Includes(path) && !matchesAny(path, n.Exclude)
}

// RespectsGitignore reports whether Git ignore sources should prune matches.
func (n Namespace) RespectsGitignore() bool {
	return n.RespectGitignore == nil || *n.RespectGitignore
}

// Includes reports whether path matches namespace's include patterns.
func (n Namespace) Includes(path string) bool {
	return matchesAny(path, n.Patterns)
}

func matchesAny(filePath string, patterns []string) bool {
	normalizedPath := filepath.ToSlash(filePath)
	for _, pattern := range patterns {
		ok, err := doublestar.PathMatch(filepath.ToSlash(pattern), normalizedPath)
		if err == nil && ok {
			return true
		}
	}
	return false
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

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '_':
		default:
			return false
		}
	}
	return true
}
