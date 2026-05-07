// Package state stores local-only skeeper operational state.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	queueFile = "queue.json"
	logFile   = "sync.log"
)

// Entry records a queued sync retry.
type Entry struct {
	Time      time.Time `json:"time"`
	Reason    string    `json:"reason"`
	MainSHA   string    `json:"main_sha"`
	Namespace string    `json:"namespace,omitempty"`
}

// Store manages queue and log files under .git/skeeper.
type Store struct {
	dir string
}

// New returns a Store rooted at dir.
func New(dir string) *Store {
	return &Store{dir: dir}
}

// Enqueue appends a retry entry.
func (s *Store) Enqueue(entry Entry) error {
	entries, err := s.Queue()
	if err != nil {
		return err
	}
	entries = append(entries, entry)
	if err := s.ensureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sync queue: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(s.dir, queueFile), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write sync queue: %w", err)
	}
	return nil
}

// Queue returns all queued sync entries.
func (s *Store) Queue() ([]Entry, error) {
	path := filepath.Join(s.dir, queueFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sync queue: %w", err)
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode sync queue: %w", err)
	}
	return entries, nil
}

// ClearQueue removes queued retries.
func (s *Store) ClearQueue() error {
	path := filepath.Join(s.dir, queueFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear sync queue: %w", err)
	}
	return nil
}

// AppendLog writes one human-readable sync log line.
func (s *Store) AppendLog(line string) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	entry := time.Now().UTC().Format(time.RFC3339) + " " + line + "\n"
	if err := appendFile(filepath.Join(s.dir, logFile), []byte(entry)); err != nil {
		return fmt.Errorf("append sync log: %w", err)
	}
	return nil
}

func (s *Store) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create skeeper state dir: %w", err)
	}
	return nil
}

func appendFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := file.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := file.Chmod(perm); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
