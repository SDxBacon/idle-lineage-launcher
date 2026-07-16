package main

import (
	"embed"
	"log"
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func init() {
	application.RegisterEvent[GameState]("launcher:game-state")
	application.RegisterEvent[struct{}]("launcher:reload-game")
}

func main() {
	slog.Info("launcher backend starting")
	paths, err := resolveDataPaths()
	if err != nil {
		log.Fatal(err)
	}
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
	service := &LauncherService{manager: manager, windows: windows}
	app = application.New(application.Options{
		Name:        "Idle Lineage Launcher",
		Description: "Desktop launcher for Idle Lineage Class",
		Icon:        appIcon,
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler:    application.AssetFileServerFS(assets),
			Middleware: gameAssetMiddleware(manager),
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
				windows.Create().Focus()
			},
		},
		OnShutdown: func() {
			slog.Info("launcher shutdown callback started")
			manager.Shutdown()
			slog.Info("launcher shutdown callback completed")
		},
	})
	windows.app = app
	windows.Create()
	if _, _, installed := manager.ActiveVersion(); installed {
		slog.Info("installed game detected; scheduling startup update check")
		_ = manager.StartCheckForUpdate()
	}

	slog.Info("starting Wails application loop")
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
	slog.Info("Wails application loop exited")
}
