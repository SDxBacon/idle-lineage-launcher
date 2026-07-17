package main

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
)

// GameBrowser is a browser that the operating system has registered as able to
// open the game's local HTML entry point. ID is an opaque platform identifier;
// callers must not interpret it as an executable path or command.
type GameBrowser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GameLaunchResult describes a successful launch. FallbackToDefault is true
// only when the selected browser failed and the system default handler then
// accepted the game entry.
type GameLaunchResult struct {
	FallbackToDefault bool `json:"fallbackToDefault"`
}

type gameLauncherPlatform interface {
	GetAvailableBrowsers() ([]GameBrowser, error)
	LaunchDefault(entryPath string) error
	LaunchWithBrowser(entryPath, browserID string) error
}

// GameLauncher owns the platform-independent game launch policy. Platform
// adapters perform only discovery and operating-system handoff.
type GameLauncher struct {
	platform gameLauncherPlatform
	logger   *slog.Logger
}

func newGameLauncher() *GameLauncher {
	return &GameLauncher{
		platform: newGameLauncherPlatform(),
		logger:   slog.Default().With("component", "game_launcher"),
	}
}

func (launcher *GameLauncher) GetAvailableBrowsers() ([]GameBrowser, error) {
	platform, err := launcher.availablePlatform()
	if err != nil {
		return nil, err
	}
	browsers, err := platform.GetAvailableBrowsers()
	if err != nil {
		return nil, fmt.Errorf("get available game browsers: %w", err)
	}
	if len(browsers) == 0 {
		return []GameBrowser{}, nil
	}
	browsers = append([]GameBrowser(nil), browsers...)
	sort.SliceStable(browsers, func(i, j int) bool {
		if browsers[i].Name != browsers[j].Name {
			return browsers[i].Name < browsers[j].Name
		}
		return browsers[i].ID < browsers[j].ID
	})
	return browsers, nil
}

func (launcher *GameLauncher) Launch(entryPath string, browserID *string) (GameLaunchResult, error) {
	platform, err := launcher.availablePlatform()
	if err != nil {
		return GameLaunchResult{}, err
	}
	if browserID == nil {
		if err := platform.LaunchDefault(entryPath); err != nil {
			return GameLaunchResult{}, fmt.Errorf("open game entry with system default browser: %w", err)
		}
		return GameLaunchResult{}, nil
	}

	if err := platform.LaunchWithBrowser(entryPath, *browserID); err == nil {
		return GameLaunchResult{}, nil
	} else {
		customErr := fmt.Errorf("open game entry with selected browser %q: %w", *browserID, err)
		launcher.getLogger().Warn(
			"selected browser failed to open game; falling back to system default",
			"browser_id", *browserID,
			"error", err,
		)
		if fallbackErr := platform.LaunchDefault(entryPath); fallbackErr != nil {
			return GameLaunchResult{}, errors.Join(
				customErr,
				fmt.Errorf("open game entry with system default browser after selected browser failed: %w", fallbackErr),
			)
		}
	}

	return GameLaunchResult{FallbackToDefault: true}, nil
}

func (launcher *GameLauncher) availablePlatform() (gameLauncherPlatform, error) {
	if launcher == nil || launcher.platform == nil {
		return nil, errors.New("game launcher is unavailable")
	}
	return launcher.platform, nil
}

func (launcher *GameLauncher) getLogger() *slog.Logger {
	if launcher != nil && launcher.logger != nil {
		return launcher.logger
	}
	return slog.Default()
}
