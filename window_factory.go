package main

import (
	"log/slog"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type windowFactory struct {
	mu  sync.Mutex
	app *application.App
}

func (factory *windowFactory) Create() application.Window {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if window, exists := factory.app.Window.GetByName("launcher-window"); exists {
		return window
	}
	slog.Info("creating launcher window")

	window := factory.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "launcher-window",
		Title:           "Idle Lineage Launcher",
		Width:           900,
		Height:          720,
		MinWidth:        720,
		MinHeight:       600,
		URL:             "/",
		InitialPosition: application.WindowCentered,
		BackgroundColour: application.NewRGB(
			8, 12, 18,
		),
		DefaultContextMenuDisabled: true,
	})
	slog.Info("launcher window created")
	return window
}

func (factory *windowFactory) ShowAndFocus() {
	window := factory.Create()
	window.Show()
	window.Restore()
	window.Focus()
}
