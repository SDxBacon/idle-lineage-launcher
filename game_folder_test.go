package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMissingGameInitializationDoesNotCreateGameDirectories(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.State().Status != StatusMissing {
		t.Fatalf("unexpected state: %+v", manager.State())
	}
	if _, err := os.Stat(paths.Game); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing game initialization created game data: %v", err)
	}
}

func TestUnavailableGameRootHasDedicatedState(t *testing.T) {
	appRoot := t.TempDir()
	missingRoot := filepath.Join(t.TempDir(), "unmounted")
	manager, err := newGameManager(makeDataPathsForGameRoot(appRoot, missingRoot), nil)
	if err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.Status != StatusStorageUnavailable || state.Error == "" {
		t.Fatalf("unavailable root was not reported: %+v", state)
	}
}

func TestUninstalledFolderChangeAppliesImmediately(t *testing.T) {
	coordinator, manager, _ := newTestGameFolderCoordinator(t, false, false)
	candidate := t.TempDir()

	result, err := coordinator.RequestChange(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.RequiresMove || result.Cancelled {
		t.Fatalf("unexpected change result: %+v", result)
	}
	if coordinator.Info().Root != result.Root || manager.paths.GameRoot != result.Root {
		t.Fatalf("new root was not applied: info=%+v paths=%+v", coordinator.Info(), manager.paths)
	}
	if manager.State().Status != StatusMissing {
		t.Fatalf("uninstalled change altered game state: %+v", manager.State())
	}
}

func TestUninstalledFolderChangeLoadsValidExistingGame(t *testing.T) {
	coordinator, manager, _ := newTestGameFolderCoordinator(t, false, false)
	candidate := t.TempDir()
	newLocalGameRepository(t, makeDataPathsForGameRoot(manager.paths.Root, candidate).Source)

	result, err := coordinator.RequestChange(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || manager.State().Status != StatusReady {
		t.Fatalf("existing game was not activated: result=%+v state=%+v", result, manager.State())
	}
}

func TestUninstalledFolderChangeRejectsUnknownAndSymlinkedSources(t *testing.T) {
	t.Run("unknown source", func(t *testing.T) {
		coordinator, manager, _ := newTestGameFolderCoordinator(t, false, false)
		candidate := t.TempDir()
		source := makeDataPathsForGameRoot(manager.paths.Root, candidate).Source
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.RequestChange(candidate); err == nil || !strings.Contains(err.Error(), "無法辨識") {
			t.Fatalf("unknown source was accepted: %v", err)
		}
	})

	t.Run("symlinked source", func(t *testing.T) {
		coordinator, manager, _ := newTestGameFolderCoordinator(t, false, false)
		candidate := t.TempDir()
		paths := makeDataPathsForGameRoot(manager.paths.Root, candidate)
		outside := filepath.Join(t.TempDir(), "outside-game")
		newLocalGameRepository(t, outside)
		if err := os.MkdirAll(paths.Game, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, paths.Source); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := coordinator.RequestChange(candidate); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("symlinked source was accepted: %v", err)
		}
	})
}

func TestFolderChangeRejectsSameLocationThroughSymlink(t *testing.T) {
	coordinator, _, _ := newTestGameFolderCoordinator(t, false, false)
	alias := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(coordinator.Info().Root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := coordinator.RequestChange(alias); err == nil || !strings.Contains(err.Error(), "相同") {
		t.Fatalf("same location through symlink was accepted: %v", err)
	}
}

func TestRestoreDefaultIsNoOpWhenAlreadyUsingDefault(t *testing.T) {
	coordinator, manager, _ := newTestGameFolderCoordinator(t, false, false)
	service := &LauncherService{manager: manager, folders: coordinator}
	result, err := service.RestoreDefaultGameFolder()
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.RequiresMove || result.Cancelled {
		t.Fatalf("default restore was not a no-op: %+v", result)
	}
}

func TestGameFolderValidationCanonicalizesAliasesAndRejectsUnsafeLayouts(t *testing.T) {
	t.Run("canonical alias", func(t *testing.T) {
		realRoot := t.TempDir()
		alias := filepath.Join(t.TempDir(), "root-alias")
		if err := os.Symlink(realRoot, alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		root, err := validateGameFolderRoot(alias)
		if err != nil {
			t.Fatal(err)
		}
		if !sameCleanPath(root, realRoot) || root == alias {
			t.Fatalf("alias was not canonicalized: got %q, real %q", root, realRoot)
		}
	})

	t.Run("game occupied by file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "game"), []byte("occupied"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := validateGameFolderRoot(root); err == nil || !strings.Contains(err.Error(), "不是資料夾") {
			t.Fatalf("game file was accepted: %v", err)
		}
	})

	t.Run("source containment", func(t *testing.T) {
		oldSource := filepath.Join(t.TempDir(), "game", "shines871")
		newSource := filepath.Join(oldSource, "nested", "game", "shines871")
		if err := validateGameFolderRelationship(oldSource, newSource); err == nil || !strings.Contains(err.Error(), "包含") {
			t.Fatalf("contained source was accepted: %v", err)
		}
	})
}

func TestInstalledFolderChangeRequiresConfirmationAndRejectsDestination(t *testing.T) {
	coordinator, manager, _ := newTestGameFolderCoordinator(t, true, false)
	oldSource := manager.paths.Source
	candidate := t.TempDir()

	result, err := coordinator.RequestChange(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || !result.RequiresMove {
		t.Fatalf("installed change did not require a move: %+v", result)
	}
	if sameCleanPath(coordinator.Info().Root, candidate) {
		t.Fatal("candidate was applied before confirmation")
	}
	if _, err := os.Stat(oldSource); err != nil {
		t.Fatalf("old source changed before confirmation: %v", err)
	}

	conflict := t.TempDir()
	if err := os.MkdirAll(makeDataPathsForGameRoot(manager.paths.Root, conflict).Source, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RequestChange(conflict); err == nil || !strings.Contains(err.Error(), "已存在遊戲") {
		t.Fatalf("destination conflict was accepted: %v", err)
	}
}

func TestConfirmedFolderMoveMovesInstalledRepositoryAndPersistsRoot(t *testing.T) {
	coordinator, manager, store := newTestGameFolderCoordinator(t, true, false)
	oldSource := manager.paths.Source
	candidate := t.TempDir()

	if _, err := coordinator.RequestChange(candidate); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ConfirmMove(candidate); err != nil {
		t.Fatal(err)
	}
	newSource := manager.paths.Source
	if _, err := os.Stat(oldSource); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old source remains after move: %v", err)
	}
	if _, _, err := inspectGameSource(newSource, manager.logger); err != nil {
		t.Fatalf("moved game is invalid: %v", err)
	}
	if manager.paths.Source != newSource || manager.State().Status != StatusReady {
		t.Fatalf("manager did not activate moved game: paths=%+v state=%+v", manager.paths, manager.State())
	}
	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !sameCleanPath(settings.GameRoot, candidate) || !settings.LastKnownInstalled || settings.PendingMove != nil {
		t.Fatalf("moved root was not persisted: %+v", settings)
	}
}

func TestConfirmedFolderMoveFallsBackToCopyAcrossDevices(t *testing.T) {
	coordinator, manager, _ := newTestGameFolderCoordinator(t, true, false)
	oldSource := manager.paths.Source
	candidate := t.TempDir()
	coordinator.stageSource = func(string, string) error { return syscall.EXDEV }

	result, err := coordinator.RequestChange(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RequiresMove {
		t.Fatalf("installed game did not request a move: %+v", result)
	}
	if err := coordinator.ConfirmMove(candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(oldSource); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-device move retained the old source: %v", err)
	}
	if _, _, err := inspectGameSource(manager.paths.Source, manager.logger); err != nil {
		t.Fatalf("cross-device destination is invalid: %v", err)
	}
}

func TestFolderCoordinatorShutdownWaitsForMove(t *testing.T) {
	coordinator, _, _ := newTestGameFolderCoordinator(t, true, false)
	candidate := t.TempDir()
	if _, err := coordinator.RequestChange(candidate); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	coordinator.stageSource = func(source, destination string) error {
		close(entered)
		<-release
		return os.Rename(source, destination)
	}
	moveDone := make(chan error, 1)
	go func() { moveDone <- coordinator.ConfirmMove(candidate) }()
	<-entered

	shutdownDone := make(chan struct{})
	go func() {
		coordinator.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
		t.Fatal("shutdown returned before the move completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-moveDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not return after the move completed")
	}
	if err := coordinator.ConfirmMove(t.TempDir()); err == nil || !strings.Contains(err.Error(), "正在關閉") {
		t.Fatalf("move was accepted after shutdown: %v", err)
	}
}

func TestRestoreDefaultAppliesOrMovesUsingNormalRules(t *testing.T) {
	t.Run("uninstalled", func(t *testing.T) {
		coordinator, manager, _ := newTestGameFolderCoordinator(t, false, true)
		result, err := coordinator.RequestChange(coordinator.defaultRoot)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Applied || !coordinator.Info().IsDefault || manager.State().Status != StatusMissing {
			t.Fatalf("default root was not applied: result=%+v info=%+v state=%+v", result, coordinator.Info(), manager.State())
		}
	})

	t.Run("uninstalled existing valid game", func(t *testing.T) {
		coordinator, manager, _ := newTestGameFolderCoordinator(t, false, true)
		newLocalGameRepository(t, makeDataPaths(coordinator.defaultRoot).Source)
		result, err := coordinator.RequestChange(coordinator.defaultRoot)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Applied || manager.State().Status != StatusReady {
			t.Fatalf("valid default game was not activated: result=%+v state=%+v", result, manager.State())
		}
	})

	t.Run("installed move", func(t *testing.T) {
		coordinator, manager, _ := newTestGameFolderCoordinator(t, true, true)
		result, err := coordinator.RequestChange(coordinator.defaultRoot)
		if err != nil {
			t.Fatal(err)
		}
		if !result.RequiresMove {
			t.Fatalf("restore did not request a move: %+v", result)
		}
		if err := coordinator.ConfirmMove(coordinator.defaultRoot); err != nil {
			t.Fatal(err)
		}
		if !coordinator.Info().IsDefault || !sameCleanPath(manager.paths.GameRoot, coordinator.defaultRoot) {
			t.Fatalf("installed game was not restored to default: info=%+v paths=%+v", coordinator.Info(), manager.paths)
		}
	})
}

func TestRestoreDefaultRejectsExistingDestinationForInstalledGame(t *testing.T) {
	coordinator, manager, _ := newTestGameFolderCoordinator(t, true, true)
	newLocalGameRepository(t, makeDataPaths(coordinator.defaultRoot).Source)

	_, err := coordinator.RequestChange(coordinator.defaultRoot)
	if err == nil || !strings.Contains(err.Error(), "預設位置已有遊戲") {
		t.Fatalf("default destination conflict was accepted: %v", err)
	}
	if manager.State().Status != StatusReady {
		t.Fatalf("default conflict changed current game: %+v", manager.State())
	}
}

func TestPreparedMoveRecoveryRestoresOriginalSource(t *testing.T) {
	appRoot := t.TempDir()
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	oldPaths := makeDataPathsForGameRoot(appRoot, oldRoot)
	newPaths := makeDataPathsForGameRoot(appRoot, newRoot)
	newLocalGameRepository(t, oldPaths.Source)
	if err := os.MkdirAll(newPaths.Staging, 0o755); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(newPaths.Staging, movingGameDirectory)
	if err := os.Rename(oldPaths.Source, temporary); err != nil {
		t.Fatal(err)
	}
	store := newLauncherSettingsStore(makeDataPaths(appRoot))
	settings := launcherSettings{
		Version:            launcherSettingsVersion,
		GameRoot:           oldRoot,
		LastKnownInstalled: true,
		PendingMove:        &pendingGameMove{FromRoot: oldRoot, ToRoot: newRoot, Phase: "prepared"},
	}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}

	recovered, err := recoverPendingGameMove(store, settings, appRoot)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.GameRoot != oldRoot || recovered.PendingMove != nil {
		t.Fatalf("move record was not rolled back: %+v", recovered)
	}
	if _, _, err := inspectGameSource(oldPaths.Source, slog.Default()); err != nil {
		t.Fatalf("original game was not restored: %v", err)
	}
}

func TestCommittedMoveRecoveryKeepsNewSourceAndCleansOldSource(t *testing.T) {
	appRoot := t.TempDir()
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	oldPaths := makeDataPathsForGameRoot(appRoot, oldRoot)
	newPaths := makeDataPathsForGameRoot(appRoot, newRoot)
	repository := newLocalGameRepository(t, oldPaths.Source)
	if err := copyGameTree(oldPaths.Source, newPaths.Source, func(string, string, int) {}); err != nil {
		t.Fatal(err)
	}
	store := newLauncherSettingsStore(makeDataPaths(appRoot))
	settings := launcherSettings{
		Version:            launcherSettingsVersion,
		GameRoot:           newRoot,
		LastKnownInstalled: true,
		PendingMove: &pendingGameMove{
			FromRoot: oldRoot,
			ToRoot:   newRoot,
			Phase:    "committed",
			Commit:   repository.head(t),
		},
	}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}

	recovered, err := recoverPendingGameMove(store, settings, appRoot)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.PendingMove != nil || recovered.GameRoot != newRoot || !recovered.LastKnownInstalled {
		t.Fatalf("committed move was not finalized: %+v", recovered)
	}
	if _, err := os.Lstat(oldPaths.Source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old source was not cleaned: %v", err)
	}
	if _, _, err := inspectGameSource(newPaths.Source, slog.Default()); err != nil {
		t.Fatalf("new source was not retained: %v", err)
	}
}

func TestMoveRecoveryKeepsRecordWhileAStorageRootIsUnavailable(t *testing.T) {
	appRoot := t.TempDir()
	oldRoot := filepath.Join(t.TempDir(), "missing-old-root")
	newRoot := t.TempDir()
	store := newLauncherSettingsStore(makeDataPaths(appRoot))
	settings := launcherSettings{
		Version:            launcherSettingsVersion,
		GameRoot:           oldRoot,
		LastKnownInstalled: true,
		PendingMove:        &pendingGameMove{FromRoot: oldRoot, ToRoot: newRoot, Phase: "prepared"},
	}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}

	recovered, err := recoverPendingGameMove(store, settings, appRoot)
	if err == nil || recovered.PendingMove == nil {
		t.Fatalf("unavailable storage unexpectedly cleared recovery: settings=%+v err=%v", recovered, err)
	}
	persisted, loadErr := store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.PendingMove == nil || persisted.GameRoot != oldRoot {
		t.Fatalf("recovery record was not preserved: %+v", persisted)
	}
}

func TestCopyGameTreePreservesFilesAndSymlinks(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "game.dat"), []byte("game-data"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("nested", "game.dat"), filepath.Join(source, "game-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var lastPercent int
	if err := copyGameTree(source, destination, func(_, _ string, percent int) {
		lastPercent = percent
	}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "nested", "game.dat"))
	if err != nil || string(contents) != "game-data" {
		t.Fatalf("regular file was not copied: %q (%v)", contents, err)
	}
	link, err := os.Readlink(filepath.Join(destination, "game-link"))
	if err != nil || link != filepath.Join("nested", "game.dat") {
		t.Fatalf("symlink was not preserved: %q (%v)", link, err)
	}
	if lastPercent != 100 {
		t.Fatalf("copy progress did not finish at 100: %d", lastPercent)
	}
}

func newTestGameFolderCoordinator(t *testing.T, installed, customRoot bool) (*gameFolderCoordinator, *gameManager, *launcherSettingsStore) {
	t.Helper()
	appRoot := t.TempDir()
	gameRoot := appRoot
	if customRoot {
		gameRoot = t.TempDir()
	}
	paths := makeDataPathsForGameRoot(appRoot, gameRoot)
	if installed {
		newLocalGameRepository(t, paths.Source)
	}
	store := newLauncherSettingsStore(makeDataPaths(appRoot))
	settings := launcherSettings{Version: launcherSettingsVersion, GameRoot: gameRoot, LastKnownInstalled: installed}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	manager, err := newGameManager(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	return newGameFolderCoordinator(manager, store, settings, appRoot), manager, store
}
