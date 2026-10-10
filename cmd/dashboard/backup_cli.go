package main

import (
	"bytes"
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
	// Refuse to swap files under a live dashboard: if the listen address is
	// taken, a hub is (very likely) still running against this data dir.
	if ln, err := net.Listen("tcp", addr); err != nil {
		return fmt.Errorf("cannot bind %s (%v) — a dashboard seems to be running; stop it before importing", addr, err)
	} else {
		_ = ln.Close()
	}
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
