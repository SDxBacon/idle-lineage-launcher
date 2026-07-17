//go:build darwin

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestDarwinGameBrowserHandlerRankEligibility(t *testing.T) {
	tests := []struct {
		name     string
		record   darwinGameBrowserRecord
		eligible bool
	}{
		{
			name: "Safari default ranks",
			record: darwinGameBrowserRecord{
				HTTPHandlerRank:  "Default",
				HTTPSHandlerRank: "Default",
			},
			eligible: true,
		},
		{
			name: "Chrome omitted ranks normalised by bridge",
			record: darwinGameBrowserRecord{
				HTTPHandlerRank:  "Default",
				HTTPSHandlerRank: "Default",
			},
			eligible: true,
		},
		{
			name: "owner beats alternate declarations in bridge aggregation",
			record: darwinGameBrowserRecord{
				HTTPHandlerRank:  "Owner",
				HTTPSHandlerRank: "Default",
			},
			eligible: true,
		},
		{
			name: "rank matching is case insensitive",
			record: darwinGameBrowserRecord{
				HTTPHandlerRank:  "  oWnEr ",
				HTTPSHandlerRank: " dEfAuLt\n",
			},
			eligible: true,
		},
		{
			name: "missing metadata remains eligible",
			record: darwinGameBrowserRecord{
				HTTPHandlerRank:  "",
				HTTPSHandlerRank: "",
			},
			eligible: true,
		},
		{
			name: "unknown metadata remains eligible",
			record: darwinGameBrowserRecord{
				HTTPHandlerRank:  "Unknown",
				HTTPSHandlerRank: "future-rank",
			},
			eligible: true,
		},
		{
			name: "iTerm alternate ranks",
			record: darwinGameBrowserRecord{
				HTTPHandlerRank:  "Alternate",
				HTTPSHandlerRank: "Alternate",
			},
			eligible: false,
		},
		{
			name: "one alternate scheme is insufficient",
			record: darwinGameBrowserRecord{
				HTTPHandlerRank:  "Alternate",
				HTTPSHandlerRank: "Default",
			},
			eligible: false,
		},
		{
			name: "none rank is rejected",
			record: darwinGameBrowserRecord{
				HTTPHandlerRank:  "Owner",
				HTTPSHandlerRank: "NoNe",
			},
			eligible: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := darwinGameBrowserRecordIsEligible(test.record); got != test.eligible {
				t.Fatalf("eligibility = %v, want %v for %+v", got, test.eligible, test.record)
			}
		})
	}
}

func TestDarwinGameBrowserIDDoesNotIncludeHandlerRank(t *testing.T) {
	record := darwinGameBrowserRecord{
		Name:             "Safari",
		BundleID:         "com.apple.Safari",
		ApplicationPath:  "/Applications/Safari.app",
		HTTPHandlerRank:  "Default",
		HTTPSHandlerRank: "Default",
	}
	changedRank := record
	changedRank.HTTPHandlerRank = "Alternate"
	changedRank.HTTPSHandlerRank = "None"

	if darwinGameBrowserID(record) != darwinGameBrowserID(changedRank) {
		t.Fatal("handler rank unexpectedly changed the opaque browser ID")
	}
}

func TestDarwinGameLauncherListsOpaqueBrowserIDs(t *testing.T) {
	originalList := darwinListGameBrowsers
	darwinListGameBrowsers = func() ([]darwinGameBrowserRecord, error) {
		return []darwinGameBrowserRecord{
			{Name: "Safari", BundleID: "com.apple.Safari", ApplicationPath: "/Applications/Safari.app"},
			{Name: "Chrome", BundleID: "com.google.Chrome", ApplicationPath: "/Applications/Google Chrome.app"},
		}, nil
	}
	t.Cleanup(func() { darwinListGameBrowsers = originalList })

	browsers, err := (&darwinGameLauncherPlatform{}).GetAvailableBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(browsers) != 2 || browsers[0].Name != "Chrome" || browsers[1].Name != "Safari" {
		t.Fatalf("unexpected browser list: %+v", browsers)
	}
	for _, browser := range browsers {
		if browser.ID == "" || browser.ID == "/Applications/Safari.app" || browser.ID == "/Applications/Google Chrome.app" {
			t.Fatalf("browser path leaked through ID: %+v", browser)
		}
	}
}

func TestDarwinGameLauncherFiltersAlternateAndNoneHandlers(t *testing.T) {
	originalList := darwinListGameBrowsers
	darwinListGameBrowsers = func() ([]darwinGameBrowserRecord, error) {
		return []darwinGameBrowserRecord{
			{
				Name:             "Safari",
				BundleID:         "com.apple.Safari",
				ApplicationPath:  "/Applications/Safari.app",
				HTTPHandlerRank:  "Default",
				HTTPSHandlerRank: "Default",
			},
			{
				Name:             "iTerm2",
				BundleID:         "com.googlecode.iterm2",
				ApplicationPath:  "/Applications/iTerm.app",
				HTTPHandlerRank:  "Alternate",
				HTTPSHandlerRank: "Alternate",
			},
			{
				Name:             "Disabled Handler",
				BundleID:         "example.disabled-handler",
				ApplicationPath:  "/Applications/Disabled Handler.app",
				HTTPHandlerRank:  "None",
				HTTPSHandlerRank: "None",
			},
		}, nil
	}
	t.Cleanup(func() { darwinListGameBrowsers = originalList })

	browsers, err := (&darwinGameLauncherPlatform{}).GetAvailableBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(browsers) != 1 || browsers[0].Name != "Safari" {
		t.Fatalf("unexpected filtered browser list: %+v", browsers)
	}
}

func TestDarwinGameLauncherResolvesBrowserBeforeOpening(t *testing.T) {
	record := darwinGameBrowserRecord{Name: "Safari", BundleID: "com.apple.Safari", ApplicationPath: "/Applications/Safari.app"}
	originalList := darwinListGameBrowsers
	originalOpen := darwinOpenGameWithApp
	darwinListGameBrowsers = func() ([]darwinGameBrowserRecord, error) { return []darwinGameBrowserRecord{record}, nil }
	var openedEntry, openedApplication string
	darwinOpenGameWithApp = func(entryPath, applicationPath string) error {
		openedEntry, openedApplication = entryPath, applicationPath
		return nil
	}
	t.Cleanup(func() {
		darwinListGameBrowsers = originalList
		darwinOpenGameWithApp = originalOpen
	})

	if err := (&darwinGameLauncherPlatform{}).LaunchWithBrowser("/game/index.html", darwinGameBrowserID(record)); err != nil {
		t.Fatal(err)
	}
	if openedEntry != "/game/index.html" || openedApplication != record.ApplicationPath {
		t.Fatalf("unexpected open request: entry=%q application=%q", openedEntry, openedApplication)
	}
}

func TestDarwinGameLauncherPropagatesDefaultCompletionError(t *testing.T) {
	want := errors.New("default application rejected the file")
	originalOpen := darwinOpenDefaultGame
	var openedEntry string
	darwinOpenDefaultGame = func(entryPath string) error {
		openedEntry = entryPath
		return want
	}
	t.Cleanup(func() { darwinOpenDefaultGame = originalOpen })

	err := (&darwinGameLauncherPlatform{}).LaunchDefault("/game/index.html")
	if !errors.Is(err, want) {
		t.Fatalf("unexpected default completion error: %v", err)
	}
	if openedEntry != "/game/index.html" {
		t.Fatalf("unexpected default entry: %q", openedEntry)
	}
}

func TestDarwinGameLauncherPropagatesCustomCompletionError(t *testing.T) {
	record := darwinGameBrowserRecord{Name: "Safari", BundleID: "com.apple.Safari", ApplicationPath: "/Applications/Safari.app"}
	want := errors.New("selected application rejected the file")
	originalList := darwinListGameBrowsers
	originalOpen := darwinOpenGameWithApp
	darwinListGameBrowsers = func() ([]darwinGameBrowserRecord, error) {
		return []darwinGameBrowserRecord{record}, nil
	}
	darwinOpenGameWithApp = func(string, string) error { return want }
	t.Cleanup(func() {
		darwinListGameBrowsers = originalList
		darwinOpenGameWithApp = originalOpen
	})

	err := (&darwinGameLauncherPlatform{}).LaunchWithBrowser(
		"/game/index.html",
		darwinGameBrowserID(record),
	)
	if !errors.Is(err, want) {
		t.Fatalf("unexpected custom completion error: %v", err)
	}
}

func TestDarwinGameLauncherRejectsStaleBrowserID(t *testing.T) {
	originalList := darwinListGameBrowsers
	originalOpen := darwinOpenGameWithApp
	darwinListGameBrowsers = func() ([]darwinGameBrowserRecord, error) { return []darwinGameBrowserRecord{}, nil }
	darwinOpenGameWithApp = func(string, string) error {
		t.Fatal("browser opener must not be called")
		return nil
	}
	t.Cleanup(func() {
		darwinListGameBrowsers = originalList
		darwinOpenGameWithApp = originalOpen
	})

	if err := (&darwinGameLauncherPlatform{}).LaunchWithBrowser("/game/index.html", "darwin:missing"); err == nil {
		t.Fatal("expected stale browser ID error")
	}
}

func TestDarwinGameLauncherRejectsPersistedAlternateHandlerID(t *testing.T) {
	record := darwinGameBrowserRecord{
		Name:             "iTerm2",
		BundleID:         "com.googlecode.iterm2",
		ApplicationPath:  "/Applications/iTerm.app",
		HTTPHandlerRank:  "Alternate",
		HTTPSHandlerRank: "Alternate",
	}
	originalList := darwinListGameBrowsers
	originalOpen := darwinOpenGameWithApp
	darwinListGameBrowsers = func() ([]darwinGameBrowserRecord, error) {
		return []darwinGameBrowserRecord{record}, nil
	}
	darwinOpenGameWithApp = func(string, string) error {
		t.Fatal("ineligible browser opener must not be called")
		return nil
	}
	t.Cleanup(func() {
		darwinListGameBrowsers = originalList
		darwinOpenGameWithApp = originalOpen
	})

	err := (&darwinGameLauncherPlatform{}).LaunchWithBrowser(
		"/game/index.html",
		darwinGameBrowserID(record),
	)
	if err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("expected stale browser error, got %v", err)
	}
}

func TestDarwinGameLauncherFallsBackForPersistedAlternateHandlerID(t *testing.T) {
	record := darwinGameBrowserRecord{
		Name:             "iTerm2",
		BundleID:         "com.googlecode.iterm2",
		ApplicationPath:  "/Applications/iTerm.app",
		HTTPHandlerRank:  "Alternate",
		HTTPSHandlerRank: "Alternate",
	}
	originalList := darwinListGameBrowsers
	originalOpenDefault := darwinOpenDefaultGame
	originalOpenCustom := darwinOpenGameWithApp
	darwinListGameBrowsers = func() ([]darwinGameBrowserRecord, error) {
		return []darwinGameBrowserRecord{record}, nil
	}
	defaultEntries := make([]string, 0, 1)
	darwinOpenDefaultGame = func(entryPath string) error {
		defaultEntries = append(defaultEntries, entryPath)
		return nil
	}
	darwinOpenGameWithApp = func(string, string) error {
		t.Fatal("ineligible browser opener must not be called")
		return nil
	}
	t.Cleanup(func() {
		darwinListGameBrowsers = originalList
		darwinOpenDefaultGame = originalOpenDefault
		darwinOpenGameWithApp = originalOpenCustom
	})

	browserID := darwinGameBrowserID(record)
	result, err := (&GameLauncher{platform: &darwinGameLauncherPlatform{}}).Launch(
		"/game/index.html",
		&browserID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.FallbackToDefault {
		t.Fatal("expected excluded persisted browser to fall back to the system default")
	}
	if len(defaultEntries) != 1 || defaultEntries[0] != "/game/index.html" {
		t.Fatalf("unexpected default launches: %#v", defaultEntries)
	}
}

func TestDarwinGameLauncherPropagatesDiscoveryError(t *testing.T) {
	want := errors.New("workspace unavailable")
	originalList := darwinListGameBrowsers
	darwinListGameBrowsers = func() ([]darwinGameBrowserRecord, error) { return nil, want }
	t.Cleanup(func() { darwinListGameBrowsers = originalList })

	if _, err := (&darwinGameLauncherPlatform{}).GetAvailableBrowsers(); !errors.Is(err, want) {
		t.Fatalf("unexpected discovery error: %v", err)
	}
}

func TestDarwinNativeBrowserDiscoverySmoke(t *testing.T) {
	if os.Getenv("IDLE_LINEAGE_TEST_DARWIN_BROWSER_DISCOVERY") != "1" {
		t.Skip("set IDLE_LINEAGE_TEST_DARWIN_BROWSER_DISCOVERY=1 to inspect native browser discovery")
	}

	records, err := loadDarwinGameBrowserRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Skip("NSWorkspace returned no browser candidates to this command-line test process; verify from the packaged app")
	}
	originalList := darwinListGameBrowsers
	darwinListGameBrowsers = func() ([]darwinGameBrowserRecord, error) { return records, nil }
	t.Cleanup(func() { darwinListGameBrowsers = originalList })

	browsers, err := (&darwinGameLauncherPlatform{}).GetAvailableBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	listedIDs := make(map[string]bool, len(browsers))
	for _, browser := range browsers {
		listedIDs[browser.ID] = true
		t.Logf("listed browser: name=%q id=%q", browser.Name, browser.ID)
	}

	for _, record := range records {
		eligible := darwinGameBrowserRecordIsEligible(record)
		t.Logf(
			"native candidate: name=%q bundleID=%q httpRank=%q httpsRank=%q eligible=%v path=%q",
			record.Name,
			record.BundleID,
			record.HTTPHandlerRank,
			record.HTTPSHandlerRank,
			eligible,
			record.ApplicationPath,
		)

		id := darwinGameBrowserID(record)
		if !eligible && listedIDs[id] {
			t.Fatalf("ineligible native candidate was listed: %+v", record)
		}
		identity := strings.ToLower(record.BundleID + " " + record.Name)
		if strings.Contains(identity, "iterm") && eligible {
			t.Fatalf("iTerm candidate was not classified as alternate: %+v", record)
		}
	}
}
