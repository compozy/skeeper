package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const hydrationFile = "hydration.json"

// HydrationJournal records the sidecar blobs last materialized into the main worktree.
type HydrationJournal struct {
	Version      int                           `json:"version"`
	SourceBranch string                        `json:"source_branch"`
	Namespaces   map[string]HydrationNamespace `json:"namespaces"`
	UpdatedAt    time.Time                     `json:"updated_at"`
}

// HydrationNamespace records one namespace's last hydrated lock commit.
type HydrationNamespace struct {
	LockCommit string                   `json:"lock_commit"`
	Files      map[string]HydrationFile `json:"files"`
}

// HydrationFile records the sidecar blob and content digest for one hydrated file.
type HydrationFile struct {
	SidecarBlob string `json:"sidecar_blob,omitempty"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

// LoadHydration returns the current hydration journal if it exists.
func (s *Store) LoadHydration(ctx context.Context) (HydrationJournal, bool, error) {
	if err := ctx.Err(); err != nil {
		return HydrationJournal{}, false, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir, hydrationFile))
	if err != nil {
		if os.IsNotExist(err) {
			return HydrationJournal{}, false, nil
		}
		return HydrationJournal{}, false, fmt.Errorf("read hydration journal: %w", err)
	}
	var journal HydrationJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return HydrationJournal{}, false, fmt.Errorf("decode hydration journal: %w", err)
	}
	if journal.Version != 1 {
		return HydrationJournal{}, false, fmt.Errorf("unsupported hydration journal version %d", journal.Version)
	}
	if journal.Namespaces == nil {
		journal.Namespaces = map[string]HydrationNamespace{}
	}
	for name, namespace := range journal.Namespaces {
		if namespace.Files == nil {
			namespace.Files = map[string]HydrationFile{}
			journal.Namespaces[name] = namespace
		}
	}
	return journal, true, nil
}

// WriteHydration replaces the local hydration journal atomically.
func (s *Store) WriteHydration(ctx context.Context, journal HydrationJournal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	journal.Version = 1
	if journal.Namespaces == nil {
		journal.Namespaces = map[string]HydrationNamespace{}
	}
	journal.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(canonicalHydrationJournal(journal), "", "  ")
	if err != nil {
		return fmt.Errorf("encode hydration journal: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(s.dir, hydrationFile), append(data, '\n')); err != nil {
		return fmt.Errorf("write hydration journal: %w", err)
	}
	return nil
}

func canonicalHydrationJournal(journal HydrationJournal) HydrationJournal {
	// JSON object key ordering is deterministic in encoding/json, but rebuilding the
	// nested maps prevents nil maps from changing shape across runs.
	next := HydrationJournal{
		Version:      journal.Version,
		SourceBranch: journal.SourceBranch,
		Namespaces:   map[string]HydrationNamespace{},
		UpdatedAt:    journal.UpdatedAt,
	}
	namespaceNames := make([]string, 0, len(journal.Namespaces))
	for name := range journal.Namespaces {
		namespaceNames = append(namespaceNames, name)
	}
	sort.Strings(namespaceNames)
	for _, name := range namespaceNames {
		namespace := journal.Namespaces[name]
		files := map[string]HydrationFile{}
		fileNames := make([]string, 0, len(namespace.Files))
		for path := range namespace.Files {
			fileNames = append(fileNames, path)
		}
		sort.Strings(fileNames)
		for _, path := range fileNames {
			files[path] = namespace.Files[path]
		}
		next.Namespaces[name] = HydrationNamespace{
			LockCommit: namespace.LockCommit,
			Files:      files,
		}
	}
	return next
}
