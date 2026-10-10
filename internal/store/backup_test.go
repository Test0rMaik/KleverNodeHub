package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotTo_DoesNotNeedThePooledConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := NewSettingsStore(db).Set("k", "v"); err != nil {
		t.Fatal(err)
	}

	// Hold the dashboard's only pooled connection and the write lock: a snapshot
	// built on the shared pool would deadlock here.
	conn, err := db.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	db.mu.Lock()
	defer db.mu.Unlock()

	done := make(chan error, 1)
	snap := filepath.Join(t.TempDir(), "snap.db")
	go func() { done <- db.SnapshotTo(snap) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("snapshot blocked on the dashboard's pooled connection")
	}

	sdb, err := Open(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sdb.Close() }()
	if v, _ := NewSettingsStore(sdb).Get("k"); v != "v" {
		t.Fatalf("snapshot lost data: %q", v)
	}
}

func TestPurgeMetrics_KeepsEverythingElse(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_ = NewSettingsStore(db).Set("validator_elections", "keep-me")
	if _, err := db.db.Exec(`INSERT INTO metrics_recent (node_id, server_id, metric_name, metric_value, collected_at) VALUES ('n','s','m',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO system_metrics (server_id, collected_at) VALUES ('s',1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.PurgeMetrics(); err != nil {
		t.Fatal(err)
	}
	var n int
	for _, tbl := range []string{"metrics_recent", "metrics_archive", "system_metrics"} {
		if err := db.db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s not purged: n=%d err=%v", tbl, n, err)
		}
	}
	if v, _ := NewSettingsStore(db).Get("validator_elections"); v != "keep-me" {
		t.Fatal("settings were touched by PurgeMetrics")
	}
}
