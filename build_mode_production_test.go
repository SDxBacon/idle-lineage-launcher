//go:build production

package main

import "testing"

func TestProductionBuildUsesMainTip(t *testing.T) {
	if developmentInitialGameCommit != "" {
		t.Fatalf("production build unexpectedly pins %q", developmentInitialGameCommit)
	}
}
