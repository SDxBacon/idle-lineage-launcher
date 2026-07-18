package main

import (
	"embed"
	"log"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

// version is injected from Taskfile.yml at build time with -ldflags.
var version = ""

func init() {
	application.RegisterEvent[GameState]("launcher:game-state")
	application.RegisterEvent[CloseGuardEvent](closeGuardEventName)
}

func main() {
	slog.Info("launcher backend starting")
	defaultPaths, err := resolveDataPaths()
	if err != nil {
		log.Fatal(err)
	}
	settingsStore := newLauncherSettingsStore(defaultPaths)
	settings, err := settingsStore.Load()
	if err != nil {
		slog.Error("launcher settings could not be loaded; using defaults", "error", err)
		settings = settingsStore.defaults()
	}
	settings, err = recoverPendingGameMove(settingsStore, settings, defaultPaths.Root)
	if err != nil {
		slog.Error("pending game move recovery failed", "error", err)
	}
	if normalizedRoot, normalizeErr := validateGameFolderRoot(settings.GameRoot); normalizeErr == nil && !sameCleanPath(normalizedRoot, settings.GameRoot) {
		settings.GameRoot = normalizedRoot
		if saveErr := settingsStore.Save(settings); saveErr != nil {
			slog.Error("normalized game root could not be persisted", "error", saveErr)
		}
	}
	paths := makeDataPathsForGameRoot(defaultPaths.Root, settings.GameRoot)
	slog.Info("application data paths resolved", "root", paths.Root, "game", paths.Game, "webview", paths.WebView)

	var app *application.App
	manager, err := newGameManager(paths, func(state GameState) {
		if app != nil {
			app.Event.Emit("launcher:game-state", state)
		}
	})
	if err != nil {
		log.Fatal(err)
	}

	windows := &windowFactory{}
	folders := newGameFolderCoordinator(manager, settingsStore, settings, defaultPaths.Root)
	service := &LauncherService{manager: manager, folders: folders, version: version, gameLauncher: newGameLauncher()}
	closeGuard := newUpdateCloseCoordinator(manager, func(event CloseGuardEvent) {
		if app != nil {
			app.Event.Emit(closeGuardEventName, event)
		}
	}, func() {
		if app != nil {
			app.Quit()
		}
	})
	windows.closeGuard = closeGuard
	service.closeGuard = closeGuard
	app = application.New(application.Options{
		Name:        "Idle Lineage Launcher",
		Description: "Desktop launcher for Idle Lineage Class",
		Icon:        appIcon,
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath: paths.WebView,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.sdxbacon.idle-lineage-launcher",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				windows.ShowAndFocus()
			},
		},
		OnShutdown: func() {
			slog.Info("launcher shutdown callback started")
			folders.Shutdown()
			manager.Shutdown()
			slog.Info("launcher shutdown callback completed")
		},
		ShouldQuit: closeGuard.HandleCloseRequest,
	})
	service.openURL = app.Browser.OpenURL
	service.openFolder = app.Env.OpenFileManager
	service.selectFolder = func(current string) (string, error) {
		directory := current
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			directory = defaultPaths.Root
		}
		dialog := app.Dialog.OpenFile().
			CanChooseDirectories(true).
			CanChooseFiles(false).
			CanCreateDirectories(true).
			ResolvesAliases(true).
			SetDirectory(directory).
			SetTitle("選擇遊戲資料夾").
			SetButtonText("選擇")
		if window, exists := app.Window.GetByName("launcher-window"); exists {
			dialog.AttachToWindow(window)
		}
		return dialog.PromptForSingleSelection()
	}
	windows.app = app
	windows.Create()
	if !manager.StartPendingUpdateRecovery() {
		startStartupUpdateCheck(manager)
	}

	slog.Info("starting Wails application loop")
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
	slog.Info("Wails application loop exited")
}

func startStartupUpdateCheck(manager *gameManager) {
	if _, _, installed := manager.ActiveVersion(); !installed {
		return
	}
	slog.Info("installed game detected; scheduling startup update check")
	_ = manager.StartCheckForUpdate()
}
