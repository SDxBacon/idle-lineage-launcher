package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetLauncherInfoReturnsBuildVersionAndGameRepository(t *testing.T) {
	service := &LauncherService{version: "1.2.3"}

	info := service.GetLauncherInfo()

	if info.Version != "1.2.3" || info.GameRepository != gameRepository {
		t.Fatalf("unexpected launcher info: %+v", info)
	}
}

func TestRepositoryOpenersUseFixedGitHubPages(t *testing.T) {
	tests := []struct {
		name string
		want string
		open func(*LauncherService) error
	}{
		{name: "game", want: gameRepositoryPageURL, open: (*LauncherService).OpenGameRepository},
		{name: "launcher", want: launcherRepositoryPageURL, open: (*LauncherService).OpenLauncherRepository},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var opened string
			service := &LauncherService{openURL: func(url string) error {
				opened = url
				return nil
			}}

			if err := test.open(service); err != nil {
				t.Fatal(err)
			}
			if opened != test.want {
				t.Fatalf("unexpected URL: got %q, want %q", opened, test.want)
			}
		})
	}
}

func TestRepositoryOpenersRejectMissingSystemOpener(t *testing.T) {
	service := &LauncherService{}
	for _, open := range []func() error{service.OpenGameRepository, service.OpenLauncherRepository} {
		if err := open(); err == nil || err.Error() != "system URL opener is unavailable" {
			t.Fatalf("unexpected missing-opener error: %v", err)
		}
	}
}

func TestRepositoryOpenersReturnSystemOpenerErrors(t *testing.T) {
	want := errors.New("opener failed")
	service := &LauncherService{openURL: func(string) error { return want }}
	for _, open := range []func() error{service.OpenGameRepository, service.OpenLauncherRepository} {
		if err := open(); !errors.Is(err, want) {
			t.Fatalf("unexpected opener error: %v", err)
		}
	}
}

func TestGetGameBrowsersDelegatesToGameLauncher(t *testing.T) {
	service := &LauncherService{gameLauncher: testGameLauncher(&stubGameLauncherPlatform{
		browsers: []GameBrowser{
			{ID: "firefox", Name: "Firefox"},
			{ID: "chrome", Name: "Chrome"},
		},
	})}

	browsers, err := service.GetGameBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	want := []GameBrowser{{ID: "chrome", Name: "Chrome"}, {ID: "firefox", Name: "Firefox"}}
	if len(browsers) != len(want) || browsers[0] != want[0] || browsers[1] != want[1] {
		t.Fatalf("unexpected game browsers: got %+v, want %+v", browsers, want)
	}
}

func TestGetGameBrowsersRejectsMissingGameLauncher(t *testing.T) {
	var nilService *LauncherService
	for _, service := range []*LauncherService{nilService, &LauncherService{}} {
		if _, err := service.GetGameBrowsers(); err == nil {
			t.Fatal("expected missing game launcher to fail")
		}
	}
}

func TestLaunchGameOpensAbsoluteInstalledEntry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "game with spaces")
	writeGameEntry(t, root)
	manager := launchableManager(root, StatusReady)
	var opened string
	service := &LauncherService{
		manager: manager,
		gameLauncher: testGameLauncherWithDefault(func(path string) error {
			opened = path
			return nil
		}),
	}

	if err := launchGameError(service, nil); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if opened != want {
		t.Fatalf("unexpected opened path: got %q, want %q", opened, want)
	}
}

func TestLaunchGameAllowsInstalledNonUpdatingStates(t *testing.T) {
	root := t.TempDir()
	writeGameEntry(t, root)
	for _, status := range []GameStatus{StatusReady, StatusChecking, StatusUpdateAvailable} {
		t.Run(string(status), func(t *testing.T) {
			called := false
			service := &LauncherService{
				manager: launchableManager(root, status),
				gameLauncher: testGameLauncherWithDefault(func(string) error {
					called = true
					return nil
				}),
			}
			if err := launchGameError(service, nil); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("file opener was not called")
			}
		})
	}
}

func TestLaunchGameRejectsUnavailableStates(t *testing.T) {
	root := t.TempDir()
	writeGameEntry(t, root)
	statuses := []GameStatus{
		StatusMissing,
		StatusResolving,
		StatusInstalling,
		StatusUpdating,
		StatusCancelled,
		StatusError,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			called := false
			service := &LauncherService{
				manager: launchableManager(root, status),
				gameLauncher: testGameLauncherWithDefault(func(string) error {
					called = true
					return nil
				}),
			}
			if err := launchGameError(service, nil); err == nil {
				t.Fatal("expected launch to be rejected")
			}
			if called {
				t.Fatal("file opener was called for a rejected state")
			}
		})
	}
}

func TestLaunchGameRejectsReadyStateWithoutActiveInstall(t *testing.T) {
	service := &LauncherService{
		manager: &gameManager{state: GameState{Status: StatusReady}},
		gameLauncher: testGameLauncherWithDefault(func(string) error {
			t.Fatal("file opener must not be called")
			return nil
		}),
	}
	if err := launchGameError(service, nil); err == nil {
		t.Fatal("expected missing active install to be rejected")
	}
}

func TestLaunchGameReconcilesDeletedActiveInstall(t *testing.T) {
	root := t.TempDir()
	writeGameEntry(t, root)
	manager := launchableManager(root, StatusReady)
	called := false
	service := &LauncherService{
		manager: manager,
		gameLauncher: testGameLauncherWithDefault(func(string) error {
			called = true
			return nil
		}),
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	if err := launchGameError(service, nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("file opener was called for a deleted installation")
	}
	if state := manager.State(); state.Status != StatusMissing || state.Error != "" {
		t.Fatalf("deleted installation was not reconciled: %+v", state)
	}
	if root, commit, installed := manager.ActiveVersion(); installed || root != "" || commit != "" {
		t.Fatalf("deleted installation remained active: root=%q commit=%q installed=%v", root, commit, installed)
	}
}

func TestLaunchGameRevalidatesEntry(t *testing.T) {
	root := t.TempDir()
	service := &LauncherService{
		manager: launchableManager(root, StatusReady),
		gameLauncher: testGameLauncherWithDefault(func(string) error {
			t.Fatal("file opener must not be called")
			return nil
		}),
	}
	if err := launchGameError(service, nil); err == nil {
		t.Fatal("expected missing index.html to be rejected")
	}

	if err := os.Mkdir(filepath.Join(root, "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := launchGameError(service, nil); err == nil {
		t.Fatal("expected a non-regular index.html to be rejected")
	}
}

func TestLaunchGameRejectsEntrySymlinkOutsideRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "game")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.html")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "index.html")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	service := &LauncherService{
		manager: launchableManager(root, StatusReady),
		gameLauncher: testGameLauncherWithDefault(func(string) error {
			t.Fatal("file opener must not be called")
			return nil
		}),
	}
	if err := launchGameError(service, nil); err == nil {
		t.Fatal("expected escaping symlink to be rejected")
	}
}

func TestLaunchGameAllowsEntrySymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	realEntry := filepath.Join(root, "game.html")
	if err := os.WriteFile(realEntry, []byte("game"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realEntry, filepath.Join(root, "index.html")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	called := false
	service := &LauncherService{
		manager: launchableManager(root, StatusReady),
		gameLauncher: testGameLauncherWithDefault(func(string) error {
			called = true
			return nil
		}),
	}
	if err := launchGameError(service, nil); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("file opener was not called")
	}
}

func TestLaunchGameReturnsDefaultBrowserLaunchError(t *testing.T) {
	root := t.TempDir()
	writeGameEntry(t, root)
	want := errors.New("opener failed")
	service := &LauncherService{
		manager: launchableManager(root, StatusReady),
		gameLauncher: testGameLauncherWithDefault(func(string) error {
			return want
		}),
	}
	if err := launchGameError(service, nil); !errors.Is(err, want) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLaunchGamePassesSelectedBrowserAndReturnsFallbackResult(t *testing.T) {
	root := t.TempDir()
	writeGameEntry(t, root)
	platform := &stubGameLauncherPlatform{customErr: errors.New("browser was removed")}
	service := &LauncherService{
		manager:      launchableManager(root, StatusReady),
		gameLauncher: testGameLauncher(platform),
	}
	browserID := "opaque-browser-id"

	result, err := service.LaunchGame(&browserID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.FallbackToDefault {
		t.Fatal("expected LauncherService to propagate successful fallback")
	}
	if len(platform.customCalls) != 1 || platform.customCalls[0].browserID != browserID {
		t.Fatalf("selected browser was not forwarded: %#v", platform.customCalls)
	}
	if len(platform.defaultEntries) != 1 {
		t.Fatalf("expected one default fallback, got %#v", platform.defaultEntries)
	}
}

func TestLaunchGameRejectsMissingGameLauncher(t *testing.T) {
	root := t.TempDir()
	writeGameEntry(t, root)
	service := &LauncherService{manager: launchableManager(root, StatusReady)}

	if err := launchGameError(service, nil); err == nil || err.Error() != "game launcher is unavailable" {
		t.Fatalf("unexpected missing game launcher error: %v", err)
	}
}

func TestOpenGameFolderOpensActiveInstallInEveryInstalledState(t *testing.T) {
	for _, status := range []GameStatus{StatusReady, StatusChecking, StatusUpdateAvailable, StatusUpdating} {
		t.Run(string(status), func(t *testing.T) {
			root := t.TempDir()
			var opened string
			var selectFile bool
			service := &LauncherService{
				manager: launchableManager(root, status),
				openFolder: func(path string, selectPath bool) error {
					opened = path
					selectFile = selectPath
					return nil
				},
			}

			if err := service.OpenGameFolder(); err != nil {
				t.Fatal(err)
			}
			if opened != root || selectFile {
				t.Fatalf("unexpected folder open request: path=%q selectFile=%v", opened, selectFile)
			}
		})
	}
}

func TestOpenGameFolderRejectsMissingActiveInstall(t *testing.T) {
	service := &LauncherService{
		manager: &gameManager{state: GameState{Status: StatusMissing}},
		openFolder: func(string, bool) error {
			t.Fatal("folder opener must not be called")
			return nil
		},
	}
	if err := service.OpenGameFolder(); err == nil {
		t.Fatal("expected missing install to be rejected")
	}
}

func TestOpenGameFolderReconcilesDeletedActiveInstall(t *testing.T) {
	root := t.TempDir()
	manager := launchableManager(root, StatusReady)
	called := false
	service := &LauncherService{
		manager: manager,
		openFolder: func(string, bool) error {
			called = true
			return nil
		},
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	if err := service.OpenGameFolder(); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("folder opener was called for a deleted installation")
	}
	if state := manager.State(); state.Status != StatusMissing || state.Error != "" {
		t.Fatalf("deleted installation was not reconciled: %+v", state)
	}
}

func TestOpenGameFolderReturnsFolderOpenerError(t *testing.T) {
	want := errors.New("folder opener failed")
	service := &LauncherService{
		manager: launchableManager(t.TempDir(), StatusReady),
		openFolder: func(string, bool) error {
			return want
		},
	}
	if err := service.OpenGameFolder(); !errors.Is(err, want) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLaunchGameAndUpdateHaveDefinedOrdering(t *testing.T) {
	root := t.TempDir()
	writeGameEntry(t, root)
	manager := launchableManager(root, StatusUpdateAvailable)
	openerEntered := make(chan struct{})
	releaseOpener := make(chan struct{})
	service := &LauncherService{
		manager: manager,
		gameLauncher: testGameLauncherWithDefault(func(string) error {
			close(openerEntered)
			<-releaseOpener
			return nil
		}),
	}

	launchDone := make(chan error, 1)
	go func() {
		launchDone <- launchGameError(service, nil)
	}()
	<-openerEntered

	updateAttempted := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		close(updateAttempted)
		updateDone <- manager.StartUpdate()
	}()
	<-updateAttempted
	select {
	case err := <-updateDone:
		t.Fatalf("update began before the system opener returned: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseOpener)
	if err := <-launchDone; err != nil {
		t.Fatal(err)
	}
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	manager.Shutdown()
}

func launchableManager(root string, status GameStatus) *gameManager {
	return &gameManager{
		activeRoot: root,
		paths:      dataPaths{Source: root},
		logger:     slog.Default(),
		state:      GameState{Status: status, Commit: testCommitHash},
	}
}

func launchGameError(service *LauncherService, browserID *string) error {
	_, err := service.LaunchGame(browserID)
	return err
}

func writeGameEntry(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("game"), 0o644); err != nil {
		t.Fatal(err)
	}
}

const testCommitHash = "0123456789abcdef0123456789abcdef01234567"
