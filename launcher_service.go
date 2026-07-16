package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type fileOpener func(string) error
type folderOpener func(string, bool) error

type LauncherService struct {
	manager    *gameManager
	openFile   fileOpener
	openFolder folderOpener
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

func (service *LauncherService) LaunchGame() error {
	slog.Info("backend service call", "method", "LaunchGame")
	if service == nil || service.manager == nil {
		return errors.New("game manager is unavailable")
	}
	return service.manager.withLaunchableRoot(func(root string) error {
		entry, err := validatedGameEntry(root)
		if err != nil {
			return err
		}
		if service.openFile == nil {
			return errors.New("system file opener is unavailable")
		}
		slog.Info("opening game entry with system HTML handler", "entry", entry)
		if err := service.openFile(entry); err != nil {
			return fmt.Errorf("open game entry: %w", err)
		}
		return nil
	})
}

func (service *LauncherService) OpenGameFolder() error {
	slog.Info("backend service call", "method", "OpenGameFolder")
	if service == nil || service.manager == nil {
		return errors.New("game manager is unavailable")
	}
	root, _, installed := service.manager.ActiveVersion()
	if !installed {
		return errors.New("game is not installed")
	}
	if service.openFolder == nil {
		return errors.New("system folder opener is unavailable")
	}
	slog.Info("opening installed game directory", "directory", root)
	if err := service.openFolder(root, false); err != nil {
		return fmt.Errorf("open game folder: %w", err)
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
