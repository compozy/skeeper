// Package state stores local-only skeeper operational state.
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	transactionFile = "transaction.json"
	bypassFile      = "bypass.json"
)

// TransactionPhase identifies a durable mutating operation phase.
type TransactionPhase string

const (
	// TransactionPhasePlanned records a plan before side effects.
	TransactionPhasePlanned TransactionPhase = "planned"
	// TransactionPhaseSidecarPushed records that sidecar content is durable remotely.
	TransactionPhaseSidecarPushed TransactionPhase = "sidecar_pushed"
	// TransactionPhaseMainIndexMutated records that the main Git index was mutated.
	TransactionPhaseMainIndexMutated TransactionPhase = "main_index_mutated"
	// TransactionPhaseLockStaged records that skeeper.lock was written and staged.
	TransactionPhaseLockStaged TransactionPhase = "lock_staged"
)

// Transaction records a resumable multi-step operation.
type Transaction struct {
	ID               string           `json:"id"`
	Kind             string           `json:"kind"`
	Phase            TransactionPhase `json:"phase"`
	Root             string           `json:"root"`
	Targets          []string         `json:"targets,omitempty"`
	Namespaces       []string         `json:"namespaces,omitempty"`
	MainIndexMutated bool             `json:"main_index_mutated"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// Bypass records an audited strict-hook bypass.
type Bypass struct {
	Time    time.Time `json:"time"`
	Env     string    `json:"env"`
	MainSHA string    `json:"main_sha"`
	Reason  string    `json:"reason"`
}

// TransactionStore persists active transaction state.
type TransactionStore interface {
	Begin(ctx context.Context, tx Transaction) error
	Current(ctx context.Context) (Transaction, bool, error)
	MarkPhase(ctx context.Context, id string, phase TransactionPhase) error
	Complete(ctx context.Context, id string) error
	Abort(ctx context.Context, id string) error
}

// Store manages local transaction and bypass journals under .git/skeeper.
type Store struct {
	dir string
}

// New returns a Store rooted at dir.
func New(dir string) *Store {
	return &Store{dir: dir}
}

var _ TransactionStore = (*Store)(nil)

// Begin stores a new active transaction and rejects concurrent transactions.
func (s *Store) Begin(ctx context.Context, tx Transaction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if current, ok, err := s.Current(ctx); err != nil {
		return err
	} else if ok {
		return fmt.Errorf(
			"transaction %s already active in phase %s; run `skeeper repair --check`",
			current.ID,
			current.Phase,
		)
	}
	if tx.ID == "" {
		tx.ID = fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	now := time.Now().UTC()
	if tx.CreatedAt.IsZero() {
		tx.CreatedAt = now
	}
	tx.UpdatedAt = now
	if tx.Phase == "" {
		tx.Phase = TransactionPhasePlanned
	}
	if !tx.Phase.Valid() {
		return fmt.Errorf("unknown transaction phase %q", tx.Phase)
	}
	return s.writeTransaction(tx)
}

// Current returns the active transaction if one exists.
func (s *Store) Current(ctx context.Context) (Transaction, bool, error) {
	if err := ctx.Err(); err != nil {
		return Transaction{}, false, err
	}
	path := filepath.Join(s.dir, transactionFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Transaction{}, false, nil
		}
		return Transaction{}, false, fmt.Errorf("read transaction journal: %w", err)
	}
	var tx Transaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return Transaction{}, false, fmt.Errorf("decode transaction journal: %w", err)
	}
	return tx, true, nil
}

// MarkPhase advances an active transaction phase.
func (s *Store) MarkPhase(ctx context.Context, id string, phase TransactionPhase) error {
	if !phase.Valid() {
		return fmt.Errorf("unknown transaction phase %q", phase)
	}
	tx, ok, err := s.Current(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("transaction %s is not active", id)
	}
	if tx.ID != id {
		return fmt.Errorf("active transaction is %s, not %s", tx.ID, id)
	}
	tx.Phase = phase
	tx.UpdatedAt = time.Now().UTC()
	if phase == TransactionPhaseMainIndexMutated {
		tx.MainIndexMutated = true
	}
	return s.writeTransaction(tx)
}

// Complete removes a completed transaction.
func (s *Store) Complete(ctx context.Context, id string) error {
	tx, ok, err := s.Current(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if tx.ID != id {
		return fmt.Errorf("active transaction is %s, not %s", tx.ID, id)
	}
	return s.removeTransaction()
}

// Abort removes a transaction before main index mutation.
func (s *Store) Abort(ctx context.Context, id string) error {
	tx, ok, err := s.Current(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if tx.ID != id {
		return fmt.Errorf("active transaction is %s, not %s", tx.ID, id)
	}
	if tx.MainIndexMutated {
		return fmt.Errorf("transaction %s already mutated the main index; inspect files manually", id)
	}
	return s.removeTransaction()
}

// RecordBypass writes the latest audited bypass.
func (s *Store) RecordBypass(ctx context.Context, bypass Bypass) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	if bypass.Time.IsZero() {
		bypass.Time = time.Now().UTC()
	}
	data, err := json.MarshalIndent(bypass, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bypass journal: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(s.dir, bypassFile), append(data, '\n')); err != nil {
		return fmt.Errorf("write bypass journal: %w", err)
	}
	return nil
}

// Bypass returns the latest audited bypass if one exists.
func (s *Store) Bypass(ctx context.Context) (Bypass, bool, error) {
	if err := ctx.Err(); err != nil {
		return Bypass{}, false, err
	}
	path := filepath.Join(s.dir, bypassFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Bypass{}, false, nil
		}
		return Bypass{}, false, fmt.Errorf("read bypass journal: %w", err)
	}
	var bypass Bypass
	if err := json.Unmarshal(data, &bypass); err != nil {
		return Bypass{}, false, fmt.Errorf("decode bypass journal: %w", err)
	}
	return bypass, true, nil
}

// ClearBypass removes the bypass journal.
func (s *Store) ClearBypass(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.dir, bypassFile)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear bypass journal: %w", err)
	}
	return nil
}

func (s *Store) writeTransaction(tx Transaction) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		return fmt.Errorf("encode transaction journal: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(s.dir, transactionFile), append(data, '\n')); err != nil {
		return fmt.Errorf("write transaction journal: %w", err)
	}
	return nil
}

func (s *Store) removeTransaction() error {
	if err := os.Remove(filepath.Join(s.dir, transactionFile)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove transaction journal: %w", err)
	}
	return nil
}

func (s *Store) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create skeeper state dir: %w", err)
	}
	return nil
}

func atomicWriteFile(path string, data []byte) error {
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
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// Valid reports whether phase is part of the transaction state machine.
func (p TransactionPhase) Valid() bool {
	switch p {
	case TransactionPhasePlanned,
		TransactionPhaseSidecarPushed,
		TransactionPhaseMainIndexMutated,
		TransactionPhaseLockStaged:
		return true
	default:
		return false
	}
}
