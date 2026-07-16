package main

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type windowFactory struct {
	mu      sync.Mutex
	app     *application.App
	counter uint64
}

func (factory *windowFactory) Create() application.Window {
	factory.mu.Lock()
	factory.counter++
	number := factory.counter
	factory.mu.Unlock()
	slog.Info("creating game window", "window_number", number)

	window := factory.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            fmt.Sprintf("game-window-%d", number),
		Title:           "Idle Lineage Launcher",
		Width:           1600,
		Height:          900,
		MinWidth:        1024,
		MinHeight:       720,
		URL:             "/",
		InitialPosition: application.WindowCentered,
		BackgroundColour: application.NewRGB(
			8, 12, 18,
		),
		DefaultContextMenuDisabled: true,
		KeyBindings: map[string]func(application.Window){
			"CmdOrCtrl+N": func(application.Window) {
				factory.Create()
			},
			"CmdOrCtrl+R": func(window application.Window) {
				window.EmitEvent("launcher:reload-game")
			},
			"F11": func(window application.Window) {
				window.ToggleFullscreen()
			},
		},
		OpenInspectorOnStartup: true,
	})
	slog.Info("game window created", "window_number", number)
	return window
}
