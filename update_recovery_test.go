package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestStartupRecoversInPlaceUpdateJournal(t *testing.T) {
	tests := []struct {
		name          string
		phase         updateJournalPhase
		corruptTarget bool
		wantTarget    bool
		wantMessage   string
	}{
		{
			name:        "prepared rolls back to previous commit",
			phase:       updateJournalPhasePrepared,
			wantMessage: "上次更新中斷，已復原原版本",
		},
		{
			name:          "prepared reconstructs previous commit from partial tree",
			phase:         updateJournalPhasePrepared,
			corruptTarget: true,
			wantMessage:   "上次更新中斷，已復原原版本",
		},
		{
			name:        "committed keeps validated target commit",
			phase:       updateJournalPhaseCommitted,
			wantTarget:  true,
			wantMessage: "上次更新已完成並通過檢查",
		},
		{
			name:          "committed reconstructs damaged target worktree",
			phase:         updateJournalPhaseCommitted,
			corruptTarget: true,
			wantTarget:    true,
			wantMessage:   "上次更新已完成並通過檢查",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := makeDataPaths(t.TempDir())
			repository := newLocalGameRepository(t, paths.Source)
			fromCommit := repository.head(t)
			targetCommit := repository.commitFile(t, "js/new-only.js", "new version", "target version")
			if test.corruptTarget {
				writeTestFile(t, paths.Source, "index.html", "interrupted write")
			}
			saveRecoveryTestJournal(t, paths, updateJournal{
				GameRoot:     paths.GameRoot,
				FromCommit:   fromCommit,
				TargetCommit: targetCommit,
				Strategy:     updateJournalStrategyInPlace,
				Phase:        test.phase,
			})

			recorder := &recoveryStateRecorder{}
			manager, err := newGameManager(paths, recorder.record)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Shutdown)
			assertPendingRecoveryState(t, manager)
			assertLaunchBlockedDuringRecovery(t, manager)

			if !manager.StartPendingUpdateRecovery() {
				t.Fatal("startup did not detect pending in-place recovery")
			}
			waitForStatus(t, manager, StatusReady)

			wantCommit := fromCommit
			if test.wantTarget {
				wantCommit = targetCommit
			}
			assertRecoveryReady(t, manager, paths, wantCommit, test.wantMessage)
			assertRecoveryProgressPublished(t, recorder.snapshot())
			assertRecoveryJournalCleared(t, paths)
			if test.wantTarget {
				assertRecoveryFile(t, paths.Source, "js/new-only.js", "new version")
				assertRecoveryFile(t, paths.Source, "index.html", "game")
			} else if _, err := os.Lstat(filepath.Join(paths.Source, "js", "new-only.js")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target-only file survived prepared rollback: %v", err)
			}
		})
	}
}

func TestStartupRecoversReplacementUpdateJournal(t *testing.T) {
	tests := []struct {
		name               string
		phase              updateJournalPhase
		corruptTarget      bool
		corruptBackupIndex bool
		wantTarget         bool
		wantMessage        string
	}{
		{
			name:        "prepared restores previous backup",
			phase:       updateJournalPhasePrepared,
			wantMessage: "上次更新中斷，已復原原版本",
		},
		{
			name:        "committed keeps validated replacement",
			phase:       updateJournalPhaseCommitted,
			wantTarget:  true,
			wantMessage: "上次更新已完成並通過檢查",
		},
		{
			name:               "prepared repairs corrupt backup index before rollback",
			phase:              updateJournalPhasePrepared,
			corruptBackupIndex: true,
			wantMessage:        "上次更新中斷，已復原原版本",
		},
		{
			name:          "committed restores backup when replacement is damaged",
			phase:         updateJournalPhaseCommitted,
			corruptTarget: true,
			wantMessage:   "上次更新未完整保留，已復原原版本",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := makeDataPaths(t.TempDir())
			backup := filepath.Join(paths.Staging, ".previous-game")
			previous := newLocalGameRepository(t, backup)
			fromCommit := previous.commitFile(t, "js/old-only.js", "old version", "previous version")
			if test.corruptBackupIndex {
				if err := os.WriteFile(filepath.Join(backup, ".git", "index"), []byte("corrupt index"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			candidate := newLocalGameRepository(t, paths.Source)
			targetCommit := candidate.commitFile(t, "js/new-only.js", "new version", "replacement version")
			if test.corruptTarget {
				if err := os.Remove(filepath.Join(paths.Source, "assets", "image.png")); err != nil {
					t.Fatal(err)
				}
			}
			saveRecoveryTestJournal(t, paths, updateJournal{
				GameRoot:     paths.GameRoot,
				FromCommit:   fromCommit,
				TargetCommit: targetCommit,
				Strategy:     updateJournalStrategyReplace,
				Phase:        test.phase,
			})

			manager, err := newGameManager(paths, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Shutdown)
			assertPendingRecoveryState(t, manager)
			if !manager.StartPendingUpdateRecovery() {
				t.Fatal("startup did not detect pending replacement recovery")
			}
			waitForStatus(t, manager, StatusReady)

			wantCommit := fromCommit
			if test.wantTarget {
				wantCommit = targetCommit
			}
			assertRecoveryReady(t, manager, paths, wantCommit, test.wantMessage)
			assertRecoveryJournalCleared(t, paths)
			if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("replacement backup survived successful recovery: %v", err)
			}
			entries, err := os.ReadDir(paths.Staging)
			if err != nil {
				t.Fatalf("read cleaned recovery staging: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("recovery staging was not cleaned: %v", entries)
			}
			if test.wantTarget {
				assertRecoveryFile(t, paths.Source, "js/new-only.js", "new version")
				if _, err := os.Lstat(filepath.Join(paths.Source, "js", "old-only.js")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("backup-only file appeared in committed replacement: %v", err)
				}
			} else {
				assertRecoveryFile(t, paths.Source, "js/old-only.js", "old version")
				if _, err := os.Lstat(filepath.Join(paths.Source, "js", "new-only.js")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("replacement-only file survived backup recovery: %v", err)
				}
			}
		})
	}
}

func TestReplacementRecoveryHandlesRenameCrashBoundaries(t *testing.T) {
	t.Run("prepared before first rename keeps original source", func(t *testing.T) {
		paths := makeDataPaths(t.TempDir())
		original := newLocalGameRepository(t, paths.Source)
		fromCommit := original.commitFile(t, "js/old-only.js", "old version", "previous version")
		target := newLocalGameRepository(t, filepath.Join(t.TempDir(), "target-template"))
		targetCommit := target.commitFile(t, "js/new-only.js", "new version", "replacement version")
		saveRecoveryTestJournal(t, paths, updateJournal{GameRoot: paths.GameRoot, FromCommit: fromCommit, TargetCommit: targetCommit, Strategy: updateJournalStrategyReplace, Phase: updateJournalPhasePrepared})

		manager, err := newGameManager(paths, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !manager.StartPendingUpdateRecovery() {
			t.Fatal("prepared replacement was not detected")
		}
		waitForStatus(t, manager, StatusReady)
		assertRecoveryReady(t, manager, paths, fromCommit, "上次更新中斷，已復原原版本")
		assertRecoveryJournalCleared(t, paths)
	})

	t.Run("prepared between renames restores backup when source is absent", func(t *testing.T) {
		paths := makeDataPaths(t.TempDir())
		backup := newLocalGameRepository(t, filepath.Join(paths.Staging, ".previous-game"))
		fromCommit := backup.commitFile(t, "js/old-only.js", "old version", "previous version")
		target := newLocalGameRepository(t, filepath.Join(t.TempDir(), "target-template"))
		targetCommit := target.commitFile(t, "js/new-only.js", "new version", "replacement version")
		saveRecoveryTestJournal(t, paths, updateJournal{GameRoot: paths.GameRoot, FromCommit: fromCommit, TargetCommit: targetCommit, Strategy: updateJournalStrategyReplace, Phase: updateJournalPhasePrepared})

		manager, err := newGameManager(paths, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !manager.StartPendingUpdateRecovery() {
			t.Fatal("prepared replacement was not detected")
		}
		waitForStatus(t, manager, StatusReady)
		assertRecoveryReady(t, manager, paths, fromCommit, "上次更新中斷，已復原原版本")
		assertRecoveryJournalCleared(t, paths)
	})

	t.Run("committed after backup cleanup keeps target", func(t *testing.T) {
		paths := makeDataPaths(t.TempDir())
		previous := newLocalGameRepository(t, filepath.Join(t.TempDir(), "previous-template"))
		fromCommit := previous.commitFile(t, "js/old-only.js", "old version", "previous version")
		target := newLocalGameRepository(t, paths.Source)
		targetCommit := target.commitFile(t, "js/new-only.js", "new version", "replacement version")
		saveRecoveryTestJournal(t, paths, updateJournal{GameRoot: paths.GameRoot, FromCommit: fromCommit, TargetCommit: targetCommit, Strategy: updateJournalStrategyReplace, Phase: updateJournalPhaseCommitted})

		manager, err := newGameManager(paths, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !manager.StartPendingUpdateRecovery() {
			t.Fatal("committed replacement was not detected")
		}
		waitForStatus(t, manager, StatusReady)
		assertRecoveryReady(t, manager, paths, targetCommit, "上次更新已完成並通過檢查")
		assertRecoveryJournalCleared(t, paths)
	})
}

func TestRecoveryRequiredWaitsForOriginalJobToFinishBeforeRetry(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	journal := updateJournal{
		SchemaVersion: updateJournalSchemaVersion,
		GameRoot:      paths.GameRoot,
		FromCommit:    "1111111111111111111111111111111111111111",
		TargetCommit:  "2222222222222222222222222222222222222222",
		Strategy:      updateJournalStrategyInPlace,
		Phase:         updateJournalPhasePrepared,
	}
	manager.mu.Lock()
	manager.running = true
	manager.state = GameState{Status: StatusUpdating, ProgressCancellable: false}
	manager.startUpdateProgressLocked(time.Now())
	manager.state.ProgressCancellable = false
	manager.mu.Unlock()

	recoveryErr := manager.markUpdateRecoveryRequired(journal, errors.New("rollback failed"))
	manager.mu.RLock()
	stillRunning := manager.running
	progress := manager.operationProgress
	manager.mu.RUnlock()
	if !stillRunning || progress == nil {
		t.Fatal("recovery failure exposed an idle manager before the original update finished")
	}
	if err := manager.RetryUpdateRecovery(); err != nil {
		t.Fatalf("retry while original update is finishing: %v", err)
	}
	manager.mu.RLock()
	if !manager.running || manager.operationProgress != progress {
		manager.mu.RUnlock()
		t.Fatal("retry started a concurrent recovery before finishJob")
	}
	manager.mu.RUnlock()

	manager.finishJob("sync", recoveryErr, GameState{Status: StatusReady}, "failed", "cancelled")
	state := manager.State()
	manager.mu.RLock()
	running := manager.running
	operationProgress := manager.operationProgress
	manager.mu.RUnlock()
	if running || operationProgress != nil || state.Status != StatusRecoveryFailed {
		t.Fatalf("finishJob did not publish one stable recovery failure: running=%v progress=%v state=%+v", running, operationProgress, state)
	}
}

func TestRecoveryHeartbeatReportsElapsedTimeWithoutChangingItsStep(t *testing.T) {
	manager, err := newGameManager(makeDataPaths(t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	manager.mu.Lock()
	manager.state = GameState{
		Status:              StatusRecovering,
		ProgressPhase:       "復原安全版本",
		ProgressText:        "正在還原可安全啟動的遊戲版本…",
		ProgressPercent:     -1,
		ProgressStep:        1,
		ProgressStepTotal:   updateRecoverySteps,
		ProgressCancellable: false,
	}
	manager.operationProgress = &gameOperationProgress{sequence: 41, started: started, lastActivity: started, stop: make(chan struct{})}
	manager.mu.Unlock()

	manager.publishOperationHeartbeat(41, started.Add(75*time.Second))
	state := manager.State()
	if state.ProgressSeconds != 75 || state.ProgressStep != 1 || state.ProgressText != "正在還原可安全啟動的遊戲版本…" {
		t.Fatalf("recovery heartbeat changed phase state or lost elapsed time: %+v", state)
	}
}

func TestPreparedJournalDirectorySyncFailureRemainsOwnedAndRecoverable(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	repository := newLocalGameRepository(t, paths.Source)
	fromCommit := repository.head(t)
	targetCommit := repository.commitFile(t, "js/new-only.js", "new version", "target version")
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.journalStore.syncDirectory = func(string) error { return errors.New("injected directory sync failure") }
	manager.mu.Lock()
	manager.running = true
	manager.state.Status = StatusUpdating
	manager.startUpdateProgressLocked(time.Now())
	manager.mu.Unlock()

	journal, prepareErr := manager.prepareUpdateJournal(fromCommit, targetCommit, updateJournalStrategyInPlace)
	if !errors.Is(prepareErr, errUpdateRecoveryRequired) {
		t.Fatalf("directory sync failure = %v, want recovery-required error", prepareErr)
	}
	if journal.FromCommit != fromCommit || journal.TargetCommit != targetCommit {
		t.Fatalf("prepared journal ownership was lost: %+v", journal)
	}
	if _, err := os.Stat(paths.UpdateJournal); err != nil {
		t.Fatalf("renamed journal was not retained after sync failure: %v", err)
	}
	manager.mu.RLock()
	pending := manager.pendingJournal
	running := manager.running
	manager.mu.RUnlock()
	if pending == nil || !running || manager.State().Status != StatusRecoveryFailed {
		t.Fatalf("sync failure was not owned by the active job: pending=%+v running=%v state=%+v", pending, running, manager.State())
	}

	manager.finishJob("sync", prepareErr, GameState{Status: StatusReady}, "failed", "cancelled")
	manager.journalStore.syncDirectory = syncUpdateJournalDirectory
	if err := manager.RetryUpdateRecovery(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	assertRecoveryReady(t, manager, paths, fromCommit, "上次更新中斷，已復原原版本")
	assertRecoveryJournalCleared(t, paths)
}

func TestCommittedJournalDirectorySyncFailurePreservesActualPhase(t *testing.T) {
	for _, strategy := range []updateJournalStrategy{updateJournalStrategyInPlace, updateJournalStrategyReplace} {
		t.Run(string(strategy), func(t *testing.T) {
			paths := makeDataPaths(t.TempDir())
			store := newUpdateJournalStore(paths)
			prepared := updateJournal{
				SchemaVersion: updateJournalSchemaVersion,
				GameRoot:      paths.GameRoot,
				FromCommit:    "1111111111111111111111111111111111111111",
				TargetCommit:  "2222222222222222222222222222222222222222",
				Strategy:      strategy,
				Phase:         updateJournalPhasePrepared,
			}
			if err := store.Save(prepared); err != nil {
				t.Fatal(err)
			}
			store.syncDirectory = func(string) error { return errors.New("injected committed directory sync failure") }
			manager := &gameManager{paths: paths, journalStore: store, pendingJournal: &prepared}

			actual, err := manager.commitUpdateJournal(prepared)
			if !isUpdateJournalRenameSyncError(err) {
				t.Fatalf("commit error = %v, want rename-sync error", err)
			}
			if actual.Phase != updateJournalPhaseCommitted {
				t.Fatalf("returned phase = %q, want committed", actual.Phase)
			}
			manager.mu.RLock()
			pending := manager.pendingJournal
			manager.mu.RUnlock()
			if pending == nil || pending.Phase != updateJournalPhaseCommitted {
				t.Fatalf("in-memory phase = %+v, want committed", pending)
			}
			onDisk, loadErr := store.Load()
			if loadErr != nil || onDisk == nil || onDisk.Phase != updateJournalPhaseCommitted {
				t.Fatalf("on-disk phase = %+v (%v), want committed", onDisk, loadErr)
			}
		})
	}
}

func TestRecoveryRetriesDirectorySyncAfterJournalWasRemoved(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	repository := newLocalGameRepository(t, paths.Source)
	fromCommit := repository.head(t)
	targetCommit := repository.commitFile(t, "js/new-only.js", "new version", "target version")
	saveRecoveryTestJournal(t, paths, updateJournal{GameRoot: paths.GameRoot, FromCommit: fromCommit, TargetCommit: targetCommit, Strategy: updateJournalStrategyInPlace, Phase: updateJournalPhasePrepared})
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	syncCalls := 0
	manager.journalStore.syncDirectory = func(string) error {
		syncCalls++
		if syncCalls == 1 {
			return errors.New("injected recovery clear sync failure")
		}
		return nil
	}

	if !manager.StartPendingUpdateRecovery() {
		t.Fatal("pending recovery was not detected")
	}
	waitForStatus(t, manager, StatusRecoveryFailed)
	manager.WaitForIdle()
	if _, err := os.Stat(paths.UpdateJournal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed clear did not remove the live journal: %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("directory sync calls after first recovery = %d, want 1", syncCalls)
	}
	if err := manager.RetryUpdateRecovery(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	assertRecoveryReady(t, manager, paths, fromCommit, "上次更新中斷，已復原原版本")
	if syncCalls != 2 {
		t.Fatalf("directory sync calls after retry = %d, want 2", syncCalls)
	}
}

func TestMalformedUpdateJournalIsRetainedAndBlocksLaunch(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	newLocalGameRepository(t, paths.Source)
	malformed := []byte(`{"schemaVersion":`)
	if err := os.WriteFile(paths.UpdateJournal, malformed, 0o600); err != nil {
		t.Fatal(err)
	}

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Shutdown)
	state := manager.State()
	if state.Status != StatusRecoveryFailed || state.Error == "" {
		t.Fatalf("malformed journal state = %+v, want recovery_failed with error", state)
	}
	if _, _, installed := manager.ActiveVersion(); installed {
		t.Fatal("malformed recovery journal left the game launchable")
	}
	assertLaunchBlockedDuringRecovery(t, manager)
	if err := manager.RetryUpdateRecovery(); err == nil {
		t.Fatal("retry unexpectedly accepted malformed recovery journal")
	}
	if state := manager.State(); state.Status != StatusRecoveryFailed || state.Error == "" {
		t.Fatalf("retry state = %+v, want recovery_failed with error", state)
	}
	contents, err := os.ReadFile(paths.UpdateJournal)
	if err != nil {
		t.Fatalf("malformed recovery journal was removed: %v", err)
	}
	if string(contents) != string(malformed) {
		t.Fatalf("malformed recovery journal changed: got %q, want %q", contents, malformed)
	}
}

func TestFolderRecheckCannotHideRecoveryOrDeleteItsBackup(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	repository := newLocalGameRepository(t, paths.Source)
	fromCommit := repository.head(t)
	targetCommit := repository.commitFile(t, "js/new-only.js", "new version", "target version")
	backupSentinel := filepath.Join(paths.Staging, ".previous-game", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(backupSentinel), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupSentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	saveRecoveryTestJournal(t, paths, updateJournal{
		GameRoot:     paths.GameRoot,
		FromCommit:   fromCommit,
		TargetCommit: targetCommit,
		Strategy:     updateJournalStrategyInPlace,
		Phase:        updateJournalPhasePrepared,
	})
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := newLauncherSettingsStore(paths)
	settings := launcherSettings{Version: launcherSettingsVersion, GameRoot: paths.GameRoot, LastKnownInstalled: true}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	coordinator := newGameFolderCoordinator(manager, store, settings, paths.Root)

	if err := coordinator.Recheck(); err == nil {
		t.Fatal("folder recheck unexpectedly ran while update recovery was pending")
	}
	if state := manager.State(); state.Status != StatusRecovering {
		t.Fatalf("folder recheck hid recovery state: %+v", state)
	}
	contents, err := os.ReadFile(backupSentinel)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("folder recheck deleted recovery backup: %q (%v)", contents, err)
	}
}

func TestFailedReplacementRecoverySucceedsAfterBackupIsRestoredAndRetried(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	previousTemplate := newLocalGameRepository(t, filepath.Join(t.TempDir(), "previous-template"))
	fromCommit := previousTemplate.commitFile(t, "js/old-only.js", "old version", "previous version")
	candidate := newLocalGameRepository(t, paths.Source)
	targetCommit := candidate.commitFile(t, "js/new-only.js", "new version", "replacement version")
	saveRecoveryTestJournal(t, paths, updateJournal{
		GameRoot:     paths.GameRoot,
		FromCommit:   fromCommit,
		TargetCommit: targetCommit,
		Strategy:     updateJournalStrategyReplace,
		Phase:        updateJournalPhasePrepared,
	})

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Shutdown)
	if !manager.StartPendingUpdateRecovery() {
		t.Fatal("startup did not detect pending replacement recovery")
	}
	waitForStatus(t, manager, StatusRecoveryFailed)
	if _, err := os.Stat(paths.UpdateJournal); err != nil {
		t.Fatalf("failed recovery did not retain its journal: %v", err)
	}
	assertLaunchBlockedDuringRecovery(t, manager)

	backup := filepath.Join(paths.Staging, ".previous-game")
	restoredBackup := newLocalGameRepository(t, backup)
	if got := restoredBackup.commitFile(t, "js/old-only.js", "old version", "previous version"); got != fromCommit {
		t.Fatalf("restored backup commit = %s, want journal source %s", got, fromCommit)
	}
	if err := manager.RetryUpdateRecovery(); err != nil {
		t.Fatalf("retry recovered replacement: %v", err)
	}
	waitForStatus(t, manager, StatusReady)
	assertRecoveryReady(t, manager, paths, fromCommit, "上次更新中斷，已復原原版本")
	assertRecoveryJournalCleared(t, paths)
	assertRecoveryFile(t, paths.Source, "js/old-only.js", "old version")

	opened := ""
	if err := manager.withLaunchableRoot(func(root string) error {
		opened = root
		return nil
	}); err != nil {
		t.Fatalf("launch remained blocked after successful retry: %v", err)
	}
	if opened != paths.Source {
		t.Fatalf("launch root after recovery = %q, want %q", opened, paths.Source)
	}
}

type recoveryStateRecorder struct {
	mu     sync.Mutex
	states []GameState
}

func (recorder *recoveryStateRecorder) record(state GameState) {
	recorder.mu.Lock()
	recorder.states = append(recorder.states, state)
	recorder.mu.Unlock()
}

func (recorder *recoveryStateRecorder) snapshot() []GameState {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]GameState(nil), recorder.states...)
}

func saveRecoveryTestJournal(t *testing.T, paths dataPaths, journal updateJournal) {
	t.Helper()
	if err := newUpdateJournalStore(paths).Save(journal); err != nil {
		t.Fatalf("save recovery journal: %v", err)
	}
}

func assertPendingRecoveryState(t *testing.T, manager *gameManager) {
	t.Helper()
	state := manager.State()
	if state.Status != StatusRecovering || state.ProgressStep != 1 || state.ProgressStepTotal != updateRecoverySteps || state.ProgressPercent != -1 || state.ProgressCancellable {
		t.Fatalf("pending recovery state = %+v", state)
	}
	if _, _, installed := manager.ActiveVersion(); installed {
		t.Fatal("pending recovery exposed an active game version")
	}
}

func assertLaunchBlockedDuringRecovery(t *testing.T, manager *gameManager) {
	t.Helper()
	called := false
	err := manager.withLaunchableRoot(func(string) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("launch was allowed during update recovery")
	}
	if called {
		t.Fatal("game opener was called during update recovery")
	}
}

func assertRecoveryReady(t *testing.T, manager *gameManager, paths dataPaths, commit, message string) {
	t.Helper()
	manager.WaitForIdle()
	state := manager.State()
	if state.Status != StatusReady || state.Commit != commit || state.RemoteCommit != commit || state.Message != message || state.Error != "" {
		t.Fatalf("recovered state = %+v, want ready commit %s and message %q", state, commit, message)
	}
	if _, err := validateInstalledCommit(paths.Source, plumbing.NewHash(commit)); err != nil {
		t.Fatalf("recovered installation is invalid: %v", err)
	}
	root, activeCommit, installed := manager.ActiveVersion()
	if !installed || root != paths.Source || activeCommit != commit {
		t.Fatalf("active recovery result = (%q, %q, %v), want (%q, %q, true)", root, activeCommit, installed, paths.Source, commit)
	}
	manager.mu.RLock()
	running := manager.running
	progress := manager.operationProgress
	manager.mu.RUnlock()
	if running || progress != nil || state.ProgressPhase != "" || state.ProgressText != "" || state.ProgressSeconds != 0 || state.ProgressStep != 0 || state.ProgressStepTotal != 0 || state.ProgressCancellable {
		t.Fatalf("recovery terminal state retained lifecycle or progress: running=%v progress=%v state=%+v", running, progress, state)
	}
}

func assertRecoveryProgressPublished(t *testing.T, states []GameState) {
	t.Helper()
	want := map[int]bool{1: false, 2: false}
	for _, state := range states {
		if state.Status != StatusRecovering {
			continue
		}
		if _, exists := want[state.ProgressStep]; exists && state.ProgressStepTotal == updateRecoverySteps && state.ProgressPercent == -1 && !state.ProgressCancellable {
			want[state.ProgressStep] = true
		}
	}
	for step, seen := range want {
		if !seen {
			t.Fatalf("recovery progress did not publish step %d/2: %+v", step, states)
		}
	}
}

func assertRecoveryJournalCleared(t *testing.T, paths dataPaths) {
	t.Helper()
	if _, err := os.Lstat(paths.UpdateJournal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery journal was not cleared: %v", err)
	}
}

func assertRecoveryFile(t *testing.T, root, relative, want string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read recovered file %s: %v", relative, err)
	}
	if string(contents) != want {
		t.Fatalf("recovered file %s = %q, want %q", relative, contents, want)
	}
}
