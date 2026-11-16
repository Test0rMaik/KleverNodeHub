//go:build windows

package main

import "os"

// lockDataDir is a no-op on Windows; the listen-port check in
// ensureDashboardStopped is the only guard there.
func lockDataDir(string) (func(), error) {
	return func() {}, nil
}

// copyOwner is a no-op on Windows (no uid/gid ownership to preserve).
func copyOwner(string, os.FileInfo) error { return nil }

// syncDir is a no-op on Windows (directories can't be fsynced there).
func syncDir(string) {}
