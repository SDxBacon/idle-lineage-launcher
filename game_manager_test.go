package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
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
	if state.Status != StatusReady || state.Commit != commit {
		t.Fatalf("unexpected state: %+v", state)
	}
	root, activeCommit, ready := manager.ActiveVersion()
	if !ready || root != paths.Source || activeCommit != commit {
		t.Fatalf("unexpected active version: %q %q %v", root, activeCommit, ready)
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
	if state.Commit != remote.head(t) {
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
	if !state.UpdateAvailable || state.Commit != oldCommit || state.RemoteCommit != newCommit {
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
	if state.Commit != newCommit || state.UpdateAvailable {
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

func TestDevelopmentInstallFetchesOnlyPinnedCommitThenFindsUpdate(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	pinnedCommit := remote.head(t)
	pinnedFixtureReference := plumbing.NewBranchReferenceName("pinned-install-fixture")
	if err := remote.repository.Storer.SetReference(plumbing.NewHashReference(pinnedFixtureReference, plumbing.NewHash(pinnedCommit))); err != nil {
		t.Fatal(err)
	}
	remote.commitFile(t, "css/app.css", "body{color:white}", "first update")
	latestCommit := remote.commitFile(t, "js/app.js", "console.log('latest')", "second update")
	manager := testManager(t, remote.path, nil)
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

	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusUpdateAvailable)
	if manager.State().RemoteCommit != latestCommit {
		t.Fatalf("fetch did not find latest main: %+v", manager.State())
	}
	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if manager.State().Commit != latestCommit {
		t.Fatalf("pull did not reach latest main: %+v", manager.State())
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
		if !strings.Contains(manager.State().Error, fallbackError.Error()) {
			t.Fatalf("install error = %q, want fallback error", manager.State().Error)
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
		if !strings.Contains(manager.State().Error, "read development initial commit") {
			t.Fatalf("install error = %q, want missing pinned commit error", manager.State().Error)
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
	if state.UpdateAvailable || state.RemoteCommit != state.Commit {
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

func TestPullRefusesDirtyWorkingTree(t *testing.T) {
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
	localPath := filepath.Join(manager.paths.Source, "js", "app.js")
	if err := os.WriteFile(localPath, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		state := manager.State()
		return state.Status == StatusUpdateAvailable && state.Error != ""
	}, "dirty-worktree rejection")
	contents, err := os.ReadFile(localPath)
	if err != nil || string(contents) != "local" {
		t.Fatalf("dirty file was overwritten: %q (%v)", contents, err)
	}
}

func TestInstallReplacesInvalidShines871SourceOnRetry(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	paths := makeDataPaths(t.TempDir())
	writeValidGame(t, paths.Source)
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
	if state.ProgressPhase != "計算 Git objects" || state.ProgressPercent != 42 || state.ProgressText != "Counting objects: 42% (42/100)" {
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
	if state.ProgressPhase != "壓縮 Git objects" || state.ProgressPercent != 100 {
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
	if state.ProgressPhase != "接收 Git objects" || state.ProgressPercent != 12 || state.ProgressText != "Receiving objects: 12% (13118/109319)" {
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
