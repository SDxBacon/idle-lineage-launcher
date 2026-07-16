//go:build !production

package main

import "testing"

func TestDevelopmentBuildPinsInitialGameCommit(t *testing.T) {
	const expected = "7e30bc454196683129b8a883a2a1e6011f35bcc6"
	if developmentInitialGameCommit != expected {
		t.Fatalf("development initial commit = %q, want %q", developmentInitialGameCommit, expected)
	}
}
