//go:build !windows

package main

import "syscall"

func testCrossDeviceError() error {
	return syscall.EXDEV
}
