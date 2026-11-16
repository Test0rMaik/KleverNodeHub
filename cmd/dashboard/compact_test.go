//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CTJaeger/KleverNodeHub/internal/store"
)

func TestLockDataDir_Exclusive(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockDataDir(dir); !errors.Is(err, errDataDirLocked) {
		t.Fatalf("second lock: err = %v, want errDataDirLocked", err)
	}
	unlock()
	unlock2, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("relock after release: %v", err)
	}
	unlock2()
}

// Maintenance must refuse while a dashboard holds the data dir, even when the
// listen address is free (dashboard in another container / on another --addr).
func TestCompactDB_RefusesWhileDataDirLocked(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	unlock, err := lockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	err = runCompactDB(dir, "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "in use by a running dashboard") {
		t.Fatalf("err = %v, want refusal", err)
	}
}

func TestCompactDB_ShrinksAndKeepsData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dashboard.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NewSettingsStore(db).Set("dashboard_name", "keep-me"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 22000; i++ { // > one purge window
		if _, err := tx.Exec(`INSERT INTO metrics_recent (node_id, server_id, metric_name, metric_value, collected_at)
			VALUES ('n', 's', 'klv_total_supply', ?, ?)`, i, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO metrics_recent (node_id, server_id, metric_name, metric_value, collected_at)
		VALUES ('n', 's', 'klv_nonce', 7, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := dbFileSize(dbPath)

	if err := runCompactDB(dir, "127.0.0.1:0"); err != nil {
		t.Fatalf("compact: %v", err)
	}

	after, _ := dbFileSize(dbPath)
	if after >= before {
		t.Errorf("size %d → %d, want smaller", before, after)
	}
	if _, err := os.Stat(dbPath + ".compact"); !os.IsNotExist(err) {
		t.Error("compact temp file left behind")
	}
	if st, err := os.Stat(dbPath); err != nil || st.Mode().Perm() != 0600 {
		t.Errorf("db mode = %v (err %v), want 0600", st.Mode().Perm(), err)
	}

	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if v, _ := store.NewSettingsStore(db).Get("dashboard_name"); v != "keep-me" {
		t.Errorf("setting lost after compact: %q", v)
	}
	var kept, junk int
	_ = db.SQL().QueryRow(`SELECT COUNT(*) FROM metrics_recent WHERE metric_name = 'klv_nonce'`).Scan(&kept)
	_ = db.SQL().QueryRow(`SELECT COUNT(*) FROM metrics_recent WHERE metric_name = 'klv_total_supply'`).Scan(&junk)
	if kept != 1 || junk != 0 {
		t.Errorf("kept=%d junk=%d, want 1/0", kept, junk)
	}
}

// A reader holding a WAL snapshot blocks the TRUNCATE checkpoint; compact must
// refuse (and leave the original untouched) rather than drop a WAL that may
// still hold committed data.
func TestCompactDB_RefusesWhenAnotherConnectionBlocksCheckpoint(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dashboard.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := store.NewSettingsStore(db).Set("dashboard_name", "keep-me"); err != nil {
		t.Fatal(err)
	}
	reader, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback() }()
	var n int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		t.Fatal(err)
	}

	err = runCompactDB(dir, "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "in use by another process") {
		t.Fatalf("err = %v, want refusal", err)
	}
	if _, err := os.Stat(dbPath + ".compact"); !os.IsNotExist(err) {
		t.Error("compact temp file left behind")
	}
}
