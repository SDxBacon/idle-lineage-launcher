//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const windowsRegisteredApplicationsPath = `SOFTWARE\RegisteredApplications`

const (
	windowsSEEMaskClassName = 0x00000001
	windowsSEEMaskNoAsync   = 0x00000100
	windowsSEEMaskFlagNoUI  = 0x00000400
	windowsSWShowNormal     = 1
)

var windowsShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

var callWindowsShellExecuteEx = func(info *windowsShellExecuteInfo) (uintptr, error) {
	succeeded, _, callErr := windowsShellExecuteExW.Call(uintptr(unsafe.Pointer(info)))
	return succeeded, callErr
}

type windowsRegistrySource struct {
	root registry.Key
	view uint32
	name string
}

var windowsGameBrowserRegistrySources = []windowsRegistrySource{
	{root: registry.CURRENT_USER, view: registry.WOW64_64KEY, name: "HKCU 64-bit"},
	{root: registry.CURRENT_USER, view: registry.WOW64_32KEY, name: "HKCU 32-bit"},
	{root: registry.LOCAL_MACHINE, view: registry.WOW64_64KEY, name: "HKLM 64-bit"},
	{root: registry.LOCAL_MACHINE, view: registry.WOW64_32KEY, name: "HKLM 32-bit"},
}

type windowsBrowserRegistry interface {
	ReadValueNames(root registry.Key, path string, view uint32) ([]string, error)
	ReadString(root registry.Key, path, name string, view uint32) (string, error)
}

type nativeWindowsBrowserRegistry struct{}

func (nativeWindowsBrowserRegistry) ReadValueNames(root registry.Key, path string, view uint32) ([]string, error) {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE|view)
	if err != nil {
		return nil, err
	}
	defer key.Close()

	names, err := key.ReadValueNames(-1)
	if errors.Is(err, io.EOF) {
		return names, nil
	}
	return names, err
}

func (nativeWindowsBrowserRegistry) ReadString(root registry.Key, path, name string, view uint32) (string, error) {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE|view)
	if err != nil {
		return "", err
	}
	defer key.Close()

	value, valueType, err := key.GetStringValue(name)
	if err != nil {
		return "", err
	}
	if name == "ApplicationName" && strings.HasPrefix(strings.TrimSpace(value), "@") {
		if loadErr := registry.LoadRegLoadMUIString(); loadErr == nil {
			localized, localizedErr := key.GetMUIStringValue(name)
			if localizedErr == nil && strings.TrimSpace(localized) != "" {
				return localized, nil
			}
		}
	}
	if valueType == registry.EXPAND_SZ {
		return registry.ExpandString(value)
	}
	return value, nil
}

type windowsGameBrowser struct {
	GameBrowser
	htmlProgID string
}

type windowsShellExecuteFunc func(entryPath, htmlProgID string) error

type windowsGameLauncherPlatform struct {
	registry     windowsBrowserRegistry
	shellExecute windowsShellExecuteFunc
	sources      []windowsRegistrySource
}

func newGameLauncherPlatform() gameLauncherPlatform {
	return &windowsGameLauncherPlatform{
		registry:     nativeWindowsBrowserRegistry{},
		shellExecute: newWindowsSTAShellExecutor(),
		sources:      windowsGameBrowserRegistrySources,
	}
}

func (platform *windowsGameLauncherPlatform) GetAvailableBrowsers() ([]GameBrowser, error) {
	browsers, err := platform.discoverBrowsers()
	if err != nil {
		return nil, err
	}
	result := make([]GameBrowser, 0, len(browsers))
	for _, browser := range browsers {
		result = append(result, browser.GameBrowser)
	}
	return result, nil
}

func (platform *windowsGameLauncherPlatform) LaunchDefault(entryPath string) error {
	if platform == nil || platform.shellExecute == nil {
		return errors.New("Windows game launcher is unavailable")
	}
	if strings.TrimSpace(entryPath) == "" {
		return errors.New("game entry path is empty")
	}
	return platform.shellExecute(entryPath, "")
}

func (platform *windowsGameLauncherPlatform) LaunchWithBrowser(entryPath, browserID string) error {
	if platform == nil || platform.shellExecute == nil {
		return errors.New("Windows game launcher is unavailable")
	}
	if strings.TrimSpace(entryPath) == "" {
		return errors.New("game entry path is empty")
	}
	if strings.TrimSpace(browserID) == "" {
		return errors.New("game browser ID is empty")
	}

	browsers, err := platform.discoverBrowsers()
	if err != nil {
		return fmt.Errorf("refresh registered Windows browsers: %w", err)
	}
	for _, browser := range browsers {
		if browser.ID == browserID {
			return platform.shellExecute(entryPath, browser.htmlProgID)
		}
	}
	return fmt.Errorf("game browser %q is no longer registered", browserID)
}

func (platform *windowsGameLauncherPlatform) discoverBrowsers() ([]windowsGameBrowser, error) {
	if platform == nil || platform.registry == nil {
		return nil, errors.New("Windows browser registry is unavailable")
	}

	sources := platform.sources
	if len(sources) == 0 {
		sources = windowsGameBrowserRegistrySources
	}

	byID := make(map[string]windowsGameBrowser)
	var sourceErrors []error
	scannedSource := false
	for _, source := range sources {
		applicationNames, err := platform.registry.ReadValueNames(
			source.root,
			windowsRegisteredApplicationsPath,
			source.view,
		)
		if err != nil {
			if errors.Is(err, registry.ErrNotExist) {
				continue
			}
			sourceErrors = append(sourceErrors, fmt.Errorf("read %s RegisteredApplications: %w", source.name, err))
			continue
		}
		scannedSource = true

		sort.Slice(applicationNames, func(i, j int) bool {
			left := strings.ToLower(applicationNames[i])
			right := strings.ToLower(applicationNames[j])
			if left == right {
				return applicationNames[i] < applicationNames[j]
			}
			return left < right
		})

		for _, applicationName := range applicationNames {
			browser, ok := platform.readBrowser(source, applicationName)
			if !ok {
				continue
			}
			if _, exists := byID[browser.ID]; !exists {
				byID[browser.ID] = browser
			}
		}
	}

	if !scannedSource && len(sourceErrors) > 0 {
		return nil, errors.Join(sourceErrors...)
	}

	browsers := make([]windowsGameBrowser, 0, len(byID))
	for _, browser := range byID {
		browsers = append(browsers, browser)
	}
	sort.Slice(browsers, func(i, j int) bool {
		if browsers[i].Name != browsers[j].Name {
			return browsers[i].Name < browsers[j].Name
		}
		return browsers[i].ID < browsers[j].ID
	})
	return browsers, nil
}

func (platform *windowsGameLauncherPlatform) readBrowser(source windowsRegistrySource, registeredName string) (windowsGameBrowser, bool) {
	capabilitiesPath, err := platform.registry.ReadString(
		source.root,
		windowsRegisteredApplicationsPath,
		registeredName,
		source.view,
	)
	capabilitiesPath = strings.Trim(strings.TrimSpace(capabilitiesPath), `\`)
	if err != nil || capabilitiesPath == "" {
		return windowsGameBrowser{}, false
	}

	httpProgID, err := platform.registry.ReadString(
		source.root,
		capabilitiesPath+`\URLAssociations`,
		"http",
		source.view,
	)
	if err != nil || strings.TrimSpace(httpProgID) == "" {
		return windowsGameBrowser{}, false
	}
	httpsProgID, err := platform.registry.ReadString(
		source.root,
		capabilitiesPath+`\URLAssociations`,
		"https",
		source.view,
	)
	if err != nil || strings.TrimSpace(httpsProgID) == "" {
		return windowsGameBrowser{}, false
	}
	htmlProgID, err := platform.registry.ReadString(
		source.root,
		capabilitiesPath+`\FileAssociations`,
		".html",
		source.view,
	)
	htmlProgID = strings.TrimSpace(htmlProgID)
	if err != nil || htmlProgID == "" {
		return windowsGameBrowser{}, false
	}

	displayName, err := platform.registry.ReadString(
		source.root,
		capabilitiesPath,
		"ApplicationName",
		source.view,
	)
	displayName = strings.TrimSpace(displayName)
	if err != nil || displayName == "" {
		displayName = strings.TrimSpace(registeredName)
	}
	if displayName == "" {
		return windowsGameBrowser{}, false
	}

	return windowsGameBrowser{
		GameBrowser: GameBrowser{
			ID:   windowsBrowserID(htmlProgID),
			Name: displayName,
		},
		htmlProgID: htmlProgID,
	}, true
}

func windowsBrowserID(htmlProgID string) string {
	normalized := strings.ToLower(strings.TrimSpace(htmlProgID))
	digest := sha256.Sum256([]byte("idle-lineage-launcher/windows-game-browser/v1\x00" + normalized))
	return "windows-" + hex.EncodeToString(digest[:])
}

type windowsShellExecuteRequest struct {
	entryPath  string
	htmlProgID string
	result     chan error
}

func newWindowsSTAShellExecutor() windowsShellExecuteFunc {
	requests := make(chan windowsShellExecuteRequest)
	ready := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		initialized, err := initializeWindowsCOMSTA()
		ready <- err
		if err != nil {
			return
		}
		if initialized {
			defer windows.CoUninitialize()
		}

		for request := range requests {
			request.result <- shellExecuteWindowsFile(request.entryPath, request.htmlProgID)
		}
	}()

	if err := <-ready; err != nil {
		return func(string, string) error {
			return fmt.Errorf("initialize COM STA for Windows browser launch: %w", err)
		}
	}

	return func(entryPath, htmlProgID string) error {
		result := make(chan error, 1)
		requests <- windowsShellExecuteRequest{
			entryPath:  entryPath,
			htmlProgID: htmlProgID,
			result:     result,
		}
		return <-result
	}
}

func initializeWindowsCOMSTA() (bool, error) {
	err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	if err == nil {
		return true, nil
	}
	if windowsHRESULTMatches(err, uintptr(windows.S_FALSE)) {
		// S_FALSE still increments COM's per-thread initialization count and
		// therefore requires a matching CoUninitialize call.
		return true, nil
	}
	return false, err
}

func windowsHRESULTMatches(err error, code uintptr) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && uint32(errno) == uint32(code)
}

type windowsShellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     unsafe.Pointer
	lpClass      *uint16
	hkeyClass    registry.Key
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

func shellExecuteWindowsFile(entryPath, htmlProgID string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return fmt.Errorf("encode ShellExecute verb: %w", err)
	}
	file, err := windows.UTF16PtrFromString(entryPath)
	if err != nil {
		return fmt.Errorf("encode game entry path: %w", err)
	}

	mask := windowsShellExecuteMask(htmlProgID)
	var class *uint16
	if htmlProgID != "" {
		class, err = windows.UTF16PtrFromString(htmlProgID)
		if err != nil {
			return fmt.Errorf("encode browser HTML ProgID: %w", err)
		}
	}

	info := windowsShellExecuteInfo{
		fMask:   mask,
		lpVerb:  verb,
		lpFile:  file,
		lpClass: class,
		nShow:   windowsSWShowNormal,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	succeeded, callErr := callWindowsShellExecuteEx(&info)
	runtime.KeepAlive(verb)
	runtime.KeepAlive(file)
	runtime.KeepAlive(class)
	if succeeded == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return fmt.Errorf("ShellExecuteExW: %w", callErr)
		}
		return errors.New("ShellExecuteExW failed without a Windows error code")
	}
	return nil
}

func windowsShellExecuteMask(htmlProgID string) uint32 {
	mask := uint32(windowsSEEMaskNoAsync | windowsSEEMaskFlagNoUI)
	if htmlProgID != "" {
		mask |= windowsSEEMaskClassName
	}
	return mask
}
