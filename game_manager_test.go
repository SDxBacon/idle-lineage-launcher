package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

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

func TestGitHeadReplacesAndRemovesLegacyManifest(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	repository := newLocalGameRepository(t, paths.Source)
	writeLegacyManifest(t, paths, testCommit)

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.State().Commit != repository.head(t) {
		t.Fatalf("manager did not use Git HEAD: %+v", manager.State())
	}
	if _, err := os.Stat(legacyManifestPath(paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy manifest was not removed: %v", err)
	}
}

func TestExistingLegacyVersionMigratesToSingleSource(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	legacyRoot := filepath.Join(paths.Game, "versions", testCommit)
	writeValidGame(t, legacyRoot)
	writeValidGame(t, filepath.Join(paths.Game, "versions", strings.Repeat("a", 40)))
	writeLegacyManifest(t, paths, testCommit)

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, _, ready := manager.ActiveVersion()
	if !ready || root != paths.Source || !manager.legacyInstall {
		t.Fatalf("legacy game was not migrated: %q %v legacy=%v", root, ready, manager.legacyInstall)
	}
	if err := validateGameRoot(paths.Source); err != nil {
		t.Fatalf("migrated source is invalid: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.Game, "versions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy versions directory still exists: %v", err)
	}
}

func TestMissingManifestRemovesOrphanedGameData(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	writeValidGame(t, filepath.Join(paths.Game, "versions", testCommit))
	writeValidGame(t, paths.Source)

	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.State().Status != StatusMissing {
		t.Fatalf("unexpected state: %+v", manager.State())
	}
	for _, path := range []string{filepath.Join(paths.Game, "versions"), paths.Source} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphaned game data still exists at %s: %v", path, err)
		}
	}
}

func TestCloneInstallsGitWorkingTreeAndServesAssets(t *testing.T) {
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
		t.Fatalf("game was not installed in src: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.paths.Source, ".git")); err != nil {
		t.Fatalf("clone did not retain Git metadata: %v", err)
	}
	if _, err := os.Stat(legacyManifestPath(manager.paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clone wrote an active manifest: %v", err)
	}
	mu.Lock()
	if !containsStatus(events, StatusInstalling) || !containsStatus(events, StatusReady) {
		mu.Unlock()
		t.Fatalf("missing clone events: %+v", events)
	}
	mu.Unlock()

	request := httptest.NewRequest(http.MethodGet, "/game/js/app.js", nil)
	response := httptest.NewRecorder()
	serveGameAsset(manager, response, request)
	if response.Code != http.StatusOK || response.Body.String() != "console.log('ready')" {
		t.Fatalf("unexpected asset response: %d %q", response.Code, response.Body.String())
	}
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

func TestDevelopmentCloneStartsAtPinnedCommitThenFindsUpdate(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	pinnedCommit := remote.head(t)
	remote.commitFile(t, "css/app.css", "body{color:white}", "first update")
	latestCommit := remote.commitFile(t, "js/app.js", "console.log('latest')", "second update")
	manager := testManager(t, remote.path, nil)
	manager.initialCommit = pinnedCommit

	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if manager.State().Commit != pinnedCommit {
		t.Fatalf("development clone did not checkout the pinned commit: %+v", manager.State())
	}
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

func TestFetchWhenCurrentRemainsReady(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	manager := testManager(t, remote.path, nil)
	if err := manager.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
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

func TestLegacyInstallIsReplacedByCloneOnFirstUpdate(t *testing.T) {
	remote := newLocalGameRepository(t, filepath.Join(t.TempDir(), "remote"))
	paths := makeDataPaths(t.TempDir())
	writeValidGame(t, paths.Source)
	writeLegacyManifest(t, paths, testCommit)
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.repositoryURL = remote.path
	manager.initialCommit = ""

	if err := manager.StartCheckForUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusUpdateAvailable)
	if !manager.State().UpdateAvailable {
		t.Fatalf("legacy install did not request conversion: %+v", manager.State())
	}
	if err := manager.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, StatusReady)
	if manager.State().Commit != remote.head(t) || manager.legacyInstall {
		t.Fatalf("legacy install was not converted: %+v", manager.State())
	}
	if _, err := git.PlainOpen(paths.Source); err != nil {
		t.Fatalf("converted install is not a Git working tree: %v", err)
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

func writeLegacyManifest(t *testing.T, paths dataPaths, commit string) {
	t.Helper()
	contents := []byte(`{"schemaVersion":1,"repository":"` + gameRepository + `","commit":"` + commit + `","installedAt":"2026-01-01T00:00:00Z"}`)
	path := legacyManifestPath(paths)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
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
