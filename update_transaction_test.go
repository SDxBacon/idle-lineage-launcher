package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestUpdateTransactionCancelBeforeApplyLeavesExactVersion(t *testing.T) {
	manager, remote, oldCommit := newUpdateTransactionFixture(t, nil)
	remote.commitFile(t, "js/app.js", "console.log('cancelled update')", "cancelled update")

	gate := newUpdateTransactionGate(t)
	manager.beforeUpdateApply = gate.block

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	gate.waitUntilBlocked(t)

	state := manager.State()
	if state.Status != StatusUpdating || !state.ProgressCancellable || state.ProgressStep != 2 {
		t.Fatalf("pre-apply state = %+v, want cancellable download step", state)
	}
	if _, err := os.Stat(manager.paths.UpdateJournal); err != nil {
		t.Fatalf("pre-apply update journal was not prepared: %v", err)
	}
	if err := manager.CancelUpdate(); err != nil {
		t.Fatalf("cancel pre-apply update: %v", err)
	}
	gate.release()

	waitForStatus(t, manager, StatusReady)
	manager.WaitForIdle()
	assertExactUpdateTransactionVersion(t, manager, oldCommit, "console.log('ready')")
	assertUpdateTransactionJournalCleared(t, manager)
	assertUpdateTransactionProgressCleared(t, manager.State())
	if state := manager.State(); state.Error != "" || state.Message != "已取消更新；目前版本仍可使用" {
		t.Fatalf("cancelled terminal state = %+v", state)
	}
}

func TestUpdateTransactionRejectsCancelAfterCriticalAndCompletes(t *testing.T) {
	manager, remote, _ := newUpdateTransactionFixture(t, nil)
	newCommit := remote.commitFile(t, "js/app.js", "console.log('critical update')", "critical update")

	gate := newUpdateTransactionGate(t)
	manager.afterUpdateCritical = gate.block

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	gate.waitUntilBlocked(t)

	state := manager.State()
	if state.Status != StatusUpdating || state.ProgressCancellable || state.ProgressStep != 3 || state.ProgressPercent != -1 {
		t.Fatalf("critical update state = %+v", state)
	}
	if err := manager.CancelUpdate(); err == nil {
		t.Fatal("critical update unexpectedly accepted cancellation")
	}
	gate.release()

	waitForStatus(t, manager, StatusReady)
	manager.WaitForIdle()
	assertExactUpdateTransactionVersion(t, manager, newCommit, "console.log('critical update')")
	assertUpdateTransactionJournalCleared(t, manager)
	assertUpdateTransactionProgressCleared(t, manager.State())
}

func TestUpdateTransactionRollsBackAfterPostMutationFailureAndReplacementFailure(t *testing.T) {
	recorder := &updateTransactionRecorder{}
	manager, remote, oldCommit := newUpdateTransactionFixture(t, recorder.record)
	remote.commitFile(t, "js/app.js", "console.log('mutated update')", "mutated update")
	recorder.reset()

	missingReplacement := filepath.Join(t.TempDir(), "missing-replacement")
	manager.synchronizeUpdate = func(repository *git.Repository, worktree *git.Worktree, root string, target plumbing.Hash, manifest map[string]gameTreePathKind) error {
		if err := forceSynchronizeGameTree(repository, worktree, root, target, manifest); err != nil {
			return err
		}
		manager.repositoryURL = missingReplacement
		return errors.New("injected failure after synchronizing the new version")
	}

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	manager.WaitForIdle()

	state := manager.State()
	if state.Error == "" || state.Message != "更新失敗；目前版本仍可使用" {
		t.Fatalf("failed update terminal state = %+v", state)
	}
	if !recorder.containsProgressPhase("重新連線 GitHub") {
		t.Fatalf("replacement fallback was not attempted: %+v", recorder.snapshot())
	}
	assertExactUpdateTransactionVersion(t, manager, oldCommit, "console.log('ready')")
	assertUpdateTransactionJournalCleared(t, manager)
	assertUpdateTransactionProgressCleared(t, state)
	assertDirectoryEmpty(t, manager.paths.Staging)
}

func TestSuccessfulUpdateTransactionPublishesEveryStepAndClearsProgress(t *testing.T) {
	recorder := &updateTransactionRecorder{}
	manager, remote, _ := newUpdateTransactionFixture(t, recorder.record)
	newCommit := remote.commitFile(t, "js/app.js", "console.log('successful update')", "successful update")
	recorder.reset()

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	manager.WaitForIdle()

	states := recorder.snapshot()
	for step := 1; step <= updateProgressSteps; step++ {
		if !containsUpdateTransactionStep(states, step) {
			t.Fatalf("successful update did not publish step %d/%d: %+v", step, updateProgressSteps, states)
		}
	}
	for _, step := range []int{3, 4} {
		if !containsLocalUpdateTransactionStep(states, step) {
			t.Fatalf("local step %d was not indeterminate and noncancellable: %+v", step, states)
		}
	}
	assertExactUpdateTransactionVersion(t, manager, newCommit, "console.log('successful update')")
	assertUpdateTransactionJournalCleared(t, manager)
	assertUpdateTransactionProgressCleared(t, manager.State())
}

type updateTransactionGate struct {
	entered        chan struct{}
	releaseChannel chan struct{}
	enterOnce      sync.Once
	releaseOnce    sync.Once
}

func newUpdateTransactionGate(t *testing.T) *updateTransactionGate {
	t.Helper()
	gate := &updateTransactionGate{
		entered:        make(chan struct{}),
		releaseChannel: make(chan struct{}),
	}
	t.Cleanup(gate.release)
	return gate
}

func (gate *updateTransactionGate) block() {
	gate.enterOnce.Do(func() { close(gate.entered) })
	<-gate.releaseChannel
}

func (gate *updateTransactionGate) waitUntilBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for update transaction seam")
	}
}

func (gate *updateTransactionGate) release() {
	gate.releaseOnce.Do(func() { close(gate.releaseChannel) })
}

type updateTransactionRecorder struct {
	mu     sync.Mutex
	states []GameState
}

func (recorder *updateTransactionRecorder) record(state GameState) {
	recorder.mu.Lock()
	recorder.states = append(recorder.states, state)
	recorder.mu.Unlock()
}

func (recorder *updateTransactionRecorder) reset() {
	recorder.mu.Lock()
	recorder.states = nil
	recorder.mu.Unlock()
}

func (recorder *updateTransactionRecorder) snapshot() []GameState {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]GameState(nil), recorder.states...)
}

func (recorder *updateTransactionRecorder) containsProgressPhase(phase string) bool {
	for _, state := range recorder.snapshot() {
		if state.ProgressPhase == phase {
			return true
		}
	}
	return false
}

func newUpdateTransactionFixture(t *testing.T, emit stateEmitter) (*gameManager, *localGameRepository, string) {
	t.Helper()
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, emit)
	t.Cleanup(manager.Shutdown)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	manager.WaitForIdle()
	return manager, remote, manager.State().Commit
}

func assertExactUpdateTransactionVersion(t *testing.T, manager *gameManager, commit, wantJavaScript string) {
	t.Helper()
	state := manager.State()
	if state.Status != StatusReady || state.Commit != commit {
		t.Fatalf("active state = %+v, want ready commit %s", state, commit)
	}
	if _, err := validateInstalledCommit(manager.paths.Source, plumbing.NewHash(commit)); err != nil {
		t.Fatalf("installed version is not the exact clean commit %s: %v", commit, err)
	}
	contents, err := os.ReadFile(filepath.Join(manager.paths.Source, "js", "app.js"))
	if err != nil || string(contents) != wantJavaScript {
		t.Fatalf("installed JavaScript = %q, want %q (%v)", contents, wantJavaScript, err)
	}
}

func assertUpdateTransactionJournalCleared(t *testing.T, manager *gameManager) {
	t.Helper()
	if _, err := os.Lstat(manager.paths.UpdateJournal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("update journal was not cleared: %v", err)
	}
	manager.mu.RLock()
	pending := manager.pendingJournal
	manager.mu.RUnlock()
	if pending != nil {
		t.Fatalf("in-memory update journal was not cleared: %+v", pending)
	}
}

func assertUpdateTransactionProgressCleared(t *testing.T, state GameState) {
	t.Helper()
	if state.ProgressPhase != "" || state.ProgressText != "" || state.ProgressPercent != 0 || state.ProgressSeconds != 0 || state.ProgressStep != 0 || state.ProgressStepTotal != 0 || state.ProgressCancellable {
		t.Fatalf("terminal state retained update progress: %+v", state)
	}
}

func containsUpdateTransactionStep(states []GameState, step int) bool {
	for _, state := range states {
		if state.Status == StatusUpdating && state.ProgressStep == step && state.ProgressStepTotal == updateProgressSteps {
			return true
		}
	}
	return false
}

func containsLocalUpdateTransactionStep(states []GameState, step int) bool {
	for _, state := range states {
		if state.Status == StatusUpdating && state.ProgressStep == step && state.ProgressStepTotal == updateProgressSteps && state.ProgressPercent == -1 && !state.ProgressCancellable {
			return true
		}
	}
	return false
}
