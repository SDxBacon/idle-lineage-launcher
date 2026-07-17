package main

import (
	"errors"
	"reflect"
	"testing"
)

type gameLaunchCall struct {
	entryPath string
	browserID string
}

type stubGameLauncherPlatform struct {
	browsers          []GameBrowser
	browserErr        error
	defaultErr        error
	customErr         error
	launchDefault     func(string) error
	launchWithBrowser func(string, string) error
	getCalls          int
	defaultEntries    []string
	customCalls       []gameLaunchCall
}

func (platform *stubGameLauncherPlatform) GetAvailableBrowsers() ([]GameBrowser, error) {
	platform.getCalls++
	return platform.browsers, platform.browserErr
}

func (platform *stubGameLauncherPlatform) LaunchDefault(entryPath string) error {
	platform.defaultEntries = append(platform.defaultEntries, entryPath)
	if platform.launchDefault != nil {
		return platform.launchDefault(entryPath)
	}
	return platform.defaultErr
}

func (platform *stubGameLauncherPlatform) LaunchWithBrowser(entryPath, browserID string) error {
	platform.customCalls = append(platform.customCalls, gameLaunchCall{entryPath: entryPath, browserID: browserID})
	if platform.launchWithBrowser != nil {
		return platform.launchWithBrowser(entryPath, browserID)
	}
	return platform.customErr
}

func testGameLauncher(platform *stubGameLauncherPlatform) *GameLauncher {
	return &GameLauncher{platform: platform}
}

func testGameLauncherWithDefault(open func(string) error) *GameLauncher {
	return testGameLauncher(&stubGameLauncherPlatform{launchDefault: open})
}

func TestGameLauncherSortsAvailableBrowsers(t *testing.T) {
	platform := &stubGameLauncherPlatform{browsers: []GameBrowser{
		{ID: "z", Name: "Firefox"},
		{ID: "b", Name: "Chrome"},
		{ID: "a", Name: "Chrome"},
	}}
	launcher := testGameLauncher(platform)

	got, err := launcher.GetAvailableBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	want := []GameBrowser{
		{ID: "a", Name: "Chrome"},
		{ID: "b", Name: "Chrome"},
		{ID: "z", Name: "Firefox"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected browsers: got %+v, want %+v", got, want)
	}
	if platform.getCalls != 1 {
		t.Fatalf("unexpected discovery calls: %d", platform.getCalls)
	}
}

func TestGameLauncherReturnsNonNilEmptyBrowserList(t *testing.T) {
	launcher := testGameLauncher(&stubGameLauncherPlatform{})

	browsers, err := launcher.GetAvailableBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	if browsers == nil || len(browsers) != 0 {
		t.Fatalf("expected non-nil empty browser list, got %#v", browsers)
	}
}

func TestGameLauncherReturnsBrowserDiscoveryError(t *testing.T) {
	want := errors.New("discovery failed")
	launcher := testGameLauncher(&stubGameLauncherPlatform{browserErr: want})

	if _, err := launcher.GetAvailableBrowsers(); !errors.Is(err, want) {
		t.Fatalf("unexpected discovery error: %v", err)
	}
}

func TestGameLauncherRejectsUnavailablePlatform(t *testing.T) {
	var nilLauncher *GameLauncher
	if _, err := nilLauncher.GetAvailableBrowsers(); err == nil {
		t.Fatal("expected unavailable launcher discovery to fail")
	}
	if _, err := (&GameLauncher{}).Launch("/game/index.html", nil); err == nil {
		t.Fatal("expected unavailable launcher launch to fail")
	}
}

func TestGameLauncherUsesSystemDefaultWhenBrowserIsNil(t *testing.T) {
	platform := &stubGameLauncherPlatform{}
	launcher := testGameLauncher(platform)

	result, err := launcher.Launch("/game/index.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.FallbackToDefault {
		t.Fatal("a requested default launch must not be reported as fallback")
	}
	if !reflect.DeepEqual(platform.defaultEntries, []string{"/game/index.html"}) {
		t.Fatalf("unexpected default launches: %#v", platform.defaultEntries)
	}
	if len(platform.customCalls) != 0 {
		t.Fatalf("unexpected custom launches: %#v", platform.customCalls)
	}
}

func TestGameLauncherReturnsDefaultLaunchError(t *testing.T) {
	want := errors.New("default failed")
	launcher := testGameLauncher(&stubGameLauncherPlatform{defaultErr: want})

	result, err := launcher.Launch("/game/index.html", nil)
	if !errors.Is(err, want) {
		t.Fatalf("unexpected default launch error: %v", err)
	}
	if result.FallbackToDefault {
		t.Fatal("failed default launch must not be reported as successful fallback")
	}
}

func TestGameLauncherUsesSelectedBrowser(t *testing.T) {
	platform := &stubGameLauncherPlatform{}
	launcher := testGameLauncher(platform)
	browserID := "opaque-browser-id"

	result, err := launcher.Launch("/game/index.html", &browserID)
	if err != nil {
		t.Fatal(err)
	}
	if result.FallbackToDefault {
		t.Fatal("successful selected browser launch must not report fallback")
	}
	wantCalls := []gameLaunchCall{{entryPath: "/game/index.html", browserID: browserID}}
	if !reflect.DeepEqual(platform.customCalls, wantCalls) {
		t.Fatalf("unexpected selected browser launches: got %#v, want %#v", platform.customCalls, wantCalls)
	}
	if len(platform.defaultEntries) != 0 {
		t.Fatalf("default browser was launched after selected browser succeeded: %#v", platform.defaultEntries)
	}
}

func TestGameLauncherFallsBackToDefault(t *testing.T) {
	customErr := errors.New("selected browser disappeared")
	platform := &stubGameLauncherPlatform{customErr: customErr}
	launcher := testGameLauncher(platform)
	browserID := "stale-or-tampered-id"

	result, err := launcher.Launch("/game/index.html", &browserID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.FallbackToDefault {
		t.Fatal("expected successful fallback to be reported")
	}
	if len(platform.customCalls) != 1 || len(platform.defaultEntries) != 1 {
		t.Fatalf("unexpected launch calls: custom=%#v default=%#v", platform.customCalls, platform.defaultEntries)
	}
}

func TestGameLauncherPreservesSelectedAndFallbackErrors(t *testing.T) {
	customErr := errors.New("selected failed")
	defaultErr := errors.New("default failed")
	launcher := testGameLauncher(&stubGameLauncherPlatform{
		customErr:  customErr,
		defaultErr: defaultErr,
	})
	browserID := "opaque-browser-id"

	result, err := launcher.Launch("/game/index.html", &browserID)
	if !errors.Is(err, customErr) || !errors.Is(err, defaultErr) {
		t.Fatalf("combined launch error did not preserve both causes: %v", err)
	}
	if result.FallbackToDefault {
		t.Fatal("failed fallback must not be reported as successful")
	}
}
