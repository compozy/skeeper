package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const rescueDir = "rescue"

// RescueManifest records files moved aside before prune or overwrite operations.
type RescueManifest struct {
	ID        string       `json:"id"`
	CreatedAt time.Time    `json:"created_at"`
	Operation string       `json:"operation"`
	Root      string       `json:"root"`
	Files     []RescueFile `json:"files"`
}

// RescueFile records one rescued file.
type RescueFile struct {
	OriginalPath string `json:"original_path"`
	RescuePath   string `json:"rescue_path"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
	Class        string `json:"class,omitempty"`
}

// RescueCandidate describes a file that should be moved into rescue.
type RescueCandidate struct {
	Path  string
	Class string
}

// CreateRescue moves candidates from root into a new rescue directory.
func (s *Store) CreateRescue(
	ctx context.Context,
	root string,
	operation string,
	candidates []RescueCandidate,
) (RescueManifest, error) {
	if err := ctx.Err(); err != nil {
		return RescueManifest{}, err
	}
	if len(candidates) == 0 {
		return RescueManifest{}, nil
	}
	if err := s.ensureDir(); err != nil {
		return RescueManifest{}, err
	}
	id := time.Now().UTC().Format("20060102T150405.000000000Z")
	manifest := RescueManifest{
		ID:        id,
		CreatedAt: time.Now().UTC(),
		Operation: operation,
		Root:      root,
	}
	base := filepath.Join(s.dir, rescueDir, id)
	for _, candidate := range candidates {
		rel, err := cleanRescuePath(candidate.Path)
		if err != nil {
			return RescueManifest{}, err
		}
		src := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return RescueManifest{}, fmt.Errorf("stat rescue candidate %s: %w", rel, err)
		}
		if info.IsDir() {
			return RescueManifest{}, fmt.Errorf("rescue candidate %s is a directory", rel)
		}
		sum, bytes, err := fileSHA256(src)
		if err != nil {
			return RescueManifest{}, err
		}
		dst := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return RescueManifest{}, fmt.Errorf("create rescue parent for %s: %w", rel, err)
		}
		if err := os.Rename(src, dst); err != nil {
			return RescueManifest{}, fmt.Errorf("move %s to rescue: %w", rel, err)
		}
		manifest.Files = append(manifest.Files, RescueFile{
			OriginalPath: rel,
			RescuePath:   filepath.ToSlash(filepath.Join(rescueDir, id, filepath.FromSlash(rel))),
			SHA256:       sum,
			Bytes:        bytes,
			Class:        candidate.Class,
		})
	}
	if len(manifest.Files) == 0 {
		return RescueManifest{}, nil
	}
	if err := s.writeRescueManifest(manifest); err != nil {
		return RescueManifest{}, err
	}
	return manifest, nil
}

// ListRescues returns every rescue manifest, newest first.
func (s *Store) ListRescues(ctx context.Context) ([]RescueManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base := filepath.Join(s.dir, rescueDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read rescue directory: %w", err)
	}
	manifests := make([]RescueManifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := s.loadRescueManifest(entry.Name())
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].CreatedAt.After(manifests[j].CreatedAt)
	})
	return manifests, nil
}

// RestoreRescue restores paths from one rescue manifest.
func (s *Store) RestoreRescue(
	ctx context.Context,
	root string,
	id string,
	paths []string,
	overwrite bool,
) (RescueManifest, error) {
	if err := ctx.Err(); err != nil {
		return RescueManifest{}, err
	}
	manifest, err := s.loadRescueManifest(id)
	if err != nil {
		return RescueManifest{}, err
	}
	selected := map[string]struct{}{}
	for _, path := range paths {
		rel, err := cleanRescuePath(path)
		if err != nil {
			return RescueManifest{}, err
		}
		selected[rel] = struct{}{}
	}
	restored := RescueManifest{
		ID:        manifest.ID,
		CreatedAt: manifest.CreatedAt,
		Operation: manifest.Operation,
		Root:      manifest.Root,
	}
	for _, file := range manifest.Files {
		originalPath, rescuePath, err := validateRescueFile(manifest.ID, file)
		if err != nil {
			return RescueManifest{}, err
		}
		if len(selected) > 0 {
			if _, ok := selected[originalPath]; !ok {
				continue
			}
		}
		src := filepath.Join(s.dir, filepath.FromSlash(rescuePath))
		sum, bytes, err := fileSHA256(src)
		if err != nil {
			return RescueManifest{}, err
		}
		if sum != file.SHA256 || bytes != file.Bytes {
			return RescueManifest{}, fmt.Errorf("rescue file %s checksum mismatch", originalPath)
		}
		dst := filepath.Join(root, filepath.FromSlash(originalPath))
		if !overwrite {
			if _, err := os.Stat(dst); err == nil {
				return RescueManifest{}, fmt.Errorf(
					"%s already exists; pass --overwrite to replace it",
					originalPath,
				)
			} else if !os.IsNotExist(err) {
				return RescueManifest{}, fmt.Errorf("stat restore target %s: %w", originalPath, err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return RescueManifest{}, fmt.Errorf("create restore parent for %s: %w", originalPath, err)
		}
		if err := copyFile(src, dst); err != nil {
			return RescueManifest{}, err
		}
		restored.Files = append(restored.Files, file)
	}
	return restored, nil
}

func (s *Store) writeRescueManifest(manifest RescueManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode rescue manifest: %w", err)
	}
	path := filepath.Join(s.dir, rescueDir, manifest.ID, "manifest.json")
	if err := atomicWriteFile(path, append(data, '\n')); err != nil {
		return fmt.Errorf("write rescue manifest: %w", err)
	}
	return nil
}

func (s *Store) loadRescueManifest(id string) (RescueManifest, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." ||
		strings.Contains(id, "/") ||
		strings.Contains(id, string(filepath.Separator)) {
		return RescueManifest{}, fmt.Errorf("invalid rescue id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, rescueDir, id, "manifest.json"))
	if err != nil {
		return RescueManifest{}, fmt.Errorf("read rescue manifest %s: %w", id, err)
	}
	var manifest RescueManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return RescueManifest{}, fmt.Errorf("decode rescue manifest %s: %w", id, err)
	}
	return manifest, nil
}

func validateRescueFile(id string, file RescueFile) (string, string, error) {
	originalPath, err := cleanRescuePath(file.OriginalPath)
	if err != nil {
		return "", "", fmt.Errorf("invalid rescue original path %q: %w", file.OriginalPath, err)
	}
	rescuePath, err := cleanRescuePath(file.RescuePath)
	if err != nil {
		return "", "", fmt.Errorf("invalid rescue stored path %q: %w", file.RescuePath, err)
	}
	expectedPrefix := filepath.ToSlash(filepath.Join(rescueDir, id)) + "/"
	if !strings.HasPrefix(rescuePath, expectedPrefix) {
		return "", "", fmt.Errorf("rescue stored path %q escapes rescue %s", file.RescuePath, id)
	}
	return originalPath, rescuePath, nil
}

func cleanRescuePath(path string) (string, error) {
	rel := filepath.ToSlash(strings.TrimSpace(path))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid rescue path %q", path)
	}
	cleaned := filepath.ToSlash(filepath.Clean(rel))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid rescue path %q", path)
	}
	return cleaned, nil
}

func fileSHA256(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s for checksum: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	bytes, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), bytes, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open rescue source %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Clean(dst), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open rescue target %s: %w", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("restore rescue file %s: %w", dst, err)
	}
	return nil
}
