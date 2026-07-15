package main

type LauncherService struct {
	manager *gameManager
	windows *windowFactory
}

func (service *LauncherService) GetGameState() GameState {
	return service.manager.State()
}

func (service *LauncherService) StartInstall() error {
	return service.manager.StartInstall()
}

func (service *LauncherService) CancelInstall() {
	service.manager.CancelInstall()
}

func (service *LauncherService) CreateGameWindow() error {
	service.windows.Create()
	return nil
}
