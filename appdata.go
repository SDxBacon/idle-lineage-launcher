package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const dataDirectoryName = "IdleLineageLauncher"

type dataPaths struct {
	Root     string
	Game     string
	Versions string
	Staging  string
	Active   string
	WebView  string
}

func resolveDataPaths() (dataPaths, error) {
	var base string
	var err error

	switch runtime.GOOS {
	case "darwin":
		var home string
		home, err = os.UserHomeDir()
		if err == nil {
			base = filepath.Join(home, "Library", "Application Support")
		}
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			err = errors.New("LOCALAPPDATA is not set")
		}
	default:
		base, err = os.UserConfigDir()
	}
	if err != nil {
		return dataPaths{}, err
	}
	if base == "" {
		return dataPaths{}, errors.New("unable to locate application data directory")
	}

	root := filepath.Join(base, dataDirectoryName)
	game := filepath.Join(root, "game")
	return dataPaths{
		Root:     root,
		Game:     game,
		Versions: filepath.Join(game, "versions"),
		Staging:  filepath.Join(game, "staging"),
		Active:   filepath.Join(game, "active.json"),
		WebView:  filepath.Join(root, "webview"),
	}, nil
}

func makeDataPaths(root string) dataPaths {
	game := filepath.Join(root, "game")
	return dataPaths{
		Root:     root,
		Game:     game,
		Versions: filepath.Join(game, "versions"),
		Staging:  filepath.Join(game, "staging"),
		Active:   filepath.Join(game, "active.json"),
		WebView:  filepath.Join(root, "webview"),
	}
}
