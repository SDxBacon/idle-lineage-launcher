package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type urlOpener func(string) error
type folderOpener func(string, bool) error
type folderSelector func(string) (string, error)

const launcherRepositoryPageURL = "https://github.com/SDxBacon/idle-lineage-launcher"

type LauncherInfo struct {
	Version        string `json:"version"`
	GameRepository string `json:"gameRepository"`
}

type LauncherService struct {
	manager      *gameManager
	version      string
	gameLauncher *GameLauncher
	folders      *gameFolderCoordinator
	openURL      urlOpener
	openFolder   folderOpener
	selectFolder folderSelector
}

func (service *LauncherService) GetGameFolderInfo() GameFolderInfo {
	slog.Info("backend service call", "method", "GetGameFolderInfo")
	if service == nil || service.folders == nil {
		return GameFolderInfo{}
	}
	return service.folders.Info()
}

func (service *LauncherService) SelectGameFolder() (GameFolderChangeResult, error) {
	slog.Info("backend service call", "method", "SelectGameFolder")
	if service == nil || service.folders == nil || service.selectFolder == nil {
		return GameFolderChangeResult{}, errors.New("game folder selector is unavailable")
	}
	selection, err := service.selectFolder(service.folders.Info().Root)
	if err != nil {
		return GameFolderChangeResult{}, fmt.Errorf("open game folder selector: %w", err)
	}
	if selection == "" {
		return GameFolderChangeResult{Cancelled: true}, nil
	}
	return service.folders.RequestChange(selection)
}

func (service *LauncherService) RestoreDefaultGameFolder() (GameFolderChangeResult, error) {
	slog.Info("backend service call", "method", "RestoreDefaultGameFolder")
	if service == nil || service.folders == nil {
		return GameFolderChangeResult{}, errors.New("game folder settings are unavailable")
	}
	info := service.folders.Info()
	if info.IsDefault {
		return GameFolderChangeResult{}, nil
	}
	return service.folders.RequestChange(info.DefaultRoot)
}

func (service *LauncherService) ConfirmGameFolderMove(root string) error {
	slog.Info("backend service call", "method", "ConfirmGameFolderMove", "root", root)
	if service == nil || service.folders == nil {
		return errors.New("game folder settings are unavailable")
	}
	return service.folders.ConfirmMove(root)
}

func (service *LauncherService) RecheckGameFolder() error {
	slog.Info("backend service call", "method", "RecheckGameFolder")
	if service == nil || service.folders == nil || service.manager == nil {
		return errors.New("game folder settings are unavailable")
	}
	if err := service.folders.Recheck(); err != nil {
		return err
	}
	if _, _, installed := service.manager.ActiveVersion(); installed {
		return service.manager.StartCheckForUpdate()
	}
	return nil
}

func (service *LauncherService) GetLauncherInfo() LauncherInfo {
	slog.Info("backend service call", "method", "GetLauncherInfo")
	info := LauncherInfo{GameRepository: gameRepository}
	if service != nil {
		info.Version = service.version
	}
	return info
}

func (service *LauncherService) GetGameState() GameState {
	slog.Info("backend service call", "method", "GetGameState")
	return service.manager.State()
}

func (service *LauncherService) StartInstall() error {
	slog.Info("backend service call", "method", "StartInstall")
	return service.manager.StartInstall()
}

func (service *LauncherService) CheckForUpdate() error {
	slog.Info("backend service call", "method", "CheckForUpdate")
	return service.manager.StartCheckForUpdate()
}

func (service *LauncherService) StartUpdate() error {
	slog.Info("backend service call", "method", "StartUpdate")
	return service.manager.StartUpdate()
}

func (service *LauncherService) CancelInstall() {
	slog.Info("backend service call", "method", "CancelInstall")
	service.manager.CancelInstall()
}

func (service *LauncherService) GetGameBrowsers() ([]GameBrowser, error) {
	slog.Info("backend service call", "method", "GetGameBrowsers")
	if service == nil || service.gameLauncher == nil {
		return nil, errors.New("game launcher is unavailable")
	}
	return service.gameLauncher.GetAvailableBrowsers()
}

func (service *LauncherService) LaunchGame(browserID *string) (GameLaunchResult, error) {
	slog.Info("backend service call", "method", "LaunchGame")
	if service == nil || service.manager == nil {
		return GameLaunchResult{}, errors.New("game manager is unavailable")
	}
	if missing, err := service.manager.reconcileMissingActiveGame(); err != nil {
		return GameLaunchResult{}, err
	} else if missing {
		return GameLaunchResult{}, nil
	}
	var result GameLaunchResult
	err := service.manager.withLaunchableRoot(func(root string) error {
		entry, err := validatedGameEntry(root)
		if err != nil {
			return err
		}
		if service.gameLauncher == nil {
			return errors.New("game launcher is unavailable")
		}
		slog.Info("opening game entry", "entry", entry, "custom_browser", browserID != nil)
		result, err = service.gameLauncher.Launch(entry, browserID)
		return err
	})
	return result, err
}

func (service *LauncherService) OpenGameFolder() error {
	slog.Info("backend service call", "method", "OpenGameFolder")
	if service == nil || service.manager == nil {
		return errors.New("game manager is unavailable")
	}
	if missing, err := service.manager.reconcileMissingActiveGame(); err != nil {
		return err
	} else if missing {
		return nil
	}
	if service.openFolder == nil {
		return errors.New("system folder opener is unavailable")
	}
	return service.manager.withOpenableRoot(func(root string) error {
		slog.Info("opening installed game directory", "directory", root)
		if err := service.openFolder(root, false); err != nil {
			return fmt.Errorf("open game folder: %w", err)
		}
		return nil
	})
}

func (service *LauncherService) OpenGameRepository() error {
	slog.Info("backend service call", "method", "OpenGameRepository")
	return service.openBrowserURL("game repository", gameRepositoryPageURL)
}

func (service *LauncherService) OpenLauncherRepository() error {
	slog.Info("backend service call", "method", "OpenLauncherRepository")
	return service.openBrowserURL("launcher repository", launcherRepositoryPageURL)
}

func (service *LauncherService) openBrowserURL(label, url string) error {
	if service == nil || service.openURL == nil {
		return errors.New("system URL opener is unavailable")
	}
	slog.Info("opening URL in system browser", "target", label, "url", url)
	if err := service.openURL(url); err != nil {
		return fmt.Errorf("open %s: %w", label, err)
	}
	return nil
}

func validatedGameEntry(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve game root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve game root symlinks: %w", err)
	}
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil || !rootInfo.IsDir() {
		return "", errors.New("game root is not a directory")
	}

	entry := filepath.Join(root, "index.html")
	resolvedEntry, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return "", fmt.Errorf("resolve game entry: %w", err)
	}
	if !pathInside(resolvedRoot, resolvedEntry) {
		return "", errors.New("game entry resolves outside the installed game directory")
	}
	entryInfo, err := os.Stat(resolvedEntry)
	if err != nil || !entryInfo.Mode().IsRegular() {
		return "", errors.New("game entry is not a regular file")
	}
	return entry, nil
}

func pathInside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
