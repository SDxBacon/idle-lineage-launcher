//go:build ignore

// 此腳本包裝 Wails build-assets updater。此專案不支援 mobile build；若執行前
// 不存在 ios 目錄，便在 updater 結束後移除它額外產生的 iOS 資產與 entitlement。
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./update_build_assets.go <app-name>")
		os.Exit(1)
	}

	iosExisted, err := pathExists("ios")
	if err != nil {
		fmt.Fprintln(os.Stderr, "檢查 iOS build assets 失敗:", err)
		os.Exit(1)
	}

	cmd := exec.Command(
		"wails3", "update", "build-assets",
		"-name", os.Args[1],
		"-binaryname", os.Args[1],
		"-config", "config.yml",
		"-dir", ".",
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()

	var cleanupErr error
	if !iosExisted {
		cleanupErr = os.RemoveAll("ios")
	}
	if runErr != nil || cleanupErr != nil {
		fmt.Fprintln(os.Stderr, errors.Join(runErr, cleanupErr))
		os.Exit(1)
	}
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
