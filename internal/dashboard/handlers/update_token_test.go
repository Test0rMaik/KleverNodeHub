package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDownloadToken_SingleUseAndBoundToServer(t *testing.T) {
	h := &UpdateHandler{downloadTokens: make(map[string]downloadToken)}

	tok := h.newDownloadToken("srv-1")
	if tok == "" {
		t.Fatal("empty token")
	}
	if h.consumeDownloadToken(tok, "srv-2") {
		t.Fatal("token accepted for a different server")
	}
	// The failed attempt above already consumed it (single use).
	if h.consumeDownloadToken(tok, "srv-1") {
		t.Fatal("token reusable after a use")
	}

	tok = h.newDownloadToken("srv-1")
	if !h.consumeDownloadToken(tok, "srv-1") {
		t.Fatal("valid token rejected")
	}
	if h.consumeDownloadToken(tok, "srv-1") {
		t.Fatal("token accepted twice")
	}
}

func TestDownloadToken_Expired(t *testing.T) {
	h := &UpdateHandler{downloadTokens: map[string]downloadToken{
		"old": {serverID: "srv-1", expires: time.Now().Add(-time.Second)},
	}}
	if h.consumeDownloadToken("old", "srv-1") {
		t.Fatal("expired token accepted")
	}
}

func TestHandleAgentBinary_RequiresToken(t *testing.T) {
	h := &UpdateHandler{downloadTokens: make(map[string]downloadToken)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent/binary/{server_id}", h.HandleAgentBinary)

	for _, target := range []string{"/api/agent/binary/srv-1", "/api/agent/binary/srv-1?token=bogus"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d, want 404", target, rec.Code)
		}
	}
}
