package backup

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CTJaeger/KleverNodeHub/internal/crypto"
	"github.com/CTJaeger/KleverNodeHub/internal/models"
	"github.com/CTJaeger/KleverNodeHub/internal/store"
)

const testPass = "correct horse battery"

// newSource builds a populated dashboard data dir: DB (settings, a server, a
// node, metrics) plus a CA encrypted with the DB's encryption key.
func newSource(t *testing.T) (*store.DB, string, *crypto.CA) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	key := bytes.Repeat([]byte{7}, 32)
	settings := store.NewSettingsStore(db)
	_ = settings.Set("encryption_key", hex.EncodeToString(key))
	_ = settings.Set("validator_elections", `{"history":{"2026-09":{"bls1":12}}}`)
	_ = settings.Set("dashboard_name", "old-hub")

	ca, err := crypto.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	caDir := filepath.Join(dir, "ca")
	if err := ca.SaveToDir(caDir, key); err != nil {
		t.Fatal(err)
	}

	pub, _, _ := crypto.GenerateEd25519KeyPair()
	cert, _ := ca.IssueAgentCertificate(pub, "srv-1")
	if err := store.NewServerStore(db).Create(&models.Server{ID: "srv-1", Name: "s", Hostname: "h", IPAddress: "1.1.1.1", Status: "online", Certificate: cert}); err != nil {
		t.Fatal(err)
	}
	return db, caDir, ca
}

func TestExportImportRoundTrip(t *testing.T) {
	db, caDir, ca := newSource(t)

	var buf bytes.Buffer
	m, err := Export(db, caDir, testPass, ExportOptions{}, &buf)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if m.Servers != 1 || m.IncludesMetrics {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if bytes.Contains(buf.Bytes(), []byte("old-hub")) || bytes.Contains(buf.Bytes(), []byte("SQLite format")) {
		t.Fatal("backup contains plaintext")
	}

	dst := t.TempDir()
	if _, err := Import(bytes.NewReader(buf.Bytes()), testPass, dst, false); err != nil {
		t.Fatalf("import: %v", err)
	}

	ndb, err := store.Open(filepath.Join(dst, "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ndb.Close() }()
	got, _ := store.NewSettingsStore(ndb).Get("validator_elections")
	if !strings.Contains(got, `"bls1":12`) {
		t.Fatalf("election history lost: %q", got)
	}
	if srv, err := store.NewServerStore(ndb).GetByID("srv-1"); err != nil || len(srv.Certificate) == 0 {
		t.Fatalf("server lost: %v", err)
	}
	keyHex, _ := store.NewSettingsStore(ndb).Get("encryption_key")
	key, _ := hex.DecodeString(keyHex)
	rca, err := crypto.LoadCAFromDir(filepath.Join(dst, "ca"), key)
	if err != nil {
		t.Fatalf("restored CA unusable: %v", err)
	}
	if !bytes.Equal(rca.CertPEM, ca.CertPEM) {
		t.Fatal("restored CA differs")
	}
}

func TestImportWrongPassphraseAndTamper(t *testing.T) {
	db, caDir, _ := newSource(t)
	var buf bytes.Buffer
	if _, err := Export(db, caDir, testPass, ExportOptions{}, &buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	if _, err := Import(bytes.NewReader(data), "wrong passphrase!!", t.TempDir(), false); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("wrong passphrase: got %v", err)
	}
	bad := append([]byte(nil), data...)
	bad[len(bad)/2] ^= 0xff
	if _, err := Import(bytes.NewReader(bad), testPass, t.TempDir(), false); err == nil {
		t.Fatal("tampered backup accepted")
	}
	if _, err := Import(bytes.NewReader(data[:len(data)-40]), testPass, t.TempDir(), false); err == nil {
		t.Fatal("truncated backup accepted")
	}
	if _, err := Import(strings.NewReader("not a backup file at all, definitely"), testPass, t.TempDir(), false); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestImportRefusesOverwriteUnlessForced(t *testing.T) {
	db, caDir, _ := newSource(t)
	var buf bytes.Buffer
	if _, err := Export(db, caDir, testPass, ExportOptions{}, &buf); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if _, err := Import(bytes.NewReader(buf.Bytes()), testPass, dst, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(bytes.NewReader(buf.Bytes()), testPass, dst, false); err == nil {
		t.Fatal("second import overwrote existing data without --force")
	}
	if _, err := Import(bytes.NewReader(buf.Bytes()), testPass, dst, true); err != nil {
		t.Fatalf("forced import failed: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dst, "dashboard.db.bak-*"))
	if len(matches) == 0 {
		t.Fatal("existing database was not moved aside")
	}
	if _, err := os.Stat(filepath.Join(dst, "ca")); err != nil {
		t.Fatal("CA missing after forced import")
	}
}

func TestExportRejectsShortPassphrase(t *testing.T) {
	db, caDir, _ := newSource(t)
	if _, err := Export(db, caDir, "short", ExportOptions{}, &bytes.Buffer{}); err == nil {
		t.Fatal("short passphrase accepted")
	}
}

func TestImportRegeneratesJWTKey(t *testing.T) {
	db, caDir, _ := newSource(t)
	_ = store.NewSettingsStore(db).Set("jwt_signing_key", "deadbeef")
	var buf bytes.Buffer
	if _, err := Export(db, caDir, testPass, ExportOptions{}, &buf); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if _, err := Import(bytes.NewReader(buf.Bytes()), testPass, dst, false); err != nil {
		t.Fatal(err)
	}
	ndb, err := store.Open(filepath.Join(dst, "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ndb.Close() }()
	if v, _ := store.NewSettingsStore(ndb).Get("jwt_signing_key"); v != "" {
		t.Fatalf("old JWT signing key survived the import: %q", v)
	}
	// Everything else (election history) must still be there.
	if v, _ := store.NewSettingsStore(ndb).Get("validator_elections"); v == "" {
		t.Fatal("election history lost")
	}
}

func TestImportRefusesStrayWALWithoutForce(t *testing.T) {
	db, caDir, _ := newSource(t)
	var buf bytes.Buffer
	if _, err := Export(db, caDir, testPass, ExportOptions{}, &buf); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "dashboard.db-wal"), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(bytes.NewReader(buf.Bytes()), testPass, dst, false); err == nil {
		t.Fatal("import ignored a stray WAL file")
	}
	m, err := Import(bytes.NewReader(buf.Bytes()), testPass, dst, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.MovedAside) != 1 {
		t.Fatalf("expected the stray WAL to be moved aside, got %v", m.MovedAside)
	}
	if _, err := os.Stat(filepath.Join(dst, "dashboard.db-wal")); err == nil {
		t.Fatal("stale WAL still present next to restored DB")
	}
}

func TestImportDetectsTruncationAfterTarEnd(t *testing.T) {
	db, caDir, _ := newSource(t)
	var buf bytes.Buffer
	if _, err := Export(db, caDir, testPass, ExportOptions{}, &buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	// Chop the last (final-flag) chunk off at a chunk boundary: header(32)
	// then [len|chunk]... — drop exactly the final chunk.
	off := 32
	last := off
	for off < len(data) {
		last = off
		n := int(data[off])<<24 | int(data[off+1])<<16 | int(data[off+2])<<8 | int(data[off+3])
		off += 4 + n
	}
	if _, err := Import(bytes.NewReader(data[:last]), testPass, t.TempDir(), false); err == nil {
		t.Fatal("archive missing its final chunk was accepted")
	}
}

func TestConcurrentExportRejected(t *testing.T) {
	db, caDir, _ := newSource(t)
	exportMu.Lock()
	defer exportMu.Unlock()
	if _, err := Export(db, caDir, testPass, ExportOptions{}, &bytes.Buffer{}); !errors.Is(err, ErrExportInProgress) {
		t.Fatalf("expected ErrExportInProgress, got %v", err)
	}
}
