package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type localGameRepository struct {
	path       string
	repository *git.Repository
	worktree   *git.Worktree
	clock      time.Time
}

func TestExistingGitVersionStartsReadyWithoutNetwork(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	repository := newLocalGameRepository(t, paths.Source)
	commit := repository.head(t)

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.Status != StatusReady || state.Commit != commit || state.CommitTime != repository.headTime(t) {
		t.Fatalf("unexpected state: %+v", state)
	}
	root, activeCommit, ready := manager.ActiveVersion()
	if !ready || root != paths.Source || activeCommit != commit {
		t.Fatalf("unexpected active version: %q %q %v", root, activeCommit, ready)
	}
	assertManagedGameExclude(t, paths.Source)
}

func TestEnsureGitInfoExcludePreservesExistingRulesAndIsIdempotent(t *testing.T) {
	tests := []struct {
		name     string
		existing *string
		want     string
	}{
		{name: "missing", want: ".DS_Store\n"},
		{name: "empty", existing: stringPointer(""), want: ".DS_Store\n"},
		{name: "existing LF", existing: stringPointer("ignored.cache\n"), want: "ignored.cache\n.DS_Store\n"},
		{name: "existing CRLF", existing: stringPointer("ignored.cache\r\n"), want: "ignored.cache\r\n.DS_Store\n"},
		{name: "missing final newline", existing: stringPointer("ignored.cache"), want: "ignored.cache\n.DS_Store\n"},
		{name: "exact LF", existing: stringPointer("ignored.cache\n.DS_Store\n"), want: "ignored.cache\n.DS_Store\n"},
		{name: "exact CRLF", existing: stringPointer("ignored.cache\r\n.DS_Store\r\n"), want: "ignored.cache\r\n.DS_Store\r\n"},
		{name: "exact at EOF", existing: stringPointer("ignored.cache\n.DS_Store"), want: "ignored.cache\n.DS_Store"},
		{
			name:     "similar patterns do not count",
			existing: stringPointer("# .DS_Store\n!.DS_Store\n/.DS_Store\n.DS_Store.backup\n"),
			want:     "# .DS_Store\n!.DS_Store\n/.DS_Store\n.DS_Store.backup\n.DS_Store\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			if test.existing != nil {
				writeTestFile(t, root, ".git/info/exclude", *test.existing)
			}
			if err := ensureGitInfoExclude(root); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
			if err != nil || string(contents) != test.want {
				t.Fatalf("exclude = %q, want %q (%v)", contents, test.want, err)
			}
			if err := ensureGitInfoExclude(root); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
			if err != nil || !bytes.Equal(after, contents) {
				t.Fatalf("idempotent ensure changed exclude: before=%q after=%q (%v)", contents, after, err)
			}
			if _, err := os.Lstat(filepath.Join(root, ".gitignore")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ensure created a working-tree .gitignore: %v", err)
			}
		})
	}
}

func TestEnsureGitInfoExcludeRejectsSymlinks(t *testing.T) {
	t.Run("info directory", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, ".git", "info")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := ensureGitInfoExclude(root); err == nil {
			t.Fatal("symlinked Git info directory was accepted")
		}
		if _, err := os.Lstat(filepath.Join(outside, "exclude")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("exclude was written outside the repository: %v", err)
		}
	})

	t.Run("exclude file", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside-exclude")
		writeTestFile(t, root, ".git/info/placeholder", "")
		if err := os.WriteFile(outside, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, ".git", "info", "exclude")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := ensureGitInfoExclude(root); err == nil {
			t.Fatal("symlinked Git info exclude was accepted")
		}
		contents, err := os.ReadFile(outside)
		if err != nil || string(contents) != "keep\n" {
			t.Fatalf("outside exclude was changed: %q (%v)", contents, err)
		}
	})
}

func TestManagedGitInfoExcludeIgnoresFinderMetadataAtAnyDepth(t *testing.T) {
	repository := newLocalGameRepository(t, filepath.Join(t.TempDir(), "game"))
	if err := ensureGitInfoExclude(repository.path); err != nil {
		t.Fatal(err)
	}
	configureManagedGameWorktree(repository.worktree)
	for _, relative := range []string{".DS_Store", "assets/.DS_Store"} {
		writeTestFile(t, repository.path, relative, "finder")
	}
	writeTestFile(t, repository.path, ".DS_Store.backup", "not Finder metadata")
	status, err := repository.worktree.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{".DS_Store", "assets/.DS_Store"} {
		if _, exists := status[relative]; exists {
			t.Fatalf("managed exclude did not ignore %q: %v", relative, status)
		}
	}
	if !status.IsUntracked(".DS_Store.backup") {
		t.Fatalf("managed exclude ignored a similar filename: %v", status)
	}
}

func TestExistingInstallIgnoresGitInfoExcludeWriteFailure(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	repository := newLocalGameRepository(t, paths.Source)
	infoPath := filepath.Join(paths.Source, ".git", "info")
	if err := os.RemoveAll(infoPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state := manager.State(); state.Status != StatusReady || state.Commit != repository.head(t) {
		t.Fatalf("exclude failure made existing install unavailable: %+v", state)
	}
}

func TestDataPathsUseShines871Source(t *testing.T) {
	root := t.TempDir()
	paths := makeDataPaths(root)
	want := filepath.Join(root, "game", "shines871")
	if paths.Source != want {
		t.Fatalf("unexpected source path: got %q, want %q", paths.Source, want)
	}
}

func TestLegacySrcIsIgnoredAndPreserved(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	legacySource := filepath.Join(paths.Game, "src")
	newLocalGameRepository(t, legacySource)
	marker := filepath.Join(legacySource, "legacy-marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.State().Status != StatusMissing {
		t.Fatalf("legacy src was treated as installed: %+v", manager.State())
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("legacy src was changed: %q (%v)", contents, err)
	}
}

func TestInvalidShines871SourceStartsInRecoverableError(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	writeValidGame(t, paths.Source)

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.State().Status != StatusError || manager.State().Error == "" {
		t.Fatalf("invalid source did not produce an error state: %+v", manager.State())
	}
	if err := validateGameRoot(paths.Source); err != nil {
		t.Fatalf("invalid source was unexpectedly deleted: %v", err)
	}
}

func TestRepositoryCheckFailureDoesNotReplaceInstalledFiles(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	writeTestFile(t, manager.paths.Source, "js/app.js", "offline copy")
	if err := os.Remove(filepath.Join(manager.paths.Source, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	events = nil
	mu.Unlock()

	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		state := manager.State()
		return state.Status == StatusReady && state.Error != ""
	}, "failed repository check")
	root, _, ready := manager.ActiveVersion()
	if !ready || root != manager.paths.Source {
		t.Fatalf("failed check deactivated the existing game: %q %v", root, ready)
	}
	contents, err := os.ReadFile(filepath.Join(manager.paths.Source, "js", "app.js"))
	if err != nil || string(contents) != "offline copy" {
		t.Fatalf("failed check changed the existing game: %q (%v)", contents, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if containsStatus(events, StatusUpdating) || containsStatus(events, StatusInstalling) {
		t.Fatalf("repository check failure triggered a mutating state: %+v", events)
	}
}

func TestDeletedGameDirectoryBecomesMissingAndCanBeReinstalled(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if err := os.RemoveAll(manager.paths.Game); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	events = nil
	mu.Unlock()

	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.Status != StatusMissing || state.Message != "尚未下載遊戲" || state.Error != "" {
		t.Fatalf("deleted installation was not reconciled as missing: %+v", state)
	}
	if state.Commit != "" || state.CommitTime != "" || state.RemoteCommit != "" || state.RemoteCommitTime != "" || state.UpdateAvailable {
		t.Fatalf("missing state retained stale version information: %+v", state)
	}
	if state.ProgressPhase != "" || state.ProgressText != "" || state.ProgressPercent != 0 || state.ProgressSeconds != 0 {
		t.Fatalf("missing state retained stale progress information: %+v", state)
	}
	if root, commit, installed := manager.ActiveVersion(); installed || root != "" || commit != "" {
		t.Fatalf("deleted installation remained active: root=%q commit=%q installed=%v", root, commit, installed)
	}
	mu.Lock()
	if containsStatus(events, StatusChecking) {
		mu.Unlock()
		t.Fatalf("check job started after the installation disappeared: %+v", events)
	}
	mu.Unlock()

	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if err := validateGameRoot(manager.paths.Source); err != nil {
		t.Fatalf("reinstall after deleting the game directory failed: %v", err)
	}
	assertDirectoryEmpty(t, manager.paths.Staging)
}

func TestUpdateReconcilesDeletedGameWithoutStartingRebuild(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if err := os.RemoveAll(manager.paths.Game); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	events = nil
	mu.Unlock()

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	if state := manager.State(); state.Status != StatusMissing || state.Error != "" {
		t.Fatalf("update did not reconcile the deleted installation: %+v", state)
	}
	mu.Lock()
	defer mu.Unlock()
	if containsStatus(events, StatusUpdating) {
		t.Fatalf("update or rebuild started after the installation disappeared: %+v", events)
	}
}

func TestGitJobFailurePrioritizesDeletedInstallationOverOperationError(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	fallback := manager.State()
	manager.mu.Lock()
	manager.running = true
	manager.state.Status = StatusChecking
	manager.mu.Unlock()
	if err := os.RemoveAll(manager.paths.Game); err != nil {
		t.Fatal(err)
	}

	manager.finishJob("fetch", errors.New("open game repository: repository does not exist"), fallback, "檢查更新失敗；目前版本仍可使用", "已取消檢查更新")
	state := manager.State()
	if state.Status != StatusMissing || state.Error != "" || state.Message != "尚未下載遊戲" {
		t.Fatalf("operation error won over missing-install reconciliation: %+v", state)
	}
}

func TestCloneInstallsShallowGitWorkingTree(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})

	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)

	state := manager.State()
	if state.Commit != remote.head(t) || state.CommitTime != remote.headTime(t) || state.RemoteCommitTime != remote.headTime(t) {
		t.Fatalf("unexpected ready state: %+v", state)
	}
	if err := validateGameRoot(manager.paths.Source); err != nil {
		t.Fatalf("game was not installed in shines871: %v", err)
	}
	installed, err := git.PlainOpen(manager.paths.Source)
	if err != nil {
		t.Fatalf("clone did not retain Git metadata: %v", err)
	}
	shallow, err := installed.Storer.Shallow()
	if err != nil || len(shallow) == 0 {
		t.Fatalf("clone is not shallow: %v (%v)", shallow, err)
	}
	assertManagedGameExclude(t, manager.paths.Source)
	mu.Lock()
	if !containsStatus(events, StatusInstalling) || !containsStatus(events, StatusReady) {
		mu.Unlock()
		t.Fatalf("missing clone events: %+v", events)
	}
	mu.Unlock()
}

func TestConcurrentInstallRequestsUseOneJob(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	errorsFound := make(chan error, 24)
	var starters sync.WaitGroup
	for range 24 {
		starters.Add(1)
		go func() {
			defer starters.Done()
			errorsFound <- manager.StartInstall()
		}()
	}
	starters.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	waitForStatus(t, manager, StatusReady)
	if manager.State().Commit != remote.head(t) {
		t.Fatalf("unexpected installed revision: %+v", manager.State())
	}
	assertDirectoryEmpty(t, manager.paths.Staging)
}

func TestCloneFailureIsRecoverableAndCleansStaging(t *testing.T) {
	manager := testManager(t, filepath.Join(t.TempDir(), "missing"), nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusError)
	if manager.State().Error == "" {
		t.Fatal("expected a useful clone error")
	}
	assertDirectoryEmpty(t, manager.paths.Staging)

	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager.repositoryURL = remote.path
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
}

func TestFetchDetectsBehindAndPullUpdatesOnlyWorkingTree(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	oldCommit := manager.State().Commit
	oldCommitTime := manager.State().CommitTime
	assetBefore, err := os.ReadFile(filepath.Join(manager.paths.Source, "assets", "image.png"))
	if err != nil {
		t.Fatal(err)
	}

	remote.commitFile(t, "css/app.css", "body{color:white}", "intermediate CSS update")
	newCommit := remote.commitFile(t, "js/app.js", "console.log('updated')", "update JavaScript")
	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusUpdateAvailable)
	state := manager.State()
	if !state.UpdateAvailable || state.Commit != oldCommit || state.CommitTime != oldCommitTime || state.RemoteCommit != newCommit || state.RemoteCommitTime != remote.headTime(t) {
		t.Fatalf("fetch did not identify the remote revision: %+v", state)
	}
	jsBefore, err := os.ReadFile(filepath.Join(manager.paths.Source, "js", "app.js"))
	if err != nil || string(jsBefore) != "console.log('ready')" {
		t.Fatalf("fetch changed the working tree: %q (%v)", jsBefore, err)
	}

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	state = manager.State()
	if state.Commit != newCommit || state.CommitTime != remote.headTime(t) || state.RemoteCommitTime != remote.headTime(t) || state.UpdateAvailable {
		t.Fatalf("pull did not activate the new revision: %+v", state)
	}
	jsAfter, err := os.ReadFile(filepath.Join(manager.paths.Source, "js", "app.js"))
	if err != nil || string(jsAfter) != "console.log('updated')" {
		t.Fatalf("working tree was not updated: %q (%v)", jsAfter, err)
	}
	assetAfter, err := os.ReadFile(filepath.Join(manager.paths.Source, "assets", "image.png"))
	if err != nil || !bytes.Equal(assetBefore, assetAfter) {
		t.Fatalf("unchanged asset was modified: %q (%v)", assetAfter, err)
	}
	if _, err := git.PlainOpen(manager.paths.Source); err != nil {
		t.Fatalf("updated source is no longer a Git working tree: %v", err)
	}
}

func TestHashOnlyCheckAndForceUpdateHandleDivergedDetachedHistory(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	base := remote.head(t)
	remote.commitFile(t, "js/app.js", "remote-old", "old official change")
	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)

	installed, err := git.PlainOpen(manager.paths.Source)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := installed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, manager.paths.Source, "local-only.txt", "local commit")
	if _, err := worktree.Add("local-only.txt"); err != nil {
		t.Fatal(err)
	}
	localHash, err := worktree.Commit("local divergent commit", &git.CommitOptions{Author: remote.signature()})
	if err != nil {
		t.Fatal(err)
	}
	if err := installed.Storer.SetReference(plumbing.NewHashReference(plumbing.HEAD, localHash)); err != nil {
		t.Fatal(err)
	}

	if err := remote.worktree.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: plumbing.NewHash(base)}); err != nil {
		t.Fatal(err)
	}
	want := remote.commitFile(t, "js/app.js", "remote-rewritten", "rewritten official history")
	mu.Lock()
	events = nil
	mu.Unlock()

	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusUpdateAvailable)
	if manager.State().RemoteCommit != want {
		t.Fatalf("hash-only check did not report rewritten remote: %+v", manager.State())
	}
	if head, err := installed.Head(); err != nil || head.Hash() != localHash || head.Type() != plumbing.HashReference {
		t.Fatalf("check changed detached local HEAD: %v (%v)", head, err)
	}
	if contents, err := os.ReadFile(filepath.Join(manager.paths.Source, "local-only.txt")); err != nil || string(contents) != "local commit" {
		t.Fatalf("check changed local working tree: %q (%v)", contents, err)
	}
	mu.Lock()
	checkEvents := append([]GameState(nil), events...)
	mu.Unlock()
	if containsStatus(checkEvents, StatusUpdating) || containsStatus(checkEvents, StatusInstalling) {
		t.Fatalf("hash-only check triggered a mutating state: %+v", checkEvents)
	}

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if manager.State().Commit != want {
		t.Fatalf("force update did not activate rewritten remote: %+v", manager.State())
	}
	if _, err := os.Lstat(filepath.Join(manager.paths.Source, "local-only.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("force update preserved local-only commit content: %v", err)
	}
	head, err := installed.Head()
	if err != nil || head.Name() != gameBranchReference || head.Hash().String() != want {
		t.Fatalf("force update did not normalize HEAD to main: %v (%v)", head, err)
	}
}

func TestUpdateRefetchesLatestRemoteAfterCheck(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	remote.commitFile(t, "js/app.js", "first", "first update")
	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusUpdateAvailable)
	want := remote.commitFile(t, "js/app.js", "second", "second update")

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if manager.State().Commit != want {
		t.Fatalf("update used stale revision from check: %+v", manager.State())
	}
}

func TestDevelopmentInstallFetchesOnlyPinnedCommitThenFindsUpdate(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	pinnedCommit := remote.head(t)
	pinnedFixtureReference := plumbing.NewBranchReferenceName("pinned-install-fixture")
	if err := remote.repository.Storer.SetReference(plumbing.NewHashReference(pinnedFixtureReference, plumbing.NewHash(pinnedCommit))); err != nil {
		t.Fatal(err)
	}
	remote.commitFile(t, "css/app.css", "body{color:white}", "first update")
	latestCommit := remote.commitFile(t, "js/app.js", "console.log('latest')", "second update")
	var eventMu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		eventMu.Lock()
		events = append(events, state)
		eventMu.Unlock()
	})
	manager.initialCommit = pinnedCommit
	var fetches initialFetchRecorder
	manager.initialFetch = func(ctx context.Context, repository *git.Repository, options *git.FetchOptions) error {
		fetches.record(options)
		mappedOptions := *options
		mappedOptions.RefSpecs = []config.RefSpec{config.RefSpec("+" + pinnedFixtureReference.String() + ":" + gameRemoteReference.String())}
		return repository.FetchContext(ctx, &mappedOptions)
	}

	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if manager.State().Commit != pinnedCommit {
		t.Fatalf("development install did not checkout the pinned commit: %+v", manager.State())
	}
	calls := fetches.snapshot()
	assertInitialFetchCalls(t, calls, []initialFetchCall{{
		remoteName: "origin",
		refSpecs:   []config.RefSpec{config.RefSpec(pinnedCommit + ":" + gameRemoteReference.String())},
		depth:      1,
		noTags:     true,
	}})

	installed, err := git.PlainOpen(manager.paths.Source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installed.CommitObject(plumbing.NewHash(latestCommit)); err == nil {
		t.Fatal("development install downloaded the latest main commit before update check")
	}
	assertDevelopmentRepositoryMetadata(t, installed, pinnedCommit)
	assertManagedGameExclude(t, manager.paths.Source)
	writeTestFile(t, manager.paths.Source, ".DS_Store", "finder")
	eventMu.Lock()
	events = nil
	eventMu.Unlock()

	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusUpdateAvailable)
	if manager.State().RemoteCommit != latestCommit {
		t.Fatalf("fetch did not find latest main: %+v", manager.State())
	}
	if contents, err := os.ReadFile(filepath.Join(manager.paths.Source, ".DS_Store")); err != nil || string(contents) != "finder" {
		t.Fatalf("update check changed Finder metadata: %q (%v)", contents, err)
	}
	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	state := manager.State()
	if state.Commit != latestCommit || state.Message != "更新完成；請重新整理或重新開啟瀏覽器頁面" {
		t.Fatalf("in-place development update did not reach latest main: %+v", state)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	if containsProgressPhase(events, "重新下載") {
		t.Fatalf("Finder metadata triggered a rebuild: %+v", events)
	}
}

func TestDevelopmentInstallFallsBackForLocalExactSHAUnsupported(t *testing.T) {
	for _, remoteURL := range []struct {
		name string
		url  func(string) string
	}{
		{name: "plain path", url: func(path string) string { return path }},
		{name: "file URL", url: localFileURL},
	} {
		t.Run(remoteURL.name, func(t *testing.T) {
			remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
			pinnedCommit := remote.head(t)
			latestCommit := remote.commitFile(t, "js/app.js", "console.log('latest')", "update")
			manager := testManager(t, remoteURL.url(remote.path), nil)
			manager.initialCommit = pinnedCommit
			var fetches initialFetchRecorder
			manager.initialFetch = func(ctx context.Context, repository *git.Repository, options *git.FetchOptions) error {
				callIndex := fetches.record(options)
				err := repository.FetchContext(ctx, options)
				if callIndex == 0 {
					if !errors.Is(err, git.ErrExactSHA1NotSupported) {
						t.Errorf("exact-SHA fetch error = %v, want %v", err, git.ErrExactSHA1NotSupported)
					}
					if count, countErr := countRepositoryObjects(repository); countErr != nil {
						t.Errorf("count objects after rejected exact-SHA fetch: %v", countErr)
					} else if count != 0 {
						t.Errorf("rejected exact-SHA fetch downloaded %d objects", count)
					}
				}
				return err
			}

			if err := manager.StartInstall(); err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, manager, StatusReady)
			if manager.State().Commit != pinnedCommit {
				t.Fatalf("fallback did not checkout pinned commit: %+v", manager.State())
			}
			assertInitialFetchCalls(t, fetches.snapshot(), []initialFetchCall{
				{
					remoteName: "origin",
					refSpecs:   []config.RefSpec{config.RefSpec(pinnedCommit + ":" + gameRemoteReference.String())},
					depth:      1,
					noTags:     true,
				},
				{
					remoteName: "origin",
					refSpecs:   []config.RefSpec{gameFetchRefSpec},
					depth:      0,
					noTags:     true,
				},
			})
			installed, err := git.PlainOpen(manager.paths.Source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := installed.CommitObject(plumbing.NewHash(latestCommit)); err != nil {
				t.Fatalf("full-main fallback did not fetch latest commit: %v", err)
			}
			assertDevelopmentRepositoryMetadata(t, installed, pinnedCommit)

			if err := manager.StartCheckForUpdate(); err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, manager, StatusUpdateAvailable)
			if manager.State().RemoteCommit != latestCommit {
				t.Fatalf("fallback update check did not find latest main: %+v", manager.State())
			}
			if err := manager.StartUpdate(); err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, manager, StatusReady)
			if manager.State().Commit != latestCommit {
				t.Fatalf("fallback update did not reach latest main: %+v", manager.State())
			}
		})
	}
}

func TestDevelopmentInstallCancellationDoesNotFallback(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	manager.initialCommit = remote.head(t)
	started := make(chan struct{})
	var fetches initialFetchRecorder
	manager.initialFetch = func(ctx context.Context, _ *git.Repository, options *git.FetchOptions) error {
		fetches.record(options)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}

	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	<-started
	manager.CancelInstall()
	waitForStatus(t, manager, StatusCancelled)
	assertSingleExactFetchAndNoInstallation(t, manager, fetches.snapshot())
}

func TestDevelopmentInstallNonCapabilityErrorsDoNotFallback(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		fetchError error
	}{
		{name: "network failure", fetchError: errors.New("test network failure")},
		{name: "already up to date without commit", fetchError: git.NoErrAlreadyUpToDate},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
			manager := testManager(t, remote.path, nil)
			manager.initialCommit = remote.head(t)
			var fetches initialFetchRecorder
			manager.initialFetch = func(_ context.Context, _ *git.Repository, options *git.FetchOptions) error {
				fetches.record(options)
				return testCase.fetchError
			}

			if err := manager.StartInstall(); err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, manager, StatusError)
			if manager.State().Error == "" {
				t.Fatal("failed development install did not report an error")
			}
			assertSingleExactFetchAndNoInstallation(t, manager, fetches.snapshot())
		})
	}
}

func TestDevelopmentInstallFallbackFailuresDoNotActivateSource(t *testing.T) {
	t.Run("fallback fetch fails", func(t *testing.T) {
		remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
		manager := testManager(t, remote.path, nil)
		manager.initialCommit = remote.head(t)
		fallbackError := errors.New("test fallback failure")
		var fetches initialFetchRecorder
		manager.initialFetch = func(_ context.Context, _ *git.Repository, options *git.FetchOptions) error {
			callIndex := fetches.record(options)
			if callIndex == 0 {
				return git.ErrExactSHA1NotSupported
			}
			return fallbackError
		}

		if err := manager.StartInstall(); err != nil {
			t.Fatal(err)
		}
		waitForStatus(t, manager, StatusError)
		if manager.State().Error != userFacingOperationError("install") {
			t.Fatalf("install error = %q, want user-facing install error", manager.State().Error)
		}
		assertExactThenFullMainFetches(t, fetches.snapshot(), manager.initialCommit)
		assertNoInstalledSource(t, manager)
	})

	t.Run("pinned commit is absent from main", func(t *testing.T) {
		remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
		manager := testManager(t, remote.path, nil)
		manager.initialCommit = strings.Repeat("1", 40)
		var fetches initialFetchRecorder
		manager.initialFetch = func(ctx context.Context, repository *git.Repository, options *git.FetchOptions) error {
			fetches.record(options)
			return repository.FetchContext(ctx, options)
		}

		if err := manager.StartInstall(); err != nil {
			t.Fatal(err)
		}
		waitForStatus(t, manager, StatusError)
		if manager.State().Error != userFacingOperationError("install") {
			t.Fatalf("install error = %q, want user-facing install error", manager.State().Error)
		}
		assertExactThenFullMainFetches(t, fetches.snapshot(), manager.initialCommit)
		assertNoInstalledSource(t, manager)
	})
}

func TestProductionInstallDoesNotUseDevelopmentInitialFetch(t *testing.T) {
	if developmentInitialGameCommit != "" {
		t.Skip("production-only behavior")
	}
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager, err := newGameManager(makeDataPaths(t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.repositoryURL = remote.path
	var fetches initialFetchRecorder
	manager.initialFetch = func(_ context.Context, _ *git.Repository, options *git.FetchOptions) error {
		fetches.record(options)
		return errors.New("development initial fetch must not run in production")
	}

	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if calls := fetches.snapshot(); len(calls) != 0 {
		t.Fatalf("production install used development initial fetch: %+v", calls)
	}
	if manager.State().Commit != remote.head(t) {
		t.Fatalf("production install did not clone main tip: %+v", manager.State())
	}
}

func TestFetchWhenCurrentRemainsReady(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	mu.Lock()
	events = nil
	mu.Unlock()
	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		state := manager.State()
		return state.Status == StatusReady && state.RemoteCommit != ""
	}, "up-to-date fetch")
	state := manager.State()
	if state.UpdateAvailable || state.RemoteCommit != state.Commit || state.RemoteCommitTime != state.CommitTime || state.CommitTime == "" {
		t.Fatalf("unexpected update result: %+v", state)
	}
	waitForCondition(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, event := range events {
			if event.Status == StatusReady && event.Message == "目前已是最新版本" {
				return true
			}
		}
		return false
	}, "ready revision event")
	mu.Lock()
	published := append([]GameState(nil), events...)
	mu.Unlock()
	var comparisonRevision, readyRevision uint64
	for _, event := range published {
		if event.Status == StatusChecking && event.ProgressPhase == "比較版本" {
			comparisonRevision = event.Revision
		}
		if event.Status == StatusReady && event.Message == "目前已是最新版本" {
			readyRevision = event.Revision
		}
	}
	if comparisonRevision == 0 || readyRevision <= comparisonRevision {
		t.Fatalf("terminal revision must supersede comparison progress: comparison=%d ready=%d events=%+v", comparisonRevision, readyRevision, published)
	}
}

func TestFailedUpdateCheckPreservesLocalCommitTime(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	before := manager.State()
	writeTestFile(t, manager.paths.Source, "js/app.js", "offline local copy")

	manager.repositoryURL = filepath.Join(t.TempDir(), "missing")
	mu.Lock()
	events = nil
	mu.Unlock()

	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		state := manager.State()
		return state.Status == StatusReady && state.Error != ""
	}, "failed update check")
	after := manager.State()
	if after.Commit != before.Commit || after.CommitTime != before.CommitTime {
		t.Fatalf("failed update check lost local version metadata: before=%+v after=%+v", before, after)
	}
	if contents, err := os.ReadFile(filepath.Join(manager.paths.Source, "js", "app.js")); err != nil || string(contents) != "offline local copy" {
		t.Fatalf("failed network check changed local content: %q (%v)", contents, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if containsStatus(events, StatusUpdating) || containsStatus(events, StatusInstalling) {
		t.Fatalf("failed network check triggered a mutating state: %+v", events)
	}
}

func TestCheckIgnoresLocalRemoteMetadata(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	want := remote.commitFile(t, "js/app.js", "remote", "remote update")

	installed, err := git.PlainOpen(manager.paths.Source)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := installed.Config()
	if err != nil {
		t.Fatal(err)
	}
	delete(configuration.Remotes, "origin")
	configuration.Branches[gameBranch] = &config.Branch{Name: gameBranch, Remote: "wrong", Merge: plumbing.NewBranchReferenceName("wrong")}
	if err := installed.Storer.SetConfig(configuration); err != nil {
		t.Fatal(err)
	}
	_ = installed.Storer.RemoveReference(gameRemoteReference)

	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusUpdateAvailable)
	if manager.State().RemoteCommit != want {
		t.Fatalf("check did not fetch the configured official revision: %+v", manager.State())
	}
	unchanged, err := installed.Config()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := unchanged.Remotes["origin"]; exists {
		t.Fatalf("check rewrote local origin metadata: %+v", unchanged.Remotes["origin"])
	}
	branch := unchanged.Branches[gameBranch]
	if branch == nil || branch.Remote != "wrong" || branch.Merge != plumbing.NewBranchReferenceName("wrong") {
		t.Fatalf("check rewrote local branch metadata: %+v", branch)
	}
}

func TestHashOnlyCheckDoesNotChangeDirtyCurrentWorkingTree(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	installed, err := git.PlainOpen(manager.paths.Source)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := installed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, manager.paths.Source, "js/app.js", "local current-head edit")
	writeTestFile(t, manager.paths.Source, "css/app.css", "staged local edit")
	if _, err := worktree.Add("css/app.css"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(manager.paths.Source, "assets", "image.png")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, manager.paths.Source, ".DS_Store", "finder")
	writeTestFile(t, manager.paths.Source, "mods/local.txt", "untracked")
	writeTestFile(t, manager.paths.Source, ".gitignore", "ignored.cache\n")
	writeTestFile(t, manager.paths.Source, "ignored.cache", "ignored")
	statusBefore, err := worktree.Status()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := statusBefore["ignored.cache"]; exists {
		t.Fatalf("test fixture did not create an ignored file: %v", statusBefore)
	}
	headBefore, err := installed.Head()
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	events = nil
	mu.Unlock()

	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		state := manager.State()
		return state.Status == StatusReady && state.RemoteCommit != ""
	}, "dirty current-head check")
	if manager.State().UpdateAvailable {
		t.Fatalf("equal HEAD hashes were reported as an update: %+v", manager.State())
	}
	headAfter, err := installed.Head()
	if err != nil || headAfter.Hash() != headBefore.Hash() || headAfter.Name() != headBefore.Name() {
		t.Fatalf("check changed local HEAD: before=%v after=%v (%v)", headBefore, headAfter, err)
	}
	statusAfter, err := worktree.Status()
	if err != nil || !reflect.DeepEqual(statusAfter, statusBefore) {
		t.Fatalf("check changed working-tree status: before=%v after=%v (%v)", statusBefore, statusAfter, err)
	}
	if contents, err := os.ReadFile(filepath.Join(manager.paths.Source, "js", "app.js")); err != nil || string(contents) != "local current-head edit" {
		t.Fatalf("check overwrote tracked local content: %q (%v)", contents, err)
	}
	for relative, want := range map[string]string{
		"css/app.css":    "staged local edit",
		".DS_Store":      "finder",
		".gitignore":     "ignored.cache\n",
		"mods/local.txt": "untracked",
		"ignored.cache":  "ignored",
	} {
		contents, err := os.ReadFile(filepath.Join(manager.paths.Source, filepath.FromSlash(relative)))
		if err != nil || string(contents) != want {
			t.Fatalf("check changed local path %q: %q (%v)", relative, contents, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(manager.paths.Source, "assets", "image.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check restored a locally deleted file: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if containsStatus(events, StatusUpdating) || containsStatus(events, StatusInstalling) {
		t.Fatalf("hash-only check triggered a mutating state: %+v", events)
	}
}

func TestStartupUpdateCheckUsesNonMutatingCheckPath(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	var mu sync.Mutex
	var events []GameState
	manager := testManager(t, remote.path, func(state GameState) {
		mu.Lock()
		events = append(events, state)
		mu.Unlock()
	})
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	want := remote.commitFile(t, "js/app.js", "official update", "startup update")
	writeTestFile(t, manager.paths.Source, "js/app.js", "local startup edit")
	writeTestFile(t, manager.paths.Source, ".DS_Store", "finder")
	mu.Lock()
	events = nil
	mu.Unlock()

	startStartupUpdateCheck(manager)
	waitForStatus(t, manager, StatusUpdateAvailable)
	if manager.State().RemoteCommit != want {
		t.Fatalf("startup check did not report the remote hash: %+v", manager.State())
	}
	if contents, err := os.ReadFile(filepath.Join(manager.paths.Source, "js", "app.js")); err != nil || string(contents) != "local startup edit" {
		t.Fatalf("startup check changed tracked content: %q (%v)", contents, err)
	}
	if contents, err := os.ReadFile(filepath.Join(manager.paths.Source, ".DS_Store")); err != nil || string(contents) != "finder" {
		t.Fatalf("startup check changed untracked content: %q (%v)", contents, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if containsStatus(events, StatusUpdating) || containsStatus(events, StatusInstalling) {
		t.Fatalf("startup check triggered a mutating state: %+v", events)
	}
}

func TestForceUpdateOverwritesDirtyTreeAndRemovesAllNonOfficialFiles(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	remote.commitFile(t, "js/app.js", "remote", "remote change")
	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusUpdateAvailable)
	installed, err := git.PlainOpen(manager.paths.Source)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := installed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(manager.paths.Source, "js", "app.js")
	if err := os.WriteFile(localPath, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("js/app.js"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(manager.paths.Source, "css", "app.css")); err != nil {
		t.Fatal(err)
	}
	for relative, contents := range map[string]string{
		".DS_Store":                  "finder",
		"public/.DS_Store":           "finder",
		"ignored.cache":              "ignored",
		"mods/local/.git/config":     "nested repository",
		"untracked/nested/extra.txt": "extra",
	} {
		writeTestFile(t, manager.paths.Source, relative, contents)
	}
	writeTestFile(t, filepath.Join(manager.paths.Source, ".git", "info"), "exclude", "ignored.cache\n")

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	contents, err := os.ReadFile(localPath)
	if err != nil || string(contents) != "remote" {
		t.Fatalf("tracked local change was not overwritten: %q (%v)", contents, err)
	}
	css, err := os.ReadFile(filepath.Join(manager.paths.Source, "css", "app.css"))
	if err != nil || string(css) != "body{}" {
		t.Fatalf("deleted tracked file was not restored: %q (%v)", css, err)
	}
	for _, relative := range []string{".DS_Store", "public", "ignored.cache", "mods", "untracked"} {
		if _, err := os.Lstat(filepath.Join(manager.paths.Source, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("non-official path %q remains after update: %v", relative, err)
		}
	}
	status, err := worktree.Status()
	if err != nil || !status.IsClean() {
		t.Fatalf("updated working tree is not clean: %v (%v)", status, err)
	}
	head, err := installed.Head()
	if err != nil || head.Hash().String() != remote.head(t) || head.Name() != gameBranchReference {
		t.Fatalf("updated HEAD was not normalized to official main: %v (%v)", head, err)
	}
	assertManagedGameExclude(t, manager.paths.Source)
}

func TestSynchronizedGameTreeAllowsFinderMetadataRecreatedAfterCleanup(t *testing.T) {
	repository := newLocalGameRepository(t, filepath.Join(t.TempDir(), "game"))
	manifest := gameManifestAtHead(t, repository)
	if err := cleanGameTreeToManifest(repository.path, manifest); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{".DS_Store", "assets/.DS_Store"} {
		writeTestFile(t, repository.path, relative, "finder")
	}
	status, err := repository.worktree.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{".DS_Store", "assets/.DS_Store"} {
		if !status.IsUntracked(relative) {
			t.Fatalf("fixture did not expose %q as untracked: %v", relative, status)
		}
	}
	if err := validateSynchronizedGameTree(repository.repository, repository.worktree, repository.path, manifest); err != nil {
		t.Fatalf("recreated Finder metadata failed final verification: %v", err)
	}
	for _, relative := range []string{".DS_Store", "assets/.DS_Store"} {
		contents, err := os.ReadFile(filepath.Join(repository.path, filepath.FromSlash(relative)))
		if err != nil || string(contents) != "finder" {
			t.Fatalf("allowed Finder metadata %q was changed: %q (%v)", relative, contents, err)
		}
	}
}

func TestForceSynchronizeGameTreeLeavesFinderMetadataWhenCleanupCannotRemoveIt(t *testing.T) {
	repository := newLocalGameRepository(t, filepath.Join(t.TempDir(), "game"))
	manifest := gameManifestAtHead(t, repository)
	writeTestFile(t, repository.path, "js/app.js", "local change")
	writeTestFile(t, repository.path, finderMetadata, "finder")
	removeAttempts := 0
	removePath := func(absolute string) error {
		if filepath.Base(absolute) == finderMetadata {
			removeAttempts++
			return os.ErrPermission
		}
		return os.RemoveAll(absolute)
	}

	if err := forceSynchronizeGameTreeWithRemover(
		repository.repository,
		repository.worktree,
		repository.path,
		plumbing.NewHash(repository.head(t)),
		manifest,
		removePath,
	); err != nil {
		t.Fatalf("Finder metadata removal failure caused synchronization failure: %v", err)
	}
	if removeAttempts != 2 {
		t.Fatalf("Finder metadata cleanup attempts = %d, want 2", removeAttempts)
	}
	contents, err := os.ReadFile(filepath.Join(repository.path, finderMetadata))
	if err != nil || string(contents) != "finder" {
		t.Fatalf("hard reset changed Finder metadata after cleanup failure: %q (%v)", contents, err)
	}
	tracked, err := os.ReadFile(filepath.Join(repository.path, "js", "app.js"))
	if err != nil || string(tracked) != "console.log('ready')" {
		t.Fatalf("hard reset did not restore official file: %q (%v)", tracked, err)
	}
}

func TestForceSynchronizeGameTreeRestoresSkipWorktreeTrackedFinderMetadata(t *testing.T) {
	repository := newLocalGameRepository(t, filepath.Join(t.TempDir(), "game"))
	officialCommit := repository.commitFile(t, finderMetadata, "official", "track Finder metadata")
	manifest := gameManifestAtHead(t, repository)
	index, err := repository.repository.Storer.Index()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := index.Entry(finderMetadata)
	if err != nil {
		t.Fatal(err)
	}
	entry.SkipWorktree = true
	if err := repository.repository.Storer.SetIndex(index); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, repository.path, finderMetadata, "modified")

	if err := forceSynchronizeGameTree(
		repository.repository,
		repository.worktree,
		repository.path,
		plumbing.NewHash(officialCommit),
		manifest,
	); err != nil {
		t.Fatalf("skip-worktree tracked Finder metadata failed synchronization: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(repository.path, finderMetadata))
	if err != nil || string(contents) != "official" {
		t.Fatalf("tracked Finder metadata was not restored: %q (%v)", contents, err)
	}
	index, err = repository.repository.Storer.Index()
	if err != nil {
		t.Fatal(err)
	}
	entry, err = index.Entry(finderMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if entry.SkipWorktree {
		t.Fatal("synchronized index retained skip-worktree")
	}
}

func TestWalkManagedGameTreeAllowsEntriesToDisappearAfterDirectoryRead(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a", "first")
	writeTestFile(t, root, "b", "second")
	err := walkManagedGameTree(root, "", func(relative, _ string, _ os.FileInfo) (bool, error) {
		if relative == "a" {
			if err := os.Remove(filepath.Join(root, "b")); err != nil {
				t.Fatal(err)
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("disappearing directory entry failed the walk: %v", err)
	}
}

func TestSynchronizedGameTreeRejectsNonFinderMetadataExceptions(t *testing.T) {
	tests := []struct {
		name             string
		configureExclude bool
		prepareOfficial  func(*testing.T, *localGameRepository)
		mutate           func(*testing.T, *localGameRepository)
	}{
		{
			name:             "similar filename",
			configureExclude: true,
			mutate: func(t *testing.T, repository *localGameRepository) {
				writeTestFile(t, repository.path, ".DS_Store.backup", "local")
			},
		},
		{
			name:             "directory",
			configureExclude: true,
			mutate: func(t *testing.T, repository *localGameRepository) {
				if err := os.Mkdir(filepath.Join(repository.path, ".DS_Store"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:             "symlink",
			configureExclude: true,
			mutate: func(t *testing.T, repository *localGameRepository) {
				if err := os.Symlink("index.html", filepath.Join(repository.path, ".DS_Store")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
		{
			name: "staged untracked file",
			mutate: func(t *testing.T, repository *localGameRepository) {
				writeTestFile(t, repository.path, ".DS_Store", "staged")
				if _, err := repository.worktree.Add(".DS_Store"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:             "tracked file modified locally",
			configureExclude: true,
			prepareOfficial: func(t *testing.T, repository *localGameRepository) {
				repository.commitFile(t, ".DS_Store", "official", "track Finder metadata")
			},
			mutate: func(t *testing.T, repository *localGameRepository) {
				writeTestFile(t, repository.path, ".DS_Store", "modified")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newLocalGameRepository(t, filepath.Join(t.TempDir(), "game"))
			if test.prepareOfficial != nil {
				test.prepareOfficial(t, repository)
			}
			manifest := gameManifestAtHead(t, repository)
			if test.configureExclude {
				if err := ensureGitInfoExclude(repository.path); err != nil {
					t.Fatal(err)
				}
				configureManagedGameWorktree(repository.worktree)
			}
			test.mutate(t, repository)
			if err := validateSynchronizedGameTree(repository.repository, repository.worktree, repository.path, manifest); err == nil {
				t.Fatal("non-Finder metadata exception passed final verification")
			}
		})
	}
}

func TestSynchronizedGameTreeRejectsTrackedFinderMetadataMissingFromManifest(t *testing.T) {
	repository := newLocalGameRepository(t, filepath.Join(t.TempDir(), "game"))
	officialManifest := gameManifestAtHead(t, repository)
	repository.commitFile(t, finderMetadata, "tracked", "track Finder metadata")
	if err := ensureGitInfoExclude(repository.path); err != nil {
		t.Fatal(err)
	}
	configureManagedGameWorktree(repository.worktree)
	status, err := repository.worktree.Status()
	if err != nil || !status.IsClean() {
		t.Fatalf("tracked Finder metadata fixture is not clean: %v (%v)", status, err)
	}
	if err := validateSynchronizedGameTree(repository.repository, repository.worktree, repository.path, officialManifest); err == nil {
		t.Fatal("tracked Finder metadata absent from the official manifest passed verification")
	}
}

func TestUpdateRebuildsLatestVersionWhenIndexIsCorrupt(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	want := remote.commitFile(t, "js/app.js", "re-downloaded", "remote update")
	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusUpdateAvailable)
	if err := os.WriteFile(filepath.Join(manager.paths.Source, ".git", "index"), []byte("corrupt index"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	state := manager.State()
	if state.Commit != want || state.Message != "更新完成；已重新下載官方最新版本" {
		t.Fatalf("corrupt index was not rebuilt to latest remote: %+v", state)
	}
	installed, err := git.PlainOpen(manager.paths.Source)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := installed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	status, err := worktree.Status()
	if err != nil || !status.IsClean() {
		t.Fatalf("rebuilt repository is not clean: %v (%v)", status, err)
	}
	assertManagedGameExclude(t, manager.paths.Source)
}

func TestInstallReplacesInvalidShines871SourceOnRetry(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	paths := makeDataPaths(t.TempDir())
	writeTestFile(t, paths.Source, "broken.txt", "not a game")
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.repositoryURL = remote.path
	manager.initialCommit = ""

	if manager.State().Status != StatusError {
		t.Fatalf("expected invalid installation error, got %+v", manager.State())
	}
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if manager.State().Commit != remote.head(t) {
		t.Fatalf("invalid install was not replaced: %+v", manager.State())
	}
	if _, err := git.PlainOpen(paths.Source); err != nil {
		t.Fatalf("recovered install is not a Git working tree: %v", err)
	}
}

func TestInstalledGameRemainsActiveDuringUpdateOperations(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	root, commit, ready := manager.ActiveVersion()
	if !ready || root != manager.paths.Source || commit == "" {
		t.Fatalf("active version disappeared during fetch: %q %q %v", root, commit, ready)
	}
	waitForCondition(t, func() bool { return manager.State().Status != StatusChecking }, "fetch completion")
}

func TestInitialiseRemovesStaleStaging(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	stale := filepath.Join(paths.Staging, "interrupted", "file")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.State().Status != StatusMissing {
		t.Fatalf("unexpected state: %+v", manager.State())
	}
	assertDirectoryEmpty(t, paths.Staging)
}

func TestGitProgressReporterPublishesSidebandAndHeartbeat(t *testing.T) {
	manager, err := newGameManager(makeDataPaths(t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.state = GameState{Status: StatusInstalling, ProgressPercent: -1}
	manager.mu.Unlock()
	var logs bytes.Buffer
	manager.logger = slog.New(slog.NewTextHandler(&logs, nil))

	reporter := newGitProgressReporter(manager, "clone", "正在連線…")
	if _, err := reporter.Write([]byte("Counting objects: 42% (42/100)\r")); err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.ProgressPhase != "計算遊戲檔案" || state.ProgressPercent != 42 || state.ProgressText != "計算遊戲檔案：42%" {
		t.Fatalf("unexpected parsed progress: %+v", state)
	}
	waitForCondition(t, func() bool { return manager.State().ProgressSeconds >= 1 }, "Git progress heartbeat")
	reporter.Close()
	if output := logs.String(); !strings.Contains(output, "git stage") || !strings.Contains(output, "git progress") {
		t.Fatalf("progress logging is incomplete: %s", output)
	}
}

func TestGitProgressReporterShowsPackfileReceiveAfterCompression(t *testing.T) {
	manager, err := newGameManager(makeDataPaths(t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.state = GameState{Status: StatusInstalling, ProgressPercent: -1}
	manager.mu.Unlock()
	var logs bytes.Buffer
	manager.logger = slog.New(slog.NewTextHandler(&logs, nil))

	packDir := filepath.Join(t.TempDir(), "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packDir, "pack-existing.pack"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	reporter := newGitProgressReporter(manager, "clone", "正在連線…")
	reporter.WatchPackDir(packDir)
	defer reporter.Close()
	if _, err := reporter.Write([]byte("Compressing objects: 100% (109319/109319)\r")); err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.ProgressPhase != "壓縮遊戲檔案" || state.ProgressPercent != 100 {
		t.Fatalf("unexpected compression progress: %+v", state)
	}
	if err := os.WriteFile(filepath.Join(packDir, "tmp_pack_receiving"), make([]byte, 5*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	reporter.mu.Lock()
	syntheticTime := reporter.lastGitOutput.Add(gitPackfileReceiveDelay + time.Millisecond)
	reporter.mu.Unlock()
	if !reporter.maybeShowSyntheticPackfileReceive(syntheticTime) {
		t.Fatal("expected synthetic packfile receive phase")
	}
	state = manager.State()
	if state.ProgressPhase != gitPackfileReceivePhase || state.ProgressPercent != -1 || !strings.Contains(state.ProgressText, "5.0 MiB") {
		t.Fatalf("unexpected synthetic receive progress: %+v", state)
	}
	if output := logs.String(); !strings.Contains(output, gitPackfileReceivePhase) {
		t.Fatalf("synthetic receive phase was not logged: %s", output)
	}

	if _, err := reporter.Write([]byte("Receiving objects: 12% (13118/109319)\r")); err != nil {
		t.Fatal(err)
	}
	state = manager.State()
	if state.ProgressPhase != "接收遊戲檔案" || state.ProgressPercent != 12 || state.ProgressText != "接收遊戲檔案：12%" {
		t.Fatalf("real receive progress did not replace synthetic phase: %+v", state)
	}
}

type initialFetchCall struct {
	remoteName string
	refSpecs   []config.RefSpec
	depth      int
	noTags     bool
}

type initialFetchRecorder struct {
	mu    sync.Mutex
	calls []initialFetchCall
}

func (recorder *initialFetchRecorder) record(options *git.FetchOptions) int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	call := initialFetchCall{
		remoteName: options.RemoteName,
		refSpecs:   append([]config.RefSpec(nil), options.RefSpecs...),
		depth:      options.Depth,
		noTags:     options.Tags == git.NoTags,
	}
	recorder.calls = append(recorder.calls, call)
	return len(recorder.calls) - 1
}

func (recorder *initialFetchRecorder) snapshot() []initialFetchCall {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	calls := make([]initialFetchCall, len(recorder.calls))
	for index, call := range recorder.calls {
		calls[index] = call
		calls[index].refSpecs = append([]config.RefSpec(nil), call.refSpecs...)
	}
	return calls
}

func assertInitialFetchCalls(t *testing.T, got, want []initialFetchCall) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("initial fetch call count = %d, want %d; calls=%+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].remoteName != want[index].remoteName || got[index].depth != want[index].depth || got[index].noTags != want[index].noTags {
			t.Errorf("initial fetch call %d = %+v, want %+v", index, got[index], want[index])
		}
		if len(got[index].refSpecs) != len(want[index].refSpecs) {
			t.Errorf("initial fetch call %d refspecs = %v, want %v", index, got[index].refSpecs, want[index].refSpecs)
			continue
		}
		for refIndex := range want[index].refSpecs {
			if got[index].refSpecs[refIndex] != want[index].refSpecs[refIndex] {
				t.Errorf("initial fetch call %d refspecs = %v, want %v", index, got[index].refSpecs, want[index].refSpecs)
				break
			}
		}
	}
}

func assertSingleExactFetchAndNoInstallation(t *testing.T, manager *gameManager, calls []initialFetchCall) {
	t.Helper()
	assertInitialFetchCalls(t, calls, []initialFetchCall{{
		remoteName: "origin",
		refSpecs:   []config.RefSpec{config.RefSpec(manager.initialCommit + ":" + gameRemoteReference.String())},
		depth:      1,
		noTags:     true,
	}})
	assertNoInstalledSource(t, manager)
}

func assertExactThenFullMainFetches(t *testing.T, calls []initialFetchCall, pinnedCommit string) {
	t.Helper()
	assertInitialFetchCalls(t, calls, []initialFetchCall{
		{
			remoteName: "origin",
			refSpecs:   []config.RefSpec{config.RefSpec(pinnedCommit + ":" + gameRemoteReference.String())},
			depth:      1,
			noTags:     true,
		},
		{
			remoteName: "origin",
			refSpecs:   []config.RefSpec{gameFetchRefSpec},
			depth:      0,
			noTags:     true,
		},
	})
}

func assertNoInstalledSource(t *testing.T, manager *gameManager) {
	t.Helper()
	if _, err := os.Lstat(manager.paths.Source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed install left source behind: %v", err)
	}
	assertDirectoryEmpty(t, manager.paths.Staging)
	if root, commit, ready := manager.ActiveVersion(); ready || root != "" || commit != "" {
		t.Fatalf("failed install activated a version: %q %q %v", root, commit, ready)
	}
}

func assertDevelopmentRepositoryMetadata(t *testing.T, repository *git.Repository, pinnedCommit string) {
	t.Helper()
	pinnedHash := plumbing.NewHash(pinnedCommit)
	head, err := repository.Reference(plumbing.HEAD, false)
	if err != nil {
		t.Fatal(err)
	}
	if head.Type() != plumbing.SymbolicReference || head.Target() != gameBranchReference {
		t.Errorf("HEAD = %s, want symbolic reference to %s", head, gameBranchReference)
	}
	for _, referenceName := range []plumbing.ReferenceName{gameBranchReference, gameRemoteReference} {
		reference, err := repository.Reference(referenceName, false)
		if err != nil {
			t.Errorf("read %s: %v", referenceName, err)
			continue
		}
		if reference.Hash() != pinnedHash {
			t.Errorf("%s = %s, want %s", referenceName, reference.Hash(), pinnedHash)
		}
	}
	shallow, err := repository.Storer.Shallow()
	if err != nil {
		t.Fatal(err)
	}
	if len(shallow) != 1 || shallow[0] != pinnedHash {
		t.Errorf("shallow boundaries = %v, want [%s]", shallow, pinnedHash)
	}
	repositoryConfig, err := repository.Config()
	if err != nil {
		t.Fatal(err)
	}
	origin := repositoryConfig.Remotes["origin"]
	if origin == nil {
		t.Error("origin remote is not configured")
	} else if len(origin.Fetch) != 1 || origin.Fetch[0] != gameFetchRefSpec {
		t.Errorf("origin fetch refspecs = %v, want [%s]", origin.Fetch, gameFetchRefSpec)
	}
	mainBranch := repositoryConfig.Branches[gameBranch]
	if mainBranch == nil {
		t.Error("main branch tracking is not configured")
	} else if mainBranch.Remote != "origin" || mainBranch.Merge != gameBranchReference {
		t.Errorf("main branch tracking = remote %q merge %q, want origin %q", mainBranch.Remote, mainBranch.Merge, gameBranchReference)
	}
}

func countRepositoryObjects(repository *git.Repository) (int, error) {
	objects, err := repository.Storer.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return 0, err
	}
	count := 0
	err = objects.ForEach(func(plumbing.EncodedObject) error {
		count++
		return nil
	})
	return count, err
}

func localFileURL(path string) string {
	slashPath := filepath.ToSlash(path)
	if len(slashPath) >= 2 && slashPath[1] == ':' {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func testManager(t *testing.T, repositoryURL string, emit stateEmitter) *gameManager {
	t.Helper()
	manager, err := newGameManager(makeDataPaths(t.TempDir()), emit)
	if err != nil {
		t.Fatal(err)
	}
	manager.repositoryURL = repositoryURL
	manager.initialCommit = ""
	return manager
}

func newLocalGameRepository(t *testing.T, root string) *localGameRepository {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	fixture := &localGameRepository{
		path:       root,
		repository: repository,
		worktree:   worktree,
		clock:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	writeValidGame(t, root)
	if err := os.WriteFile(filepath.Join(root, "assets", "image.png"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "css", "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "js", "app.js"), []byte("console.log('ready')"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("."); err != nil {
		t.Fatal(err)
	}
	hash, err := worktree.Commit("initial game", &git.CommitOptions{Author: fixture.signature()})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Storer.SetReference(plumbing.NewHashReference(gameBranchReference, hash)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, gameBranchReference)); err != nil {
		t.Fatal(err)
	}
	_ = repository.Storer.RemoveReference(plumbing.NewBranchReferenceName("master"))
	return fixture
}

func (repository *localGameRepository) commitFile(t *testing.T, relative, contents, message string) string {
	t.Helper()
	path := filepath.Join(repository.path, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.worktree.Add(relative); err != nil {
		t.Fatal(err)
	}
	hash, err := repository.worktree.Commit(message, &git.CommitOptions{Author: repository.signature()})
	if err != nil {
		t.Fatal(err)
	}
	return hash.String()
}

func (repository *localGameRepository) head(t *testing.T) string {
	t.Helper()
	head, err := repository.repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	return head.Hash().String()
}

func (repository *localGameRepository) headTime(t *testing.T) string {
	t.Helper()
	head, err := repository.repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repository.repository.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	return commit.Committer.When.Format(time.RFC3339)
}

func (repository *localGameRepository) signature() *object.Signature {
	repository.clock = repository.clock.Add(time.Minute)
	return &object.Signature{Name: "Launcher Test", Email: "launcher@example.test", When: repository.clock}
}

func writeValidGame(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"assets", "css", "js"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("game"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stringPointer(value string) *string {
	return &value
}

func assertManagedGameExclude(t *testing.T, root string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSuffix(line, "\r") == finderMetadata {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("managed Finder exclude count = %d, want 1; contents=%q", count, contents)
	}
}

func gameManifestAtHead(t *testing.T, repository *localGameRepository) map[string]gameTreePathKind {
	t.Helper()
	head, err := repository.repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repository.repository.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := gameCommitManifest(commit)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func waitForStatus(t *testing.T, manager *gameManager, status GameStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if manager.State().Status == status {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last state: %+v", status, manager.State())
}

func waitForCondition(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func assertDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected %s to be empty, found %v", directory, entries)
	}
}

func containsStatus(states []GameState, status GameStatus) bool {
	for _, state := range states {
		if state.Status == status {
			return true
		}
	}
	return false
}

func containsProgressPhase(states []GameState, phase string) bool {
	for _, state := range states {
		if state.ProgressPhase == phase {
			return true
		}
	}
	return false
}
