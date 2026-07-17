//go:build windows

package main

import (
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type fakeWindowsRegistryLocation struct {
	root registry.Key
	view uint32
	path string
}

type fakeWindowsRegistryValue struct {
	fakeWindowsRegistryLocation
	name string
}

type fakeWindowsBrowserRegistry struct {
	names           map[fakeWindowsRegistryLocation][]string
	values          map[fakeWindowsRegistryValue]string
	namesErrors     map[fakeWindowsRegistryLocation]error
	valueNamesCalls []fakeWindowsRegistryLocation
}

func newFakeWindowsBrowserRegistry() *fakeWindowsBrowserRegistry {
	return &fakeWindowsBrowserRegistry{
		names:       make(map[fakeWindowsRegistryLocation][]string),
		values:      make(map[fakeWindowsRegistryValue]string),
		namesErrors: make(map[fakeWindowsRegistryLocation]error),
	}
}

func (registryFake *fakeWindowsBrowserRegistry) ReadValueNames(root registry.Key, path string, view uint32) ([]string, error) {
	location := fakeWindowsRegistryLocation{root: root, path: path, view: view}
	registryFake.valueNamesCalls = append(registryFake.valueNamesCalls, location)
	if err, exists := registryFake.namesErrors[location]; exists {
		return nil, err
	}
	names, exists := registryFake.names[location]
	if !exists {
		return nil, registry.ErrNotExist
	}
	return append([]string(nil), names...), nil
}

func (registryFake *fakeWindowsBrowserRegistry) ReadString(root registry.Key, path, name string, view uint32) (string, error) {
	value, exists := registryFake.values[fakeWindowsRegistryValue{
		fakeWindowsRegistryLocation: fakeWindowsRegistryLocation{root: root, path: path, view: view},
		name:                        name,
	}]
	if !exists {
		return "", registry.ErrNotExist
	}
	return value, nil
}

func (registryFake *fakeWindowsBrowserRegistry) addBrowser(
	source windowsRegistrySource,
	registeredName string,
	capabilitiesPath string,
	displayName string,
	httpProgID string,
	httpsProgID string,
	htmlProgID string,
) {
	registeredLocation := fakeWindowsRegistryLocation{
		root: source.root,
		view: source.view,
		path: windowsRegisteredApplicationsPath,
	}
	registryFake.names[registeredLocation] = append(registryFake.names[registeredLocation], registeredName)
	registryFake.values[fakeWindowsRegistryValue{fakeWindowsRegistryLocation: registeredLocation, name: registeredName}] = capabilitiesPath

	capabilitiesLocation := fakeWindowsRegistryLocation{root: source.root, view: source.view, path: capabilitiesPath}
	registryFake.values[fakeWindowsRegistryValue{fakeWindowsRegistryLocation: capabilitiesLocation, name: "ApplicationName"}] = displayName
	registryFake.values[fakeWindowsRegistryValue{
		fakeWindowsRegistryLocation: fakeWindowsRegistryLocation{root: source.root, view: source.view, path: capabilitiesPath + `\URLAssociations`},
		name:                        "http",
	}] = httpProgID
	registryFake.values[fakeWindowsRegistryValue{
		fakeWindowsRegistryLocation: fakeWindowsRegistryLocation{root: source.root, view: source.view, path: capabilitiesPath + `\URLAssociations`},
		name:                        "https",
	}] = httpsProgID
	registryFake.values[fakeWindowsRegistryValue{
		fakeWindowsRegistryLocation: fakeWindowsRegistryLocation{root: source.root, view: source.view, path: capabilitiesPath + `\FileAssociations`},
		name:                        ".html",
	}] = htmlProgID
}

func TestWindowsGameBrowserDiscoveryRequiresHTTPHTTPSAndHTML(t *testing.T) {
	source := windowsRegistrySource{root: registry.CURRENT_USER, view: registry.WOW64_64KEY, name: "test"}
	registryFake := newFakeWindowsBrowserRegistry()
	registryFake.addBrowser(source, "valid", `Software\Valid\Capabilities`, "Valid Browser", "valid.http", "valid.https", "valid.html")
	registryFake.addBrowser(source, "no-http", `Software\NoHTTP\Capabilities`, "No HTTP", "", "no-http.https", "no-http.html")
	registryFake.addBrowser(source, "no-https", `Software\NoHTTPS\Capabilities`, "No HTTPS", "no-https.http", "", "no-https.html")
	registryFake.addBrowser(source, "no-html", `Software\NoHTML\Capabilities`, "No HTML", "no-html.http", "no-html.https", "")

	platform := &windowsGameLauncherPlatform{registry: registryFake, sources: []windowsRegistrySource{source}}
	browsers, err := platform.GetAvailableBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(browsers) != 1 {
		t.Fatalf("unexpected browser list: %+v", browsers)
	}
	if browsers[0].Name != "Valid Browser" {
		t.Fatalf("unexpected browser name: %+v", browsers[0])
	}
	if !strings.HasPrefix(browsers[0].ID, "windows-") || strings.Contains(browsers[0].ID, "valid.html") {
		t.Fatalf("browser ID is not opaque: %q", browsers[0].ID)
	}
}

func TestWindowsGameBrowserDiscoveryReturnsEmptyArray(t *testing.T) {
	source := windowsRegistrySource{root: registry.CURRENT_USER, view: registry.WOW64_64KEY, name: "test"}
	registryFake := newFakeWindowsBrowserRegistry()
	registryFake.names[fakeWindowsRegistryLocation{
		root: source.root,
		view: source.view,
		path: windowsRegisteredApplicationsPath,
	}] = []string{}

	platform := &windowsGameLauncherPlatform{registry: registryFake, sources: []windowsRegistrySource{source}}
	browsers, err := platform.GetAvailableBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	if browsers == nil || len(browsers) != 0 {
		t.Fatalf("expected a non-nil empty browser list, got %#v", browsers)
	}
}

func TestWindowsGameBrowserIDNormalizesProgIDCase(t *testing.T) {
	if windowsBrowserID(" Browser.HTML ") != windowsBrowserID("browser.html") {
		t.Fatal("Windows ProgID case produced an unstable browser ID")
	}
	if windowsBrowserID("browser.html") == windowsBrowserID("other.html") {
		t.Fatal("different Windows ProgIDs produced the same browser ID")
	}
}

func TestWindowsGameBrowserDiscoveryUsesAllViewsAndDeduplicatesByProgID(t *testing.T) {
	registryFake := newFakeWindowsBrowserRegistry()
	for _, source := range windowsGameBrowserRegistrySources {
		registryFake.names[fakeWindowsRegistryLocation{
			root: source.root,
			view: source.view,
			path: windowsRegisteredApplicationsPath,
		}] = []string{}
	}
	registryFake.addBrowser(windowsGameBrowserRegistrySources[0], "user-browser", `Software\UserBrowser\Capabilities`, "User Browser", "user.http", "user.https", "shared.html")
	registryFake.addBrowser(windowsGameBrowserRegistrySources[3], "machine-browser", `Software\MachineBrowser\Capabilities`, "Machine Duplicate", "machine.http", "machine.https", "SHARED.HTML")
	registryFake.addBrowser(windowsGameBrowserRegistrySources[1], "other-browser", `Software\OtherBrowser\Capabilities`, "Another Browser", "other.http", "other.https", "other.html")

	platform := &windowsGameLauncherPlatform{registry: registryFake, sources: windowsGameBrowserRegistrySources}
	browsers, err := platform.GetAvailableBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	want := []GameBrowser{
		{ID: windowsBrowserID("other.html"), Name: "Another Browser"},
		{ID: windowsBrowserID("shared.html"), Name: "User Browser"},
	}
	if !reflect.DeepEqual(browsers, want) {
		t.Fatalf("unexpected deduplicated browser list:\n got: %+v\nwant: %+v", browsers, want)
	}

	if len(registryFake.valueNamesCalls) != len(windowsGameBrowserRegistrySources) {
		t.Fatalf("did not scan every registry source: %+v", registryFake.valueNamesCalls)
	}
	for index, source := range windowsGameBrowserRegistrySources {
		call := registryFake.valueNamesCalls[index]
		if call.root != source.root || call.view != source.view {
			t.Fatalf("registry source %d mismatch: got %+v, want %+v", index, call, source)
		}
	}
}

func TestWindowsLaunchWithBrowserRefreshesRegistryAndUsesHTMLProgID(t *testing.T) {
	source := windowsRegistrySource{root: registry.CURRENT_USER, view: registry.WOW64_64KEY, name: "test"}
	registryFake := newFakeWindowsBrowserRegistry()
	registryFake.addBrowser(source, "browser", `Software\Browser\Capabilities`, "Browser", "browser.http", "browser.https", "browser.html")

	var gotEntry string
	var gotProgID string
	platform := &windowsGameLauncherPlatform{
		registry: registryFake,
		sources:  []windowsRegistrySource{source},
		shellExecute: func(entryPath, htmlProgID string) error {
			gotEntry = entryPath
			gotProgID = htmlProgID
			return nil
		},
	}

	if err := platform.LaunchWithBrowser(`C:\Games\Idle Lineage\index.html`, windowsBrowserID("browser.html")); err != nil {
		t.Fatal(err)
	}
	if gotEntry != `C:\Games\Idle Lineage\index.html` || gotProgID != "browser.html" {
		t.Fatalf("unexpected ShellExecute request: entry=%q ProgID=%q", gotEntry, gotProgID)
	}
	if len(registryFake.valueNamesCalls) != 1 {
		t.Fatalf("expected one fresh registry scan, got %d", len(registryFake.valueNamesCalls))
	}
}

func TestWindowsLaunchWithStaleBrowserDoesNotExecuteRegistryData(t *testing.T) {
	source := windowsRegistrySource{root: registry.CURRENT_USER, view: registry.WOW64_64KEY, name: "test"}
	registryFake := newFakeWindowsBrowserRegistry()
	registryFake.names[fakeWindowsRegistryLocation{root: source.root, view: source.view, path: windowsRegisteredApplicationsPath}] = []string{}
	platform := &windowsGameLauncherPlatform{
		registry: registryFake,
		sources:  []windowsRegistrySource{source},
		shellExecute: func(string, string) error {
			t.Fatal("ShellExecute must not receive an unrecognized frontend ID")
			return nil
		},
	}

	err := platform.LaunchWithBrowser(`C:\Games\index.html`, `C:\malicious.exe --argument`)
	if err == nil || !strings.Contains(err.Error(), "no longer registered") {
		t.Fatalf("unexpected stale-browser error: %v", err)
	}
}

func TestWindowsLaunchDefaultOmitsHTMLProgID(t *testing.T) {
	var gotEntry string
	var gotProgID string
	platform := &windowsGameLauncherPlatform{
		shellExecute: func(entryPath, htmlProgID string) error {
			gotEntry = entryPath
			gotProgID = htmlProgID
			return nil
		},
	}
	if err := platform.LaunchDefault(`C:\Games\index.html`); err != nil {
		t.Fatal(err)
	}
	if gotEntry != `C:\Games\index.html` || gotProgID != "" {
		t.Fatalf("unexpected default ShellExecute request: entry=%q ProgID=%q", gotEntry, gotProgID)
	}
}

func TestWindowsDiscoveryReturnsRegistryErrorWhenNoViewCanBeScanned(t *testing.T) {
	registryFake := newFakeWindowsBrowserRegistry()
	want := syscall.ERROR_ACCESS_DENIED
	for _, source := range windowsGameBrowserRegistrySources {
		registryFake.namesErrors[fakeWindowsRegistryLocation{
			root: source.root,
			view: source.view,
			path: windowsRegisteredApplicationsPath,
		}] = want
	}

	platform := &windowsGameLauncherPlatform{registry: registryFake, sources: windowsGameBrowserRegistrySources}
	if _, err := platform.GetAvailableBrowsers(); !errors.Is(err, want) {
		t.Fatalf("unexpected registry error: %v", err)
	}
}

func TestWindowsShellExecuteMaskAlwaysDisablesAsyncAndUI(t *testing.T) {
	defaultMask := windowsShellExecuteMask("")
	if defaultMask != windowsSEEMaskNoAsync|windowsSEEMaskFlagNoUI {
		t.Fatalf("unexpected default ShellExecute mask: %#x", defaultMask)
	}
	customMask := windowsShellExecuteMask("browser.html")
	if customMask != windowsSEEMaskNoAsync|windowsSEEMaskFlagNoUI|windowsSEEMaskClassName {
		t.Fatalf("unexpected custom ShellExecute mask: %#x", customMask)
	}
}

func TestWindowsShellExecuteInfoMatchesNativeLayout(t *testing.T) {
	want := uintptr(60)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		want = 112
	}
	if got := unsafe.Sizeof(windowsShellExecuteInfo{}); got != want {
		t.Fatalf("unexpected SHELLEXECUTEINFOW size: got %d, want %d", got, want)
	}
}

func TestWindowsShellExecutePreservesLastErrorOnFailedHandoff(t *testing.T) {
	originalCall := callWindowsShellExecuteEx
	want := syscall.ERROR_FILE_NOT_FOUND
	callWindowsShellExecuteEx = func(info *windowsShellExecuteInfo) (uintptr, error) {
		if info.fMask != windowsSEEMaskNoAsync|windowsSEEMaskFlagNoUI|windowsSEEMaskClassName {
			t.Fatalf("unexpected custom ShellExecute mask: %#x", info.fMask)
		}
		if info.lpClass == nil {
			t.Fatal("custom ShellExecute request omitted the HTML ProgID")
		}
		return 0, want
	}
	t.Cleanup(func() { callWindowsShellExecuteEx = originalCall })

	err := shellExecuteWindowsFile(`C:\Games\index.html`, "browser.html")
	if !errors.Is(err, want) {
		t.Fatalf("ShellExecute error was not preserved: %v", err)
	}
}

func TestWindowsShellExecuteIgnoresLastErrorAfterSuccessfulHandoff(t *testing.T) {
	originalCall := callWindowsShellExecuteEx
	callWindowsShellExecuteEx = func(info *windowsShellExecuteInfo) (uintptr, error) {
		if info.fMask != windowsSEEMaskNoAsync|windowsSEEMaskFlagNoUI {
			t.Fatalf("unexpected default ShellExecute mask: %#x", info.fMask)
		}
		if info.lpClass != nil {
			t.Fatal("default ShellExecute request must not specify a ProgID")
		}
		return 1, syscall.ERROR_ACCESS_DENIED
	}
	t.Cleanup(func() { callWindowsShellExecuteEx = originalCall })

	if err := shellExecuteWindowsFile(`C:\Games\index.html`, ""); err != nil {
		t.Fatalf("successful ShellExecute handoff returned stale last error: %v", err)
	}
}

func TestWindowsCOMInitializationRecognizesSFalse(t *testing.T) {
	err := syscall.Errno(windows.S_FALSE)
	if !windowsHRESULTMatches(err, uintptr(windows.S_FALSE)) {
		t.Fatalf("S_FALSE was not recognized: %v", err)
	}
}
