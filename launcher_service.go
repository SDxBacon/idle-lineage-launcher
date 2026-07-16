package main

import "log/slog"

type LauncherService struct {
	manager *gameManager
	windows *windowFactory
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

func (service *LauncherService) CreateGameWindow() error {
	slog.Info("backend service call", "method", "CreateGameWindow")
	service.windows.Create()
	return nil
}
