//go:build !production

package main

import "testing"

func TestDevelopmentBuildPinsInitialGameCommit(t *testing.T) {
	const expected = "68249bcfd92e89fc14a6a9812bdf456aa430921b"
	if developmentInitialGameCommit != expected {
		t.Fatalf("development initial commit = %q, want %q", developmentInitialGameCommit, expected)
	}
}
