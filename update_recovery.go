package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

const updateRecoverySteps = 2

var errUpdateRecoveryRequired = errors.New("update recovery is required")

type updateRecoveryResult struct {
	commit     string
	commitTime string
	message    string
}

func (m *gameManager) updateRecoveryPending() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pendingJournal != nil || m.journalLoadError != nil || m.state.Status == StatusRecovering || m.state.Status == StatusRecoveryFailed
}

func (m *gameManager) StartPendingUpdateRecovery() bool {
	m.mu.RLock()
	pending := m.pendingJournal != nil || m.journalLoadError != nil || m.state.Status == StatusRecovering || m.state.Status == StatusRecoveryFailed
	m.mu.RUnlock()
	if !pending {
		return false
	}
	if err := m.RetryUpdateRecovery(); err != nil {
		m.logger.Error("automatic update recovery could not start", "error", err)
	}
	return true
}

// RetryUpdateRecovery starts (or restarts) recovery without blocking the UI.
// Recovery never observes the update cancellation context because every phase
// may be changing which installation is safe to launch.
func (m *gameManager) RetryUpdateRecovery() error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	store := m.journalStore
	if store == nil {
		store = newUpdateJournalStore(m.paths)
		m.journalStore = store
	}
	journal := m.pendingJournal
	m.mu.Unlock()

	if journal == nil {
		loaded, err := store.Load()
		if err != nil {
			m.setUpdateRecoveryFailure(nil, fmt.Errorf("load update recovery journal: %w", err))
			return err
		}
		if loaded == nil {
			return m.finishRecoveryWithoutJournal()
		}
		journal = loaded
	}

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.pendingJournal = journal
	m.journalLoadError = nil
	m.activeRoot = ""
	m.running = true
	m.state = GameState{
		Status:              StatusRecovering,
		Message:             "正在復原上次未完成的更新…",
		ProgressPhase:       "復原安全版本",
		ProgressText:        "正在還原可安全啟動的遊戲版本…",
		ProgressPercent:     -1,
		ProgressStep:        1,
		ProgressStepTotal:   updateRecoverySteps,
		ProgressCancellable: false,
	}
	m.startRecoveryProgressLocked(time.Now())
	m.advanceRevisionLocked()
	state := m.state
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish(state)

	journalCopy := *journal
	go func() {
		defer m.wg.Done()
		result, err := m.recoverUpdateJournal(journalCopy)
		m.finishUpdateRecovery(result, err)
	}()
	return nil
}

func (m *gameManager) finishRecoveryWithoutJournal() error {
	state, activeRoot, err := inspectGameInstallation(m.paths, m.logger)
	if err != nil {
		m.setUpdateRecoveryFailure(nil, fmt.Errorf("inspect game after recovery journal disappeared: %w", err))
		return err
	}
	m.mu.Lock()
	m.pendingJournal = nil
	m.journalLoadError = nil
	m.activeRoot = activeRoot
	m.state = state
	m.advanceRevisionLocked()
	state = m.state
	m.mu.Unlock()
	m.publish(state)
	return nil
}

func (m *gameManager) startRecoveryProgressLocked(now time.Time) {
	m.stopOperationProgressLocked()
	m.progressSequence++
	m.operationProgress = &gameOperationProgress{
		sequence:     m.progressSequence,
		started:      now,
		lastActivity: now,
		network:      false,
		stop:         make(chan struct{}),
	}
}

func (m *gameManager) setRecoveryProgress(step int, phase, text string) {
	m.mu.Lock()
	if m.state.Status != StatusRecovering || m.operationProgress == nil {
		m.mu.Unlock()
		return
	}
	m.state.ProgressStep = step
	m.state.ProgressStepTotal = updateRecoverySteps
	m.state.ProgressPhase = phase
	m.state.ProgressText = text
	m.state.ProgressPercent = -1
	m.state.ProgressCancellable = false
	m.state.ProgressSeconds = elapsedSeconds(m.operationProgress.started, time.Now())
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) recoverUpdateJournal(journal updateJournal) (updateRecoveryResult, error) {
	var result updateRecoveryResult
	var err error
	switch journal.Strategy {
	case updateJournalStrategyInPlace:
		result, err = m.recoverInPlaceUpdate(journal)
	case updateJournalStrategyReplace:
		result, err = m.recoverReplacementUpdate(journal)
	default:
		err = fmt.Errorf("unsupported update recovery strategy %q", journal.Strategy)
	}
	if err != nil {
		return updateRecoveryResult{}, err
	}

	m.setRecoveryProgress(2, "驗證遊戲檔案", "正在確認復原後的遊戲檔案…")
	commitTime, err := validateInstalledCommit(m.paths.Source, plumbing.NewHash(result.commit))
	if err != nil {
		return updateRecoveryResult{}, fmt.Errorf("validate recovered game revision %s: %w", result.commit, err)
	}
	result.commitTime = commitTime
	if err := m.cleanupUpdateStaging(); err != nil {
		return updateRecoveryResult{}, err
	}
	if err := m.journalStore.Clear(); err != nil {
		return updateRecoveryResult{}, err
	}
	return result, nil
}

func (m *gameManager) recoverInPlaceUpdate(journal updateJournal) (updateRecoveryResult, error) {
	if journal.Phase == updateJournalPhaseCommitted {
		if commitTime, err := validateInstalledCommit(m.paths.Source, plumbing.NewHash(journal.TargetCommit)); err == nil {
			return updateRecoveryResult{
				commit:     journal.TargetCommit,
				commitTime: commitTime,
				message:    "上次更新已完成並通過檢查",
			}, nil
		}
		// A committed update may have been interrupted during a filesystem
		// flush. Try to reconstruct the target before falling back to the old
		// revision.
		if err := restoreInPlaceCommit(m.paths.Source, plumbing.NewHash(journal.TargetCommit)); err == nil {
			if commitTime, validateErr := validateInstalledCommit(m.paths.Source, plumbing.NewHash(journal.TargetCommit)); validateErr == nil {
				return updateRecoveryResult{commit: journal.TargetCommit, commitTime: commitTime, message: "上次更新已完成並通過檢查"}, nil
			}
		}
	}

	if err := restoreInPlaceCommit(m.paths.Source, plumbing.NewHash(journal.FromCommit)); err != nil {
		return updateRecoveryResult{}, fmt.Errorf("restore previous in-place revision: %w", err)
	}
	message := "上次更新中斷，已復原原版本"
	if journal.Phase == updateJournalPhaseCommitted {
		message = "上次更新未完整保留，已復原原版本"
	}
	return updateRecoveryResult{commit: journal.FromCommit, message: message}, nil
}

func (m *gameManager) recoverReplacementUpdate(journal updateJournal) (updateRecoveryResult, error) {
	if journal.Phase == updateJournalPhaseCommitted {
		if commitTime, err := validateInstalledCommit(m.paths.Source, plumbing.NewHash(journal.TargetCommit)); err == nil {
			return updateRecoveryResult{commit: journal.TargetCommit, commitTime: commitTime, message: "上次更新已完成並通過檢查"}, nil
		}
	}

	if err := m.restoreReplacementBackup(journal.FromCommit); err != nil {
		return updateRecoveryResult{}, fmt.Errorf("restore previous replacement revision: %w", err)
	}
	message := "上次更新中斷，已復原原版本"
	if journal.Phase == updateJournalPhaseCommitted {
		message = "上次更新未完整保留，已復原原版本"
	}
	return updateRecoveryResult{commit: journal.FromCommit, message: message}, nil
}

func (m *gameManager) restoreReplacementBackup(fromCommit string) error {
	backup := m.updateBackupPath()
	backupExists, err := pathExists(backup)
	if err != nil {
		return fmt.Errorf("inspect update backup: %w", err)
	}
	if !backupExists {
		if _, err := validateInstalledCommit(m.paths.Source, plumbing.NewHash(fromCommit)); err == nil {
			return nil
		}
		if err := restoreInPlaceCommit(m.paths.Source, plumbing.NewHash(fromCommit)); err != nil {
			return fmt.Errorf("update backup is missing and source cannot restore the previous commit: %w", err)
		}
		return nil
	}
	if _, err := validateInstalledCommit(backup, plumbing.NewHash(fromCommit)); err != nil {
		if restoreErr := restoreInPlaceCommit(backup, plumbing.NewHash(fromCommit)); restoreErr != nil {
			return fmt.Errorf("repair update backup after validation failed (%v): %w", err, restoreErr)
		}
		if _, validateErr := validateInstalledCommit(backup, plumbing.NewHash(fromCommit)); validateErr != nil {
			return fmt.Errorf("validate repaired update backup: %w", validateErr)
		}
	}
	if err := os.RemoveAll(m.paths.Source); err != nil {
		return fmt.Errorf("remove interrupted replacement: %w", err)
	}
	if err := os.Rename(backup, m.paths.Source); err != nil {
		return fmt.Errorf("restore update backup: %w", err)
	}
	return nil
}

func restoreInPlaceCommit(root string, target plumbing.Hash) error {
	repository, err := git.PlainOpen(root)
	if err != nil {
		return fmt.Errorf("open game repository: %w", err)
	}
	commit, err := repository.CommitObject(target)
	if err != nil {
		return fmt.Errorf("read recovery commit: %w", err)
	}
	manifest, err := gameCommitManifest(commit)
	if err != nil {
		return fmt.Errorf("read recovery manifest: %w", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return fmt.Errorf("open recovery worktree: %w", err)
	}
	configureManagedGameWorktree(worktree)
	if _, err := repository.Storer.Index(); err != nil {
		// A corrupt index is one of the reasons the launcher falls back to a
		// full replacement. During journaled recovery it is safe to discard the
		// derived index and rebuild it from the recorded commit.
		if removeErr := os.Remove(filepath.Join(root, ".git", "index")); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove corrupt recovery index: %w", removeErr)
		}
	}
	if err := forceSynchronizeGameTree(repository, worktree, root, target, manifest); err != nil {
		return fmt.Errorf("synchronize recovery revision: %w", err)
	}
	return nil
}

func validateInstalledCommit(root string, expected plumbing.Hash) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect game source: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("game source is not a real directory")
	}
	repository, err := git.PlainOpen(root)
	if err != nil {
		return "", fmt.Errorf("open game repository: %w", err)
	}
	head, err := repository.Head()
	if err != nil {
		return "", fmt.Errorf("read game revision: %w", err)
	}
	if head.Hash() != expected {
		return "", fmt.Errorf("game revision %s does not match expected revision %s", head.Hash(), expected)
	}
	commit, err := repository.CommitObject(expected)
	if err != nil {
		return "", fmt.Errorf("read expected game commit: %w", err)
	}
	manifest, err := gameCommitManifest(commit)
	if err != nil {
		return "", fmt.Errorf("read expected game manifest: %w", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return "", fmt.Errorf("open game worktree: %w", err)
	}
	configureManagedGameWorktree(worktree)
	if err := validateSynchronizedGameTree(repository, worktree, root, manifest); err != nil {
		return "", err
	}
	if err := validateGameRoot(root); err != nil {
		return "", err
	}
	return commit.Committer.When.Format(time.RFC3339), nil
}

func (m *gameManager) finishUpdateRecovery(result updateRecoveryResult, err error) {
	m.mu.Lock()
	m.stopOperationProgressLocked()
	m.running = false
	m.cancel = nil
	if err != nil {
		m.activeRoot = ""
		m.state = GameState{Status: StatusRecoveryFailed, Message: "無法自動復原上次更新", Error: err.Error()}
	} else {
		m.pendingJournal = nil
		m.journalLoadError = nil
		m.activeRoot = m.paths.Source
		m.state = GameState{
			Status:           StatusReady,
			Commit:           result.commit,
			CommitTime:       result.commitTime,
			RemoteCommit:     result.commit,
			RemoteCommitTime: result.commitTime,
			Message:          result.message,
		}
	}
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	if err != nil {
		m.logger.Error("update recovery failed", "error", err)
	} else {
		m.logger.Info("update recovery completed", "commit", result.commit)
	}
	m.publish(state)
}

func (m *gameManager) setUpdateRecoveryFailure(journal *updateJournal, err error) {
	m.mu.Lock()
	m.stopOperationProgressLocked()
	m.running = false
	m.cancel = nil
	m.activeRoot = ""
	if journal != nil {
		copy := *journal
		m.pendingJournal = &copy
	}
	m.journalLoadError = err
	m.state = GameState{Status: StatusRecoveryFailed, Message: "無法自動復原上次更新", Error: err.Error()}
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) prepareUpdateJournal(from, target string, strategy updateJournalStrategy) (updateJournal, error) {
	journal := updateJournal{
		SchemaVersion: updateJournalSchemaVersion,
		GameRoot:      m.paths.GameRoot,
		FromCommit:    from,
		TargetCommit:  target,
		Strategy:      strategy,
		Phase:         updateJournalPhasePrepared,
	}
	if m.journalStore == nil {
		m.journalStore = newUpdateJournalStore(m.paths)
	}
	if err := m.journalStore.Save(journal); err != nil {
		if _, statErr := os.Lstat(m.journalStore.path); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return journal, m.markUpdateRecoveryRequired(journal, fmt.Errorf("update journal may exist after save failure: %w", err))
		}
		return updateJournal{}, err
	}
	m.mu.Lock()
	copy := journal
	m.pendingJournal = &copy
	m.journalLoadError = nil
	m.mu.Unlock()
	return journal, nil
}

func (m *gameManager) commitUpdateJournal(journal updateJournal) (updateJournal, error) {
	committed := journal
	committed.Phase = updateJournalPhaseCommitted
	if err := m.journalStore.Save(committed); err != nil {
		if isUpdateJournalRenameSyncError(err) {
			m.mu.Lock()
			copy := committed
			m.pendingJournal = &copy
			m.mu.Unlock()
			return committed, err
		}
		return journal, err
	}
	m.mu.Lock()
	copy := committed
	m.pendingJournal = &copy
	m.mu.Unlock()
	return committed, nil
}

func (m *gameManager) clearUpdateJournal() error {
	if err := m.journalStore.Clear(); err != nil {
		return err
	}
	m.mu.Lock()
	m.pendingJournal = nil
	m.journalLoadError = nil
	m.mu.Unlock()
	return nil
}

func (m *gameManager) cleanupUpdateStaging() error {
	if err := clearGameStaging(m.paths.Staging); err != nil {
		return fmt.Errorf("clean update staging directory: %w", err)
	}
	return nil
}

func (m *gameManager) updateBackupPath() string {
	return filepath.Join(m.paths.Staging, ".previous-game")
}

func (m *gameManager) markUpdateRecoveryRequired(journal updateJournal, cause error) error {
	m.mu.Lock()
	copy := journal
	m.pendingJournal = &copy
	m.journalLoadError = cause
	m.activeRoot = ""
	m.state = GameState{Status: StatusRecoveryFailed, Message: "無法自動復原上次更新", Error: cause.Error()}
	m.advanceRevisionLocked()
	m.mu.Unlock()
	return fmt.Errorf("%w: %v", errUpdateRecoveryRequired, cause)
}

func (m *gameManager) rollbackPreparedUpdate(journal updateJournal) error {
	var err error
	switch journal.Strategy {
	case updateJournalStrategyInPlace:
		err = restoreInPlaceCommit(m.paths.Source, plumbing.NewHash(journal.FromCommit))
	case updateJournalStrategyReplace:
		err = m.restoreReplacementBackup(journal.FromCommit)
	default:
		err = fmt.Errorf("unsupported rollback strategy %q", journal.Strategy)
	}
	if err != nil {
		return err
	}
	if _, err := validateInstalledCommit(m.paths.Source, plumbing.NewHash(journal.FromCommit)); err != nil {
		return err
	}
	if err := m.cleanupUpdateStaging(); err != nil {
		return err
	}
	return m.clearUpdateJournal()
}

func (m *gameManager) enterCriticalUpdate(ctx context.Context, step int, phase, text string) error {
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return err
	}
	if m.state.Status != StatusUpdating || m.operationProgress == nil {
		m.mu.Unlock()
		return errors.New("update is no longer active")
	}
	m.state.ProgressStep = step
	m.state.ProgressStepTotal = updateProgressSteps
	m.state.ProgressPhase = phase
	m.state.ProgressText = text
	m.state.ProgressPercent = -1
	m.state.ProgressCancellable = false
	m.state.ProgressSeconds = elapsedSeconds(m.operationProgress.started, time.Now())
	m.operationProgress.network = false
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
	return nil
}
