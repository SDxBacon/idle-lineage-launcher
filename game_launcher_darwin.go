//go:build darwin

package main

/*
#cgo CFLAGS: -fblocks
#cgo LDFLAGS: -framework AppKit -framework Foundation -framework UniformTypeIdentifiers
#include <stdlib.h>
#include "game_launcher_darwin.h"
*/
import "C"

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unsafe"
)

type darwinGameLauncherPlatform struct{}

type darwinGameBrowserRecord struct {
	Name             string `json:"name"`
	BundleID         string `json:"bundleID"`
	ApplicationPath  string `json:"applicationPath"`
	HTTPHandlerRank  string `json:"httpHandlerRank"`
	HTTPSHandlerRank string `json:"httpsHandlerRank"`
}

var (
	darwinListGameBrowsers = loadDarwinGameBrowserRecords
	darwinOpenDefaultGame  = openDarwinDefaultGame
	darwinOpenGameWithApp  = openDarwinGameWithApplication
)

func newGameLauncherPlatform() gameLauncherPlatform {
	return &darwinGameLauncherPlatform{}
}

func (platform *darwinGameLauncherPlatform) GetAvailableBrowsers() ([]GameBrowser, error) {
	records, err := darwinListGameBrowsers()
	if err != nil {
		return nil, err
	}
	browsers := make([]GameBrowser, 0, len(records))
	for _, record := range records {
		if record.Name == "" || record.ApplicationPath == "" || !darwinGameBrowserRecordIsEligible(record) {
			continue
		}
		browsers = append(browsers, GameBrowser{ID: darwinGameBrowserID(record), Name: record.Name})
	}
	sort.Slice(browsers, func(i, j int) bool {
		if browsers[i].Name == browsers[j].Name {
			return browsers[i].ID < browsers[j].ID
		}
		return browsers[i].Name < browsers[j].Name
	})
	if browsers == nil {
		browsers = []GameBrowser{}
	}
	return browsers, nil
}

func (platform *darwinGameLauncherPlatform) LaunchDefault(entryPath string) error {
	return darwinOpenDefaultGame(entryPath)
}

func (platform *darwinGameLauncherPlatform) LaunchWithBrowser(entryPath, browserID string) error {
	records, err := darwinListGameBrowsers()
	if err != nil {
		return fmt.Errorf("refresh available game browsers: %w", err)
	}
	for _, record := range records {
		if darwinGameBrowserID(record) == browserID && darwinGameBrowserRecordIsEligible(record) {
			return darwinOpenGameWithApp(entryPath, record.ApplicationPath)
		}
	}
	return fmt.Errorf("game browser %q is no longer available", browserID)
}

func darwinGameBrowserID(record darwinGameBrowserRecord) string {
	digest := sha256.Sum256([]byte(record.BundleID + "\x00" + record.ApplicationPath))
	return "darwin:" + hex.EncodeToString(digest[:])
}

func darwinGameBrowserRecordIsEligible(record darwinGameBrowserRecord) bool {
	return darwinHandlerRankIsEligible(record.HTTPHandlerRank) &&
		darwinHandlerRankIsEligible(record.HTTPSHandlerRank)
}

func darwinHandlerRankIsEligible(rank string) bool {
	switch strings.ToLower(strings.TrimSpace(rank)) {
	case "alternate", "none":
		return false
	default:
		// An empty or unrecognised rank means the bundle metadata could not
		// conclusively classify the handler. NSWorkspace already confirmed the
		// application can open the scheme, so retain it to avoid false negatives.
		return true
	}
}

func loadDarwinGameBrowserRecords() ([]darwinGameBrowserRecord, error) {
	var cError *C.char
	jsonData := C.idle_lineage_copy_game_browsers_json(&cError)
	if jsonData == nil {
		return nil, consumeDarwinBridgeError(cError, "list applications capable of opening the game")
	}
	defer C.free(unsafe.Pointer(jsonData))

	var records []darwinGameBrowserRecord
	if err := json.Unmarshal([]byte(C.GoString(jsonData)), &records); err != nil {
		return nil, fmt.Errorf("decode available game browsers: %w", err)
	}
	if records == nil {
		records = []darwinGameBrowserRecord{}
	}
	return records, nil
}

func openDarwinDefaultGame(entryPath string) error {
	cPath := C.CString(entryPath)
	defer C.free(unsafe.Pointer(cPath))
	var cError *C.char
	if C.idle_lineage_open_game_default(cPath, &cError) == 0 {
		return consumeDarwinBridgeError(cError, "open the game with the default browser")
	}
	return nil
}

func openDarwinGameWithApplication(entryPath, applicationPath string) error {
	cPath := C.CString(entryPath)
	cApplicationPath := C.CString(applicationPath)
	defer C.free(unsafe.Pointer(cPath))
	defer C.free(unsafe.Pointer(cApplicationPath))
	var cError *C.char
	if C.idle_lineage_open_game_with_application(cPath, cApplicationPath, &cError) == 0 {
		return consumeDarwinBridgeError(cError, "open the game with the selected browser")
	}
	return nil
}

func consumeDarwinBridgeError(cError *C.char, fallback string) error {
	if cError == nil {
		return errors.New(fallback)
	}
	defer C.free(unsafe.Pointer(cError))
	message := C.GoString(cError)
	if message == "" {
		message = fallback
	}
	return errors.New(message)
}
