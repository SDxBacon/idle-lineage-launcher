package main

import (
	"errors"
	"os"
	"testing"
)

func TestIsCrossDeviceError(t *testing.T) {
	crossDevice := testCrossDeviceError()
	if !isCrossDeviceError(crossDevice) {
		t.Fatalf("platform cross-device error was not recognised: %v", crossDevice)
	}
	wrapped := &os.LinkError{Op: "rename", Old: "old", New: "new", Err: crossDevice}
	if !isCrossDeviceError(wrapped) {
		t.Fatalf("wrapped cross-device error was not recognised: %v", wrapped)
	}
	if isCrossDeviceError(errors.New("rename failed")) {
		t.Fatal("ordinary rename error was misidentified as cross-device")
	}
}
