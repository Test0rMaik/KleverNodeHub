package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// LatestSchemaVersion is the schema version this binary migrates to.
func LatestSchemaVersion() int { return len(migrations) }

// SchemaVersion returns the schema version currently applied to the database.
func (d *DB) SchemaVersion() (int, error) {
	var v int
	if err := d.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}

// OpenNoMigrate opens an existing database WITHOUT changing it (no WAL pragma,
// no migrations). Used by the CLI export so a newer binary never alters the
// database of a still-running older dashboard.
func OpenNoMigrate(dbPath string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &DB{db: sqlDB, path: dbPath}, nil
}

// SnapshotTo writes a consistent copy of the database to path using VACUUM INTO.
// path must not exist.
//
// It runs on its OWN connection to the database file, not on the shared pool:
// the pool is limited to one connection (SetMaxOpenConns(1)), so a VACUUM INTO
// there would block every other query — heartbeats included — for the whole
// copy. In WAL mode a second reader neither blocks nor is blocked by the
// dashboard's writes.
func (d *DB) SnapshotTo(path string) error {
	if d.path == "" {
		return fmt.Errorf("snapshot database: unknown database path")
	}
	src, err := sql.Open("sqlite", d.path)
	if err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	defer func() { _ = src.Close() }()
	src.SetMaxOpenConns(1)
	if _, err := src.Exec("PRAGMA busy_timeout=30000"); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := src.Exec("VACUUM INTO " + quoted); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	return nil
}

// PurgeMetrics deletes all time-series metric tables (recent, archive, system)
// and reclaims the space. Used to produce a small backup that keeps settings,
// servers, nodes, alerts and election history but not the metric history.
func (d *DB) PurgeMetrics() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range []string{"metrics_recent", "metrics_archive", "system_metrics"} {
		if _, err := d.db.Exec("DELETE FROM " + t); err != nil {
			return fmt.Errorf("purge %s: %w", t, err)
		}
	}
	if _, err := d.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}

// Counts returns the number of registered servers and nodes (for summaries).
func (d *DB) Counts() (servers, nodes int, err error) {
	if err = d.db.QueryRow("SELECT COUNT(*) FROM servers").Scan(&servers); err != nil {
		return 0, 0, fmt.Errorf("count servers: %w", err)
	}
	if err = d.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodes); err != nil {
		return 0, 0, fmt.Errorf("count nodes: %w", err)
	}
	return servers, nodes, nil
}
