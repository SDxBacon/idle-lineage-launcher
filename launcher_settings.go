package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const launcherSettingsVersion = 1

type pendingGameMove struct {
	FromRoot string `json:"fromRoot"`
	ToRoot   string `json:"toRoot"`
	Phase    string `json:"phase"`
	Commit   string `json:"commit,omitempty"`
}

type launcherSettings struct {
	Version            int              `json:"version"`
	GameRoot           string           `json:"gameRoot"`
	LastKnownInstalled bool             `json:"lastKnownInstalled"`
	PendingMove        *pendingGameMove `json:"pendingMove,omitempty"`
}

type launcherSettingsStore struct {
	mu          sync.Mutex
	path        string
	defaultRoot string
}

func newLauncherSettingsStore(paths dataPaths) *launcherSettingsStore {
	return &launcherSettingsStore{path: paths.Settings, defaultRoot: paths.Root}
}

func (store *launcherSettingsStore) Load() (launcherSettings, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return launcherSettings{}, fmt.Errorf("create launcher settings directory: %w", err)
	}
	contents, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store.defaults(), nil
	}
	if err != nil {
		return launcherSettings{}, fmt.Errorf("read launcher settings: %w", err)
	}
	var settings launcherSettings
	if err := json.Unmarshal(contents, &settings); err != nil {
		return launcherSettings{}, fmt.Errorf("decode launcher settings: %w", err)
	}
	if settings.Version != launcherSettingsVersion {
		return launcherSettings{}, fmt.Errorf("unsupported launcher settings version %d", settings.Version)
	}
	if settings.GameRoot == "" {
		settings.GameRoot = store.defaultRoot
	}
	return settings, nil
}

func (store *launcherSettingsStore) Save(settings launcherSettings) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	settings.Version = launcherSettingsVersion
	if settings.GameRoot == "" {
		settings.GameRoot = store.defaultRoot
	}
	contents, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode launcher settings: %w", err)
	}
	contents = append(contents, '\n')
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create launcher settings directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary launcher settings: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary launcher settings: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write temporary launcher settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("flush temporary launcher settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary launcher settings: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("replace launcher settings: %w", err)
	}
	committed = true
	return nil
}

func (store *launcherSettingsStore) defaults() launcherSettings {
	return launcherSettings{Version: launcherSettingsVersion, GameRoot: store.defaultRoot}
}
