package agent

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/CTJaeger/KleverNodeHub/internal/crypto"
	"github.com/CTJaeger/KleverNodeHub/internal/models"
)

// fakeDashboard serves /api/agent/register with the given status, signed by a fresh CA.
func fakeDashboard(t *testing.T, status int, serverID string) (url, fingerprint string) {
	t.Helper()
	ca, err := crypto.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := crypto.DashboardTLSConfig(ca)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ClientAuth = tls.RequestClientCert
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/ca" { // agents fetch the CA here since the TLS chain is leaf-only
			_, _ = w.Write(ca.CertPEM)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "invalid token", status)
			return
		}
		_ = json.NewEncoder(w).Encode(models.RegistrationResponse{ServerID: serverID, CertPEM: "cert", KeyPEM: "key", CACertPEM: string(ca.CertPEM)})
	}))
	srv.TLS = cfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL, crypto.Fingerprint(ca.Certificate)
}

func writeOldConfig(t *testing.T, dir string) {
	t.Helper()
	old := Config{ServerID: "srv-old", DashboardURL: "https://old:9443", CertPEM: "old",
		AllowUnsignedUpdates: true, AllowedDataRoots: []string{"/opt/klever"}, AgentPort: 7777}
	data, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, configFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRegister_ReplacesExistingRegistration(t *testing.T) {
	dir := t.TempDir()
	writeOldConfig(t, dir)
	url, fp := fakeDashboard(t, http.StatusOK, "srv-new")

	ag := New(dir)
	if err := ag.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if err := ag.Register(url, "token", fp); err != nil {
		t.Fatalf("re-register: %v", err)
	}

	reloaded := New(dir)
	if err := reloaded.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	c := reloaded.Config()
	if c.ServerID != "srv-new" || c.DashboardURL != url {
		t.Errorf("new identity not saved: %+v", c)
	}
	if !c.AllowUnsignedUpdates || len(c.AllowedDataRoots) != 1 || c.AgentPort != 7777 {
		t.Errorf("local-only options not carried over: %+v", c)
	}
	baks, _ := filepath.Glob(filepath.Join(dir, configFileName+".bak-*"))
	if len(baks) != 1 {
		t.Errorf("expected one backup of the old config, got %v", baks)
	}
}

func TestRegister_FailureKeepsOldRegistration(t *testing.T) {
	dir := t.TempDir()
	writeOldConfig(t, dir)
	url, fp := fakeDashboard(t, http.StatusUnauthorized, "")

	ag := New(dir)
	if err := ag.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if err := ag.Register(url, "bad-token", fp); err == nil {
		t.Fatal("registration with a rejected token succeeded")
	}
	if ag.Config().ServerID != "srv-old" {
		t.Errorf("in-memory config replaced after a failed registration: %+v", ag.Config())
	}
	reloaded := New(dir)
	_ = reloaded.LoadConfig()
	if reloaded.Config().ServerID != "srv-old" {
		t.Error("config file changed after a failed registration")
	}
	if baks, _ := filepath.Glob(filepath.Join(dir, configFileName+".bak-*")); len(baks) != 0 {
		t.Errorf("backup made although registration failed: %v", baks)
	}
}
