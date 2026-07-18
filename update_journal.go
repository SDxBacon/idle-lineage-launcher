package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const updateJournalSchemaVersion = 1

// updateJournalRenameSyncError means the atomic rename succeeded and the
// requested journal is visible in the live filesystem, but durability of the
// parent-directory entry could not be confirmed.
type updateJournalRenameSyncError struct {
	err error
}

func (failure *updateJournalRenameSyncError) Error() string { return failure.err.Error() }
func (failure *updateJournalRenameSyncError) Unwrap() error { return failure.err }

func isUpdateJournalRenameSyncError(err error) bool {
	var target *updateJournalRenameSyncError
	return errors.As(err, &target)
}

type updateJournalStrategy string

const (
	updateJournalStrategyInPlace updateJournalStrategy = "in_place"
	updateJournalStrategyReplace updateJournalStrategy = "replace"
)

type updateJournalPhase string

const (
	updateJournalPhasePrepared  updateJournalPhase = "prepared"
	updateJournalPhaseCommitted updateJournalPhase = "committed"
)

type updateJournal struct {
	SchemaVersion int                   `json:"schemaVersion"`
	GameRoot      string                `json:"gameRoot"`
	FromCommit    string                `json:"fromCommit"`
	TargetCommit  string                `json:"targetCommit"`
	Strategy      updateJournalStrategy `json:"strategy"`
	Phase         updateJournalPhase    `json:"phase"`
}

type updateJournalStore struct {
	mu               sync.Mutex
	path             string
	expectedGameRoot string
	syncDirectory    func(string) error
}

func newUpdateJournalStore(paths dataPaths) *updateJournalStore {
	path := paths.UpdateJournal
	if path == "" && paths.Root != "" {
		path = filepath.Join(paths.Root, "update-journal.json")
	}
	return &updateJournalStore{path: path, expectedGameRoot: paths.GameRoot, syncDirectory: syncUpdateJournalDirectory}
}

// Load returns nil when no update journal exists. A journal that cannot be
// decoded or validated is deliberately left on disk so recovery can be retried
// after the underlying problem has been inspected or corrected.
func (store *updateJournalStore) Load() (*updateJournal, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	contents, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read update journal: %w", err)
	}

	journal, err := decodeUpdateJournal(contents)
	if err != nil {
		return nil, err
	}
	if err := store.validate(journal); err != nil {
		return nil, err
	}

	// Only expose the configured path to recovery callers. The on-disk path is
	// used solely to prove that the record belongs to this game installation.
	journal.GameRoot = store.expectedGameRoot
	return &journal, nil
}

func (store *updateJournalStore) Save(journal updateJournal) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	journal.SchemaVersion = updateJournalSchemaVersion
	if err := store.validate(journal); err != nil {
		return err
	}
	journal.GameRoot = store.expectedGameRoot

	contents, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update journal: %w", err)
	}
	contents = append(contents, '\n')

	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create update journal directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".update-journal-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary update journal: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary update journal: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write temporary update journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("flush temporary update journal: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary update journal: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("replace update journal: %w", err)
	}
	committed = true
	if err := store.flushDirectory(directory); err != nil {
		return &updateJournalRenameSyncError{err: err}
	}
	return nil
}

func (store *updateJournalStore) Clear() error {
	store.mu.Lock()
	defer store.mu.Unlock()

	err := os.Remove(store.path)
	if errors.Is(err, os.ErrNotExist) {
		directory := filepath.Dir(store.path)
		if _, statErr := os.Stat(directory); errors.Is(statErr, os.ErrNotExist) {
			return nil
		} else if statErr != nil {
			return fmt.Errorf("inspect update journal directory before flush: %w", statErr)
		}
		return store.flushDirectory(directory)
	}
	if err != nil {
		return fmt.Errorf("remove update journal: %w", err)
	}
	return store.flushDirectory(filepath.Dir(store.path))
}

func (store *updateJournalStore) flushDirectory(directory string) error {
	if store.syncDirectory == nil {
		return syncUpdateJournalDirectory(directory)
	}
	return store.syncDirectory(directory)
}

func decodeUpdateJournal(contents []byte) (updateJournal, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()

	var journal updateJournal
	if err := decoder.Decode(&journal); err != nil {
		return updateJournal{}, fmt.Errorf("decode update journal: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return updateJournal{}, errors.New("decode update journal: multiple JSON values")
		}
		return updateJournal{}, fmt.Errorf("decode update journal trailing data: %w", err)
	}
	return journal, nil
}

func (store *updateJournalStore) validate(journal updateJournal) error {
	if store.path == "" {
		return errors.New("update journal path is empty")
	}
	if store.expectedGameRoot == "" {
		return errors.New("expected update journal game root is empty")
	}
	if journal.SchemaVersion != updateJournalSchemaVersion {
		return fmt.Errorf("unsupported update journal schema version %d", journal.SchemaVersion)
	}
	if journal.GameRoot == "" || !sameCleanPath(journal.GameRoot, store.expectedGameRoot) {
		return fmt.Errorf("update journal game root %q does not match configured game root", journal.GameRoot)
	}
	if !validUpdateJournalCommit(journal.FromCommit) {
		return fmt.Errorf("invalid update journal source commit %q", journal.FromCommit)
	}
	if !validUpdateJournalCommit(journal.TargetCommit) {
		return fmt.Errorf("invalid update journal target commit %q", journal.TargetCommit)
	}
	switch journal.Strategy {
	case updateJournalStrategyInPlace, updateJournalStrategyReplace:
	default:
		return fmt.Errorf("invalid update journal strategy %q", journal.Strategy)
	}
	switch journal.Phase {
	case updateJournalPhasePrepared, updateJournalPhaseCommitted:
	default:
		return fmt.Errorf("invalid update journal phase %q", journal.Phase)
	}
	return nil
}

func validUpdateJournalCommit(commit string) bool {
	if len(commit) != 40 {
		return false
	}
	decoded, err := hex.DecodeString(commit)
	if err != nil || len(decoded) != 20 {
		return false
	}
	for _, value := range decoded {
		if value != 0 {
			return true
		}
	}
	return false
}

// Directory Sync preserves a successful rename across power loss on Unix. The
// Windows API used by os.File.Sync does not support directory handles, so the
// file flush and atomic rename remain the strongest available guarantee there.
func syncUpdateJournalDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return fmt.Errorf("open update journal directory for flush: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("flush update journal directory: %w", err)
	}
	return nil
}
