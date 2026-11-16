package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/CTJaeger/KleverNodeHub/internal/backup"
	"github.com/CTJaeger/KleverNodeHub/internal/store"
)

// backupPassphrase resolves the backup passphrase: --backup-passphrase-file,
// then $KNH_BACKUP_PASSPHRASE, then an interactive no-echo prompt (twice when
// confirm is set, e.g. when creating a backup).
func backupPassphrase(file string, confirm bool) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read passphrase file: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	if v := os.Getenv("KNH_BACKUP_PASSPHRASE"); v != "" {
		return v, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("no passphrase: use --backup-passphrase-file, $KNH_BACKUP_PASSPHRASE, or run in a terminal")
	}
	fmt.Fprint(os.Stderr, "Backup passphrase: ")
	p1, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Repeat passphrase: ")
		p2, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(p1, p2) {
			return "", fmt.Errorf("passphrases do not match")
		}
	}
	return string(p1), nil
}

// runExportBackup writes an encrypted backup of the data dir to outFile. Safe to
// run while the dashboard is running (consistent SQLite snapshot).
func runExportBackup(dataDir, outFile, passFile string, includeMetrics bool) error {
	dbPath := filepath.Join(dataDir, "dashboard.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no dashboard database in %s: %w", dataDir, err)
	}
	pass, err := backupPassphrase(passFile, true)
	if err != nil {
		return err
	}
	db, err := store.OpenNoMigrate(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	// O_EXCL: never clobber an existing file; 0600: the backup holds secrets.
	f, err := os.OpenFile(outFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create %s: %w", outFile, err)
	}
	m, err := backup.Export(db, filepath.Join(dataDir, "ca"), pass, backup.ExportOptions{IncludeMetrics: includeMetrics}, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(outFile)
		return err
	}
	fmt.Printf("Backup written to %s (%d servers, %d nodes, metrics included: %v)\n", outFile, m.Servers, m.Nodes, m.IncludesMetrics)
	return nil
}

// runImportBackup restores a backup into dataDir. Must be run while the
// dashboard is stopped.
func runImportBackup(dataDir, inFile, passFile, addr string, force bool) error {
	unlock, err := lockDataDirForMaintenance(dataDir, addr, "importing")
	if err != nil {
		return err
	}
	defer unlock()
	pass, err := backupPassphrase(passFile, false)
	if err != nil {
		return err
	}
	f, err := os.Open(inFile)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer func() { _ = f.Close() }()
	m, err := backup.Import(f, pass, dataDir, force)
	if err != nil {
		return err
	}
	fmt.Printf("Restored backup from %s (made by %s, %d servers, %d nodes) into %s\n", m.CreatedAt, m.AppVersion, m.Servers, m.Nodes, dataDir)
	if len(m.MovedAside) > 0 {
		fmt.Println("Previous data was moved aside (it still contains the OLD secrets — delete it once the restore is confirmed):")
		for _, p := range m.MovedAside {
			fmt.Println("  ", p)
		}
	}
	fmt.Println("You will need to log in again (sessions are reset). After the move works, wipe the OLD hub's data dir (it holds the CA key).")
	fmt.Println("Start the dashboard now. Agents reconnect on their own if they can reach this hub at the same address.")
	return nil
}

// ensureDashboardStopped refuses to touch the data dir under a live dashboard:
// if the listen address is taken, a hub is (very likely) still running
// against this data dir.
func ensureDashboardStopped(addr, action string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot bind %s (%v) — a dashboard seems to be running; stop it before %s", addr, err, action)
	}
	_ = ln.Close()
	return nil
}

// errDataDirLocked means another process (normally a running dashboard)
// holds the data-dir lock.
var errDataDirLocked = errors.New("data directory is locked")

// lockDataDirForMaintenance guards offline maintenance (import, compact): the
// listen address must be free and the data-dir lock must be obtainable. The
// lock catches a dashboard in another network namespace (e.g. a second
// container on the same volume) or on a different --addr.
func lockDataDirForMaintenance(dataDir, addr, action string) (func(), error) {
	if err := ensureDashboardStopped(addr, action); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	unlock, err := lockDataDir(dataDir)
	if errors.Is(err, errDataDirLocked) {
		return nil, fmt.Errorf("%s is in use by a running dashboard — stop it before %s", dataDir, action)
	}
	if err != nil {
		// Same policy as the dashboard: a filesystem without flock (some
		// NFS/FUSE mounts) must not block maintenance; the port check ran.
		fmt.Printf("warning: cannot lock data directory (%v); relying on the listen-port check\n", err)
		return func() {}, nil
	}
	return unlock, nil
}

// runCompactDB removes the stored history of metrics that are no longer
// recorded and rewrites the database into a compact file. Must be run while
// the dashboard is stopped. The compact copy is built next to the original
// (VACUUM INTO) and swapped in afterwards, so it needs free space only about
// the size of the result, nothing goes to /tmp, and a failure leaves the
// original untouched.
func runCompactDB(dataDir, addr string) error {
	// Check first, so a mistyped --data-dir doesn't get created.
	dbPath := filepath.Join(dataDir, "dashboard.db")
	orig, err := os.Stat(dbPath)
	if err != nil {
		return fmt.Errorf("no dashboard database in %s: %w", dataDir, err)
	}
	unlock, err := lockDataDirForMaintenance(dataDir, addr, "compacting")
	if err != nil {
		return err
	}
	defer unlock()

	before, err := dbFileSize(dbPath)
	if err != nil {
		return err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()

	fmt.Printf("Compacting %s (%s) — this can take a while on a large database...\n", dbPath, humanBytes(before))
	// Drop the unused index first: every delete below then has one index
	// fewer to update.
	if err := db.DropUnusedIndexes(); err != nil {
		return err
	}
	removed, err := store.NewMetricsStore(db).PurgeUnusedHistory(context.Background(), false)
	if err != nil {
		return err
	}
	fmt.Printf("Removed %d rows of metrics that are no longer recorded. Writing compact copy...\n", removed)

	tmpPath := dbPath + ".compact"
	_ = os.Remove(tmpPath) // leftover from an interrupted run
	if err := db.SnapshotTo(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0600); err != nil { // holds the same secrets as the original
		_ = os.Remove(tmpPath)
		return err
	}
	// Keep the original owner: run as root (`sudo` without `-u klever`), the
	// new file would otherwise be root-owned and the service could no longer
	// open its database.
	if err := copyOwner(tmpPath, orig); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("keep database owner: %w", err)
	}
	// Fold the WAL into the original before closing, and refuse if another
	// connection (e.g. a concurrent --export-backup) blocks that: Close would
	// then silently skip the checkpoint, and a crash between removing the WAL
	// and the swap below would lose its committed data.
	var busy, walPages, ckptPages int
	if err := db.SQL().QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &walPages, &ckptPages); err != nil || busy != 0 {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("database is in use by another process (checkpoint busy=%d, err=%v) — close it and retry", busy, err)
	}
	closed = true
	if err := db.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close database: %w", err)
	}
	// Drop WAL/SHM before the swap: a stale WAL next to the new file would be
	// replayed into it. (Empty after the TRUNCATE checkpoint above.)
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("remove %s: %w", dbPath+suffix, err)
		}
	}
	if err := os.Rename(tmpPath, dbPath); err != nil {
		return fmt.Errorf("swap in compact database: %w", err)
	}
	syncDir(dataDir) // make the rename durable

	after, err := dbFileSize(dbPath)
	if err != nil {
		return err
	}
	fmt.Printf("Done: %s → %s\n", humanBytes(before), humanBytes(after))
	return nil
}

// dbFileSize is the size of the database including its WAL.
func dbFileSize(dbPath string) (int64, error) {
	st, err := os.Stat(dbPath)
	if err != nil {
		return 0, err
	}
	size := st.Size()
	if wal, err := os.Stat(dbPath + "-wal"); err == nil {
		size += wal.Size()
	}
	return size, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// cleanupBackupStaging removes leftover plaintext staging files from an export
// or import that was interrupted (crash/kill) in a previous run.
func cleanupBackupStaging(dataDir string) {
	for _, prefix := range backup.StagePrefixes {
		matches, _ := filepath.Glob(filepath.Join(dataDir, prefix+"*"))
		for _, m := range matches {
			if err := os.RemoveAll(m); err == nil {
				fmt.Printf("removed stale backup staging file %s\n", m)
			}
		}
	}
}
