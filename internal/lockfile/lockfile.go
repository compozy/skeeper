// Package lockfile owns skeeper.lock encoding, validation, and digesting.
package lockfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/managedblock"
	"github.com/compozy/skeeper/internal/reconcile"
)

const (
	// Filename is the committed root lockfile path.
	Filename = "skeeper.lock"
	// Version is the current lockfile schema version.
	Version = 1
)

// NamespaceDigest is a deterministic digest for one namespace tree.
type NamespaceDigest string

// Lock is the canonical JSON lockfile committed to the main repository.
type Lock struct {
	Version      int               `json:"version"`
	Sidecar      string            `json:"sidecar"`
	SourceBranch string            `json:"source_branch"`
	Namespaces   []NamespaceRecord `json:"namespaces"`
}

// NamespaceRecord points one namespace to the sidecar commit it expects.
type NamespaceRecord struct {
	Name          string          `json:"name"`
	SidecarBranch string          `json:"sidecar_branch"`
	Commit        string          `json:"commit"`
	Digest        NamespaceDigest `json:"digest"`
	Files         int             `json:"files"`
	Bytes         int64           `json:"bytes"`
}

// DigestResult describes a namespace digest and its scale.
type DigestResult struct {
	Digest NamespaceDigest
	Files  int
	Bytes  int64
}

// Store owns lockfile persistence and sidecar digests.
type Store interface {
	Load(root reconcile.RepoRoot) (Lock, error)
	Write(root reconcile.RepoRoot, lock Lock) error
	Digest(
		ctx context.Context,
		sidecarDir string,
		namespace config.Namespace,
		ref reconcile.SidecarRef,
	) (NamespaceDigest, error)
	RegenerateForMerge(ctx context.Context, root reconcile.RepoRoot) (Lock, error)
}

// JSONStore is the production lockfile store.
type JSONStore struct {
	runner gitexec.Runner
}

var _ Store = (*JSONStore)(nil)

// NewStore returns a JSON lockfile store.
func NewStore(runner gitexec.Runner) *JSONStore {
	return &JSONStore{runner: runner}
}

// Load reads and validates skeeper.lock.
func (s *JSONStore) Load(root reconcile.RepoRoot) (Lock, error) {
	path := filepath.Join(string(root), Filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Lock{}, fmt.Errorf("%s not found; run `skeeper sync`", Filename)
		}
		return Lock{}, fmt.Errorf("read %s: %w", Filename, err)
	}
	var lock Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		return Lock{}, fmt.Errorf("decode %s: %w", Filename, err)
	}
	if err := Validate(lock); err != nil {
		return Lock{}, err
	}
	return Normalize(lock), nil
}

// Write writes lock with canonical JSON encoding.
func (s *JSONStore) Write(root reconcile.RepoRoot, lock Lock) error {
	lock = Normalize(lock)
	if err := Validate(lock); err != nil {
		return err
	}
	data, err := Marshal(lock)
	if err != nil {
		return err
	}
	if err := managedblock.WriteFile(filepath.Join(string(root), Filename), data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", Filename, err)
	}
	return nil
}

// Digest calculates a namespace tree digest at ref.
func (s *JSONStore) Digest(
	ctx context.Context,
	sidecarDir string,
	namespace config.Namespace,
	ref reconcile.SidecarRef,
) (NamespaceDigest, error) {
	result, err := s.DigestResult(ctx, sidecarDir, namespace, ref)
	if err != nil {
		return "", err
	}
	return result.Digest, nil
}

// DigestResult calculates a namespace tree digest and scale at ref.
func (s *JSONStore) DigestResult(
	ctx context.Context,
	sidecarDir string,
	namespace config.Namespace,
	ref reconcile.SidecarRef,
) (DigestResult, error) {
	prefix := namespace.Name
	out, err := s.runner.Run(ctx, sidecarDir, "git", "ls-tree", "-r", "-z", string(ref), "--", prefix)
	if err != nil {
		return DigestResult{}, fmt.Errorf("list sidecar tree for namespace %q at %s: %w", namespace.Name, ref, err)
	}
	records, err := parseTreeRecords(out.Stdout, prefix)
	if err != nil {
		return DigestResult{}, err
	}
	if len(records) > 0 {
		if err := s.populateTreeRecordContent(ctx, sidecarDir, records); err != nil {
			return DigestResult{}, err
		}
	}
	return digestRecords(records), nil
}

func (s *JSONStore) populateTreeRecordContent(
	ctx context.Context,
	sidecarDir string,
	records []digestRecord,
) error {
	input := strings.Builder{}
	for _, record := range records {
		input.WriteString(record.Object)
		input.WriteByte('\n')
	}
	out, err := s.runner.RunWithInput(
		ctx,
		sidecarDir,
		input.String(),
		"git",
		"cat-file",
		"--batch",
	)
	if err != nil {
		return fmt.Errorf("read sidecar blobs with git cat-file: %w", err)
	}
	contents, err := parseCatFileBatch(out.Stdout, len(records))
	if err != nil {
		return err
	}
	for i, content := range contents {
		sum := sha256.Sum256([]byte(content))
		records[i].SHA256 = hex.EncodeToString(sum[:])
		records[i].Size = int64(len(content))
	}
	return nil
}

// RegenerateForMerge reloads the canonical lock after the merge driver updated it.
func (s *JSONStore) RegenerateForMerge(_ context.Context, root reconcile.RepoRoot) (Lock, error) {
	return s.Load(root)
}

// DigestWorkingTree calculates a digest from main-repo file contents.
func DigestWorkingTree(root string, files []string, staged map[string]string) (DigestResult, error) {
	records := make([]digestRecord, 0, len(files))
	for _, file := range files {
		content, ok := staged[file]
		if !ok {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
			if err != nil {
				return DigestResult{}, fmt.Errorf("read %s for digest: %w", file, err)
			}
			content = string(data)
		}
		sum := sha256.Sum256([]byte(content))
		records = append(records, digestRecord{
			Path:   filepath.ToSlash(file),
			Size:   int64(len(content)),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	return digestRecords(records), nil
}

// Marshal returns canonical JSON bytes for lock.
func Marshal(lock Lock) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(Normalize(lock)); err != nil {
		return nil, fmt.Errorf("encode %s: %w", Filename, err)
	}
	return buf.Bytes(), nil
}

// Normalize canonicalizes lock field order and URL representation.
func Normalize(lock Lock) Lock {
	lock.Sidecar = CanonicalSidecarURL(lock.Sidecar)
	sort.Slice(lock.Namespaces, func(i, j int) bool {
		return lock.Namespaces[i].Name < lock.Namespaces[j].Name
	})
	return lock
}

// Validate checks lock schema invariants.
func Validate(lock Lock) error {
	if lock.Version != Version {
		return fmt.Errorf("%s version must be %d, got %d", Filename, Version, lock.Version)
	}
	if strings.TrimSpace(lock.Sidecar) == "" {
		return fmt.Errorf("%s sidecar is required", Filename)
	}
	if strings.TrimSpace(lock.SourceBranch) == "" {
		return fmt.Errorf("%s source_branch is required", Filename)
	}
	seen := map[string]struct{}{}
	for i, namespace := range lock.Namespaces {
		cleaned, err := config.CleanNamespace(namespace.Name)
		if err != nil {
			return fmt.Errorf("namespaces[%d].name: %w", i, err)
		}
		if cleaned == "" {
			return fmt.Errorf("namespaces[%d].name is required", i)
		}
		if _, ok := seen[namespace.Name]; ok {
			return fmt.Errorf("namespaces[%d].name duplicates %q", i, namespace.Name)
		}
		seen[namespace.Name] = struct{}{}
		if strings.TrimSpace(namespace.SidecarBranch) == "" {
			return fmt.Errorf("namespaces[%d].sidecar_branch is required", i)
		}
		if !isFullHexSHA(namespace.Commit) {
			return fmt.Errorf("namespaces[%d].commit must be a full 40-character SHA", i)
		}
		if !isFullSHA256Digest(namespace.Digest) {
			return fmt.Errorf("namespaces[%d].digest must be sha256 followed by 64 hex characters", i)
		}
		if namespace.Files < 0 || namespace.Bytes < 0 {
			return fmt.Errorf("namespaces[%d] files and bytes must be non-negative", i)
		}
	}
	return nil
}

// CanonicalSidecarURL canonicalizes sidecar URLs for lock comparison.
func CanonicalSidecarURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "git@github.com:") {
		return ensureGitSuffix(trimmed)
	}
	if after, ok := strings.CutPrefix(trimmed, "https://github.com/"); ok {
		repo := after
		repo = strings.TrimSuffix(repo, ".git")
		return "git@github.com:" + repo + ".git"
	}
	if after, ok := strings.CutPrefix(trimmed, "ssh://git@github.com/"); ok {
		repo := after
		repo = strings.TrimSuffix(repo, ".git")
		return "git@github.com:" + repo + ".git"
	}
	return filepath.Clean(trimmed)
}

func ensureGitSuffix(value string) string {
	if strings.HasSuffix(value, ".git") {
		return value
	}
	return value + ".git"
}

type digestRecord struct {
	Path        string
	Size        int64
	SHA256      string
	Object      string
	sidecarPath string
}

func parseTreeRecords(output, namespace string) ([]digestRecord, error) {
	records := make([]digestRecord, 0)
	prefix := strings.TrimSuffix(namespace, "/") + "/"
	for raw := range strings.SplitSeq(output, "\x00") {
		if raw == "" {
			continue
		}
		header, path, ok := strings.Cut(raw, "\t")
		if !ok {
			return nil, fmt.Errorf("parse git ls-tree record %q", raw)
		}
		fields := strings.Fields(header)
		if len(fields) < 3 || fields[1] != "blob" {
			continue
		}
		mainPath := strings.TrimPrefix(filepath.ToSlash(path), prefix)
		if mainPath == path {
			continue
		}
		records = append(records, digestRecord{
			Path:        mainPath,
			Object:      fields[2],
			sidecarPath: filepath.ToSlash(path),
		})
	}
	return records, nil
}

func parseCatFileBatch(output string, count int) ([]string, error) {
	contents := make([]string, 0, count)
	offset := 0
	for range count {
		newline := strings.IndexByte(output[offset:], '\n')
		if newline == -1 {
			return nil, fmt.Errorf("parse git cat-file batch header at offset %d", offset)
		}
		header := output[offset : offset+newline]
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("parse git cat-file batch header %q", header)
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse git cat-file blob size: %w", err)
		}
		start := offset + newline + 1
		end := start + int(size)
		if end > len(output) {
			return nil, fmt.Errorf("git cat-file blob content truncated at offset %d", start)
		}
		contents = append(contents, output[start:end])
		offset = end
		if offset < len(output) && output[offset] == '\n' {
			offset++
		}
	}
	return contents, nil
}

func digestRecords(records []digestRecord) DigestResult {
	sort.Slice(records, func(i, j int) bool {
		return records[i].Path < records[j].Path
	})
	hash := sha256.New()
	result := DigestResult{Files: len(records)}
	for _, record := range records {
		result.Bytes += record.Size
		hash.Write([]byte(record.Path))
		hash.Write([]byte{0})
		hash.Write([]byte(strconv.FormatInt(record.Size, 10)))
		hash.Write([]byte{0})
		hash.Write([]byte(record.SHA256))
		hash.Write([]byte{'\n'})
	}
	result.Digest = NamespaceDigest("sha256:" + hex.EncodeToString(hash.Sum(nil)))
	return result
}

func isFullHexSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	nonZero := false
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			nonZero = nonZero || r != '0'
		case r >= 'a' && r <= 'f':
			nonZero = true
		case r >= 'A' && r <= 'F':
			nonZero = true
		default:
			return false
		}
	}
	return nonZero
}

func isFullSHA256Digest(value NamespaceDigest) bool {
	const prefix = "sha256:"
	raw := string(value)
	if !strings.HasPrefix(raw, prefix) || len(raw) != len(prefix)+64 {
		return false
	}
	for _, r := range raw[len(prefix):] {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
