//go:build !darwin && !windows

package main

import (
	"fmt"
	"os/exec"
)

type otherGameLauncherPlatform struct{}

func newGameLauncherPlatform() gameLauncherPlatform {
	return &otherGameLauncherPlatform{}
}

func (platform *otherGameLauncherPlatform) GetAvailableBrowsers() ([]GameBrowser, error) {
	return []GameBrowser{}, nil
}

func (platform *otherGameLauncherPlatform) LaunchDefault(entryPath string) error {
	command := exec.Command("xdg-open", entryPath)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start system file opener: %w", err)
	}
	go command.Wait() //nolint:errcheck
	return nil
}

func (platform *otherGameLauncherPlatform) LaunchWithBrowser(string, string) error {
	return fmt.Errorf("custom game browsers are not supported on this platform")
}
