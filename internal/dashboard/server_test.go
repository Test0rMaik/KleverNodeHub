package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kvcrypto "github.com/CTJaeger/KleverNodeHub/internal/crypto"
)

func TestServerSetupRoutes(t *testing.T) {
	srv := NewServer(&ServerConfig{Addr: ":9443"})
	if err := srv.SetupRoutes(); err != nil {
		t.Fatalf("SetupRoutes: %v", err)
	}

	// Test login page
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /login = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestServerSecurityHeaders(t *testing.T) {
	srv := NewServer(&ServerConfig{Addr: ":9443"})
	_ = srv.SetupRoutes()

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	headers := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"X-XSS-Protection":       "1; mode=block",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}

	for name, expected := range headers {
		got := w.Header().Get(name)
		if got != expected {
			t.Errorf("Header %s = %q, want %q", name, got, expected)
		}
	}

	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("missing Content-Security-Policy header")
	}
}

func TestServerOverviewPage(t *testing.T) {
	srv := NewServer(&ServerConfig{Addr: ":9443"})
	_ = srv.SetupRoutes()

	req := httptest.NewRequest(http.MethodGet, "/overview", nil)
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /overview = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestServerInjectsSharedSidebar(t *testing.T) {
	srv := NewServer(&ServerConfig{Addr: ":9443"})
	_ = srv.SetupRoutes()

	// Every sidebar-bearing page must render the shared partial with the full
	// nav (incl. the links that used to go missing per-page), and no leftover
	// marker. This guards against the duplication bug that lost links.
	for _, path := range []string{"/overview", "/validators", "/docker-cleanup", "/alerts", "/slotinspector", "/batchconfig", "/settings"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.Mux().ServeHTTP(w, req)

		body := w.Body.String()
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, w.Code)
			continue
		}
		if strings.Contains(body, sidebarMarker) {
			t.Errorf("GET %s: sidebar marker not replaced", path)
		}
		for _, link := range []string{`href="/overview"`, `href="/validators"`, `href="/docker-cleanup"`, `href="/slotinspector"`, `href="/batchconfig"`, `href="/alerts"`, `href="/settings"`} {
			if !strings.Contains(body, link) {
				t.Errorf("GET %s: missing nav link %s", path, link)
			}
		}
	}

	// Login has no sidebar marker and must still render fine (no injection).
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), sidebarMarker) {
		t.Error("login page unexpectedly contains the sidebar marker")
	}
}

func TestServerNodePage(t *testing.T) {
	srv := NewServer(&ServerConfig{Addr: ":9443"})
	_ = srv.SetupRoutes()

	req := httptest.NewRequest(http.MethodGet, "/node/test-id", nil)
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /node/test-id = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestServerStaticAssets(t *testing.T) {
	srv := NewServer(&ServerConfig{Addr: ":9443"})
	_ = srv.SetupRoutes()

	req := httptest.NewRequest(http.MethodGet, "/static/css/style.css", nil)
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /static/css/style.css = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestGetTLSConfigWithCA(t *testing.T) {
	ca, err := kvcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	srv := NewServer(&ServerConfig{Addr: ":9443", CA: ca})
	tlsCfg, err := srv.getTLSConfig()
	if err != nil {
		t.Fatalf("getTLSConfig: %v", err)
	}

	if len(tlsCfg.Certificates) == 0 {
		t.Error("no certificates in TLS config")
	}
}
