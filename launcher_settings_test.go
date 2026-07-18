package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLauncherSettingsDefaultsAndRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app-data")
	paths := makeDataPaths(root)
	store := newLauncherSettingsStore(paths)

	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.Version != launcherSettingsVersion || settings.GameRoot != root || settings.LastKnownInstalled {
		t.Fatalf("unexpected default settings: %+v", settings)
	}
	custom := filepath.Join(t.TempDir(), "custom")
	settings.GameRoot = custom
	settings.LastKnownInstalled = true
	settings.PendingMove = &pendingGameMove{
		FromRoot: root,
		ToRoot:   custom,
		Phase:    "prepared",
		Commit:   testCommitHash,
	}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.GameRoot != custom || !loaded.LastKnownInstalled || loaded.Version != launcherSettingsVersion || loaded.PendingMove == nil || loaded.PendingMove.Commit != testCommitHash {
		t.Fatalf("settings did not round trip: %+v", loaded)
	}
	info, err := os.Stat(paths.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("settings file is not private: %o", info.Mode().Perm())
	}
}

func TestLauncherSettingsRejectCorruptContents(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	store := newLauncherSettingsStore(paths)
	if err := os.WriteFile(paths.Settings, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("expected corrupt settings to fail")
	}
	contents, err := os.ReadFile(paths.Settings)
	if err != nil || string(contents) != "not-json" {
		t.Fatalf("corrupt settings were unexpectedly overwritten: %q (%v)", contents, err)
	}
}
