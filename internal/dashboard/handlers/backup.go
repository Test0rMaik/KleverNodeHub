package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/CTJaeger/KleverNodeHub/internal/auth"
	"github.com/CTJaeger/KleverNodeHub/internal/backup"
	"github.com/CTJaeger/KleverNodeHub/internal/store"
)

// exportWriteTimeout bounds building and, separately, sending a backup.
const exportWriteTimeout = 15 * time.Minute

// exportBackup is backup.Export; a variable so tests can simulate a slow export.
var exportBackup = backup.Export

// BackupHandler exports an encrypted backup of the whole hub (database + CA)
// for migrating to another server. Import is deliberately CLI-only
// (`--import-backup`), because it must run while the dashboard is stopped.
type BackupHandler struct {
	db       *store.DB
	caDir    string
	password *auth.PasswordManager
	limiter  *auth.RateLimiter
	// recentAuth reports whether the caller logged in recently; required for
	// an export on hubs without a password (see HandleExport).
	recentAuth func(*http.Request) bool
}

// SetRecentAuthCheck sets the recent-login check used for passwordless hubs.
func (h *BackupHandler) SetRecentAuthCheck(fn func(*http.Request) bool) { h.recentAuth = fn }

// NewBackupHandler creates a BackupHandler. password/limiter implement the
// fresh re-authentication required before a backup is handed out.
func NewBackupHandler(db *store.DB, caDir string, password *auth.PasswordManager, limiter *auth.RateLimiter) *BackupHandler {
	return &BackupHandler{db: db, caDir: caDir, password: password, limiter: limiter}
}

// HandleExport handles POST /api/system/backup/export
// Body: {"passphrase": "...", "current_password": "...", "include_metrics": false}.
// The backup contains every secret of the hub, so a valid session alone is not
// enough: the admin password is re-verified (when one is set). The passphrase
// travels in the body (never the URL) and is not stored. Responds with the
// encrypted file.
func (h *BackupHandler) HandleExport(w http.ResponseWriter, r *http.Request) {
	// Building the backup (DB snapshot, metric purge, encryption) can take
	// minutes on a large hub, longer than the server-wide 30s write timeout.
	// The deadline must be extended *before* that work: over HTTP/2 the
	// timeout resets the stream when it fires, and a later extension can't
	// revive it — the browser just sees a failed request.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(exportWriteTimeout))
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Passphrase      string `json:"passphrase"`
		CurrentPassword string `json:"current_password"`
		IncludeMetrics  bool   `json:"include_metrics"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	ip := extractIP(r)
	// Without a password to re-check, a recent login stands in for it: the
	// backup holds every secret of the hub, so an old session must not get it.
	if (h.password == nil || !h.password.HasPassword()) && h.recentAuth != nil && !h.recentAuth(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":  "for security, log in again before exporting a backup (your last login is older than 15 minutes)",
			"reauth": true,
		})
		return
	}
	if h.password != nil && h.password.HasPassword() {
		if h.limiter != nil && !h.limiter.Allow(ip) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed attempts, try again later"})
			return
		}
		if !h.password.Verify(req.CurrentPassword) {
			if h.limiter != nil {
				h.limiter.RecordFailure(ip)
			}
			log.Printf("backup export DENIED from %s: wrong current password", ip)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
			return
		}
		if h.limiter != nil {
			h.limiter.Reset(ip)
		}
	}
	if len(req.Passphrase) < backup.MinPassphraseLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("passphrase must be at least %d characters", backup.MinPassphraseLen)})
		return
	}

	// Build into a temp file first so a failure can still return a clean error
	// (and the response can be served with a proper Content-Length).
	// Staged in the data dir (0700), not the system temp dir.
	tmp, err := os.CreateTemp(filepath.Dir(h.caDir), ".export-*.knhbak")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot create temp file"})
		return
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()

	m, err := exportBackup(h.db, h.caDir, req.Passphrase, backup.ExportOptions{IncludeMetrics: req.IncludeMetrics}, tmp)
	if errors.Is(err, backup.ErrExportInProgress) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a backup export is already running"})
		return
	}
	if err != nil {
		log.Printf("backup export failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "backup failed — see dashboard log"})
		return
	}
	log.Printf("backup exported by %s (%d servers, %d nodes, metrics: %v)", ip, m.Servers, m.Nodes, m.IncludesMetrics)

	// Fresh window for sending the file itself.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(exportWriteTimeout))
	name := fmt.Sprintf("klever-node-hub-backup-%s.knhbak", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, time.Time{}, tmp)
}
