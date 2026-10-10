// Package backup exports and imports a complete Klever Node Hub instance
// (database + certificate authority) as a single passphrase-encrypted file, so
// the hub can be moved to a new server without losing settings, registered
// servers/nodes, alert rules, notification channels, or election history.
//
// The archive holds a consistent snapshot of dashboard.db (which contains the
// settings KV store — incl. the election history, the encryption/JWT keys and
// passkeys — plus servers, nodes, alerts and metrics) and the ca/ directory.
// Agents keep working against the restored hub because they trust the same CA
// and their certificates/server IDs are preserved.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CTJaeger/KleverNodeHub/internal/crypto"
	"github.com/CTJaeger/KleverNodeHub/internal/store"
	"github.com/CTJaeger/KleverNodeHub/internal/version"
)

const (
	formatVersion = 1
	appName       = "klever-node-hub"

	manifestName = "manifest.json"
	dbName       = "dashboard.db"
	caPrefix     = "ca/"

	// maxEntryBytes bounds a single extracted file (zip-bomb guard).
	maxEntryBytes = 8 << 30
)

// Manifest describes a backup archive.
type Manifest struct {
	Format          int    `json:"format"`
	App             string `json:"app"`
	AppVersion      string `json:"app_version"`
	CreatedAt       string `json:"created_at"`
	SchemaVersion   int    `json:"schema_version"`
	IncludesMetrics bool   `json:"includes_metrics"`
	Servers         int    `json:"servers"`
	Nodes           int    `json:"nodes"`

	// MovedAside lists files/dirs an Import with replace=true renamed to
	// *.bak-<time> (they still contain the old secrets). Not part of the archive.
	MovedAside []string `json:"-"`
}

// ExportOptions control what goes into a backup.
type ExportOptions struct {
	// IncludeMetrics keeps the metric history tables (can be large). Settings,
	// servers, nodes, alerts and election history are always included.
	IncludeMetrics bool
}

// ErrExportInProgress is returned when another export is already running.
var ErrExportInProgress = errors.New("a backup export is already running")

var exportMu sync.Mutex

// StagePrefixes are the names of temporary staging directories/files created in
// the data dir; callers may delete leftovers (from a crash) at startup.
var StagePrefixes = []string{".export-", ".import-"}

// Export writes an encrypted backup of the live database and CA directory to w.
// The plaintext snapshot is staged inside the data dir (the parent of caDir, 0700),
// never in the system temp dir.
func Export(db *store.DB, caDir, passphrase string, opts ExportOptions, w io.Writer) (*Manifest, error) {
	if len(passphrase) < MinPassphraseLen {
		return nil, fmt.Errorf("passphrase must be at least %d characters", MinPassphraseLen)
	}
	if !exportMu.TryLock() {
		return nil, ErrExportInProgress
	}
	defer exportMu.Unlock()

	tmp, err := os.MkdirTemp(filepath.Dir(caDir), ".export-")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	snapPath := filepath.Join(tmp, dbName)
	if err := db.SnapshotTo(snapPath); err != nil {
		return nil, err
	}

	// Work on the snapshot (never the live DB) when stripping metrics.
	snap, err := store.Open(snapPath)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	if !opts.IncludeMetrics {
		if err := snap.PurgeMetrics(); err != nil {
			_ = snap.Close()
			return nil, err
		}
	}
	servers, nodes, err := snap.Counts()
	if err != nil {
		_ = snap.Close()
		return nil, err
	}
	schema, err := snap.SchemaVersion()
	if err != nil {
		_ = snap.Close()
		return nil, err
	}
	// Close checkpoints the WAL so dashboard.db is self-contained.
	if err := snap.Close(); err != nil {
		return nil, fmt.Errorf("close snapshot: %w", err)
	}

	if st, err := os.Stat(snapPath); err == nil && st.Size() > maxEntryBytes {
		return nil, fmt.Errorf("database snapshot is %d bytes, over the %d byte import limit — export without metrics history", st.Size(), int64(maxEntryBytes))
	}

	m := &Manifest{
		Format:          formatVersion,
		App:             appName,
		AppVersion:      version.Version,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		SchemaVersion:   schema,
		IncludesMetrics: opts.IncludeMetrics,
		Servers:         servers,
		Nodes:           nodes,
	}

	enc, err := newEncryptWriter(w, passphrase)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)

	mj, _ := json.MarshalIndent(m, "", "  ")
	if err := addBytes(tw, manifestName, mj, 0600); err != nil {
		return nil, err
	}
	if err := addFile(tw, dbName, snapPath, 0600); err != nil {
		return nil, err
	}
	for _, name := range []string{"ca.crt", "ca.key.enc"} {
		if err := addFile(tw, caPrefix+name, filepath.Join(caDir, name), 0600); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return m, nil
}

func addBytes(tw *tar.Writer, name string, data []byte, mode int64) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func addFile(tw *tar.Writer, name, path string, mode int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// Import restores a backup into dataDir. If dataDir already contains a
// dashboard database, Import refuses unless replace is true, in which case the
// existing database and CA are moved aside to *.bak-<timestamp> (never deleted).
// The dashboard must not be running against dataDir during an import.
func Import(r io.Reader, passphrase, dataDir string, replace bool) (*Manifest, error) {
	dec, err := newDecryptReader(r, passphrase)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		if errors.Is(err, ErrBadPassphrase) {
			return nil, err
		}
		return nil, ErrBadPassphrase
	}
	tr := tar.NewReader(gz)

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	stage, err := os.MkdirTemp(dataDir, ".import-")
	if err != nil {
		return nil, fmt.Errorf("staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.MkdirAll(filepath.Join(stage, "ca"), 0700); err != nil {
		return nil, err
	}

	var m *Manifest
	seenDB := false
	caFiles := 0
	seenCA := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(err, ErrBadPassphrase) {
				return nil, err
			}
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Only exact, known names are extracted — never a path from the archive.
		var dst string
		switch hdr.Name {
		case manifestName:
			data, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, err
			}
			m = &Manifest{}
			if err := json.Unmarshal(data, m); err != nil {
				return nil, fmt.Errorf("parse manifest: %w", err)
			}
			continue
		case dbName:
			dst, seenDB = filepath.Join(stage, dbName), true
		case caPrefix + "ca.crt", caPrefix + "ca.key.enc":
			if seenCA[hdr.Name] {
				return nil, errors.New("backup contains a duplicate CA file")
			}
			seenCA[hdr.Name] = true
			dst = filepath.Join(stage, hdr.Name)
			caFiles++
		default:
			continue
		}
		if err := writeLimited(dst, tr); err != nil {
			return nil, err
		}
	}

	// Drain the rest of the stream so the gzip CRC and the final authenticated
	// chunk are verified — this is what actually detects a truncated file.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return nil, fmt.Errorf("verify archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("verify archive: %w", err)
	}
	if !dec.done {
		return nil, fmt.Errorf("%w (truncated)", ErrBadPassphrase)
	}

	if m == nil || m.App != appName {
		return nil, errors.New("archive has no valid manifest (not a Klever Node Hub backup)")
	}
	if m.Format != formatVersion {
		return nil, fmt.Errorf("unsupported backup format %d (this build understands %d)", m.Format, formatVersion)
	}
	if !seenDB || caFiles != 2 {
		return nil, errors.New("backup is incomplete (missing database or CA files)")
	}
	if m.SchemaVersion > store.LatestSchemaVersion() {
		return nil, fmt.Errorf("backup was made by a newer version (schema %d > %d) — upgrade this dashboard first", m.SchemaVersion, store.LatestSchemaVersion())
	}

	// Validate by opening the staged DB (also migrates older backups forward).
	check, err := store.Open(filepath.Join(stage, dbName))
	if err != nil {
		return nil, fmt.Errorf("backup database is unusable: %w", err)
	}
	if _, _, err := check.Counts(); err != nil {
		_ = check.Close()
		return nil, fmt.Errorf("backup database is unusable: %w", err)
	}
	// Trust the database itself, not just the manifest: Open silently accepts a
	// DB from a newer version (migrations only run forward).
	if v, err := check.SchemaVersion(); err != nil || v > store.LatestSchemaVersion() {
		_ = check.Close()
		return nil, fmt.Errorf("backup database schema is newer than this dashboard supports — upgrade this dashboard first")
	}
	// The restored CA must decrypt with the restored encryption key, otherwise
	// every agent would be locked out after the move.
	keyHex, err := store.NewSettingsStore(check).Get("encryption_key")
	if err != nil {
		_ = check.Close()
		return nil, err
	}
	encKey, err := hex.DecodeString(keyHex)
	if err != nil || len(encKey) == 0 {
		_ = check.Close()
		return nil, errors.New("backup has no usable encryption key")
	}
	if _, err := crypto.LoadCAFromDir(filepath.Join(stage, "ca"), encKey); err != nil {
		_ = check.Close()
		return nil, fmt.Errorf("backup CA cannot be loaded: %w", err)
	}
	// Force a fresh JWT signing key on the new host: stateless tokens issued by
	// the old host (or leaked from it) must not stay valid. It is regenerated at
	// startup; the only effect is one re-login.
	if err := store.NewSettingsStore(check).Delete("jwt_signing_key"); err != nil {
		_ = check.Close()
		return nil, err
	}
	if err := check.Close(); err != nil {
		return nil, err
	}

	finalDB := filepath.Join(dataDir, dbName)
	finalCA := filepath.Join(dataDir, "ca")
	stamp := time.Now().Format("20060102-150405")

	// Anything already there — including stray -wal/-shm files that SQLite
	// would replay into the restored DB — counts as existing data.
	existing := []string{}
	for _, p := range []string{finalDB, finalDB + "-wal", finalDB + "-shm", finalCA} {
		if _, err := os.Lstat(p); err == nil {
			existing = append(existing, p)
		}
	}
	var moved []string
	rollback := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			orig := moved[i][:len(moved[i])-len(".bak-"+stamp)]
			_ = os.Rename(moved[i], orig)
		}
	}
	if len(existing) > 0 {
		if !replace {
			return nil, fmt.Errorf("%s already exists — refusing to overwrite (use --force to move existing data aside to .bak-%s copies)", existing[0], stamp)
		}
		for _, p := range existing {
			if err := os.Rename(p, p+".bak-"+stamp); err != nil {
				rollback()
				return nil, fmt.Errorf("move aside %s: %w", p, err)
			}
			moved = append(moved, p+".bak-"+stamp)
		}
	}
	if err := os.Rename(filepath.Join(stage, "ca"), finalCA); err != nil {
		rollback()
		return nil, fmt.Errorf("install CA: %w", err)
	}
	if err := os.Rename(filepath.Join(stage, dbName), finalDB); err != nil {
		_ = os.RemoveAll(finalCA)
		rollback()
		return nil, fmt.Errorf("install database: %w", err)
	}
	m.MovedAside = moved
	return m, nil
}

func writeLimited(dst string, r io.Reader) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, maxEntryBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxEntryBytes {
		return errors.New("archive entry too large")
	}
	return nil
}
