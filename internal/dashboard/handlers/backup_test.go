package handlers

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CTJaeger/KleverNodeHub/internal/auth"
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
