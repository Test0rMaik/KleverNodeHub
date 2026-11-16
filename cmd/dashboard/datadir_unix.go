//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockDataDir takes an exclusive, non-blocking flock on <dataDir>/.lock and
// returns its release func. The running dashboard holds it for its lifetime;
// --import-backup / --compact-db take it too, so they refuse while a hub uses
// the data dir — also from another container on the same volume, where the
// listen-port check can't see it. The lock dies with the process (and is
// dropped on the self-update execve, since Go opens files close-on-exec).
//
// The file is opened read-only (flock works on any fd) and created 0644, so a
// lock file created by root via `sudo --compact-db` can't keep the service
// user's dashboard from starting.
func lockDataDir(dataDir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dataDir, ".lock"), os.O_RDONLY|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errDataDirLocked
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

// copyOwner gives path the owner and group of orig. A no-op when they already
// match, so it needs no privileges in the normal (service user) case.
func copyOwner(path string, orig os.FileInfo) error {
	st, ok := orig.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	cur, err := os.Stat(path)
	if err != nil {
		return err
	}
	if c, ok := cur.Sys().(*syscall.Stat_t); ok && c.Uid == st.Uid && c.Gid == st.Gid {
		return nil
	}
	return os.Chown(path, int(st.Uid), int(st.Gid))
}

// syncDir fsyncs a directory so a rename inside it survives a crash.
// Best-effort.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
