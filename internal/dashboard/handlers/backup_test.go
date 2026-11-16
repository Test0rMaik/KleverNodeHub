package handlers

import (
	"bytes"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CTJaeger/KleverNodeHub/internal/auth"
	"github.com/CTJaeger/KleverNodeHub/internal/backup"
	"github.com/CTJaeger/KleverNodeHub/internal/crypto"
	"github.com/CTJaeger/KleverNodeHub/internal/store"
)

func newBackupHandlerFixture(t *testing.T) *BackupHandler {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key := bytes.Repeat([]byte{3}, 32)
	_ = store.NewSettingsStore(db).Set("encryption_key", hex.EncodeToString(key))
	ca, err := crypto.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	caDir := filepath.Join(dir, "ca")
	if err := ca.SaveToDir(caDir, key); err != nil {
		t.Fatal(err)
	}
	pm := auth.NewPasswordManager("")
	if _, err := pm.SetPassword("admin-password-123"); err != nil {
		t.Fatal(err)
	}
	return NewBackupHandler(db, caDir, pm, auth.NewRateLimiter(time.Minute, 3))
}

func postExport(h *BackupHandler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/backup/export", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.5:1234"
	h.HandleExport(rec, req)
	return rec
}

func TestBackupExport_RequiresCurrentPassword(t *testing.T) {
	h := newBackupHandlerFixture(t)

	if rec := postExport(h, `{"passphrase":"a-long-backup-passphrase"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing password: got %d, want 401", rec.Code)
	}
	if rec := postExport(h, `{"passphrase":"a-long-backup-passphrase","current_password":"wrong"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d, want 401", rec.Code)
	}
	// Repeated failures trip the rate limiter.
	var last int
	for i := 0; i < 5; i++ {
		last = postExport(h, `{"passphrase":"a-long-backup-passphrase","current_password":"nope"}`).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
}

func TestBackupExport_Success(t *testing.T) {
	h := newBackupHandlerFixture(t)
	rec := postExport(h, `{"passphrase":"a-long-backup-passphrase","current_password":"admin-password-123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), "KNHBAK01") {
		t.Fatal("response is not an encrypted backup container")
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), ".knhbak") {
		t.Fatalf("bad Content-Disposition: %q", rec.Header().Get("Content-Disposition"))
	}
	if rec := postExport(h, `{"passphrase":"short","current_password":"admin-password-123"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("short passphrase: got %d, want 400", rec.Code)
	}
}

// An export that takes longer than the server's WriteTimeout must still be
// delivered. Over HTTP/2 (what browsers use against the TLS dashboard) the
// write timeout resets the stream when it fires, so the deadline has to be
// extended before the export runs, not after.
func TestBackupExport_SlowExportOutlivesWriteTimeout(t *testing.T) {
	h := newBackupHandlerFixture(t)
	orig := exportBackup
	exportBackup = func(db *store.DB, caDir, pass string, opts backup.ExportOptions, w io.Writer) (*backup.Manifest, error) {
		time.Sleep(600 * time.Millisecond)
		return orig(db, caDir, pass, opts, w)
	}
	t.Cleanup(func() { exportBackup = orig })

	srv := httptest.NewUnstartedServer(http.HandlerFunc(h.HandleExport))
	srv.EnableHTTP2 = true
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.StartTLS()
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL, "application/json",
		strings.NewReader(`{"passphrase":"a-long-backup-passphrase","current_password":"admin-password-123"}`))
	if err != nil {
		t.Fatalf("request failed (write timeout hit?): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ProtoMajor != 2 {
		t.Fatalf("proto = %s, want HTTP/2", resp.Proto)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body (stream reset?): %v", err)
	}
	if resp.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("status %d, %d bytes", resp.StatusCode, len(body))
	}
}
