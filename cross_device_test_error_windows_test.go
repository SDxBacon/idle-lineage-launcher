//go:build windows

package main

import "golang.org/x/sys/windows"

func testCrossDeviceError() error {
	return windows.ERROR_NOT_SAME_DEVICE
}
