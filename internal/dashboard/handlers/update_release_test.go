package handlers

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/CTJaeger/KleverNodeHub/internal/release"
)

func TestValidReleaseURL(t *testing.T) {
	base := "https://github.com/Test0rMaik/KleverNodeHub/releases/download/"
	tests := []struct {
		url  string
		want bool
	}{
		{base + "v0.3.87-knh.1/klever-agent-linux-amd64", true},
		{"https://github.com/CTJaeger/KleverNodeHub/releases/download/v0.3.87/klever-agent-linux-amd64", false},
		{base + "../../../evil/repo/releases/download/v1/klever-agent-linux-amd64", false},
		{base + "v1/klever-agent-linux-amd64?x=1", false},
		{base + "v1/klever-agent-linux-amd64#frag", false},
		{base + "klever-agent-linux-amd64", false}, // missing tag segment
		{base + "v1/a/b", false},
		{"http://github.com/Test0rMaik/KleverNodeHub/releases/download/v1/x", false},
	}
	for _, tt := range tests {
		if got := validReleaseURL(tt.url); got != tt.want {
			t.Errorf("validReleaseURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

// rewriteTransport sends every request to the test server, keeping the path.
type rewriteTransport struct{ target string }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(rt.target)
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func TestFetchSignedChecksums(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	defer release.SwapPublicKeyForTest(pub)()

	sums := []byte("abc123  klever-agent-linux-amd64\n# release good\n")
	goodSig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums))
	files := map[string]string{
		"/good/checksums.txt":     string(sums),
		"/good/release.sig":       goodSig,
		"/unsigned/checksums.txt": string(sums),
		"/forged/checksums.txt":   "evil999  klever-agent-linux-amd64\n",
		"/forged/release.sig":     goodSig,
		"/renamed/checksums.txt":  string(sums), // validly signed, but for release "good"
		"/renamed/release.sig":    goodSig,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, body := range files {
			if strings.HasSuffix(r.URL.Path, name) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	client := &http.Client{Transport: rewriteTransport{srv.URL}}

	got, _, err := fetchSignedChecksums(client, "good")
	if err != nil || string(got) != string(sums) {
		t.Fatalf("signed release rejected: %v", err)
	}
	if _, _, err := fetchSignedChecksums(client, "unsigned"); err == nil {
		t.Error("release without signature accepted")
	}
	if _, _, err := fetchSignedChecksums(client, "forged"); err == nil {
		t.Error("checksums not matching the signature accepted")
	}
	if _, _, err := fetchSignedChecksums(client, "renamed"); err == nil {
		t.Error("signed checksums of another release accepted under a new tag")
	}
	if _, _, err := fetchSignedChecksums(client, "../x"); err == nil {
		t.Error("path-like tag accepted")
	}
}

func TestUnsignedSourcesDisabledByDefault(t *testing.T) {
	h := &UpdateHandler{}
	for name, fn := range map[string]http.HandlerFunc{"upload": h.HandleUploadBinary, "custom": h.HandleDownloadCustom} {
		w := httptest.NewRecorder()
		fn(w, httptest.NewRequest(http.MethodPost, "/", nil))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want %d", name, w.Code, http.StatusForbidden)
		}
	}
}

func TestSanitizeConfigPreview_DropsHostileItems(t *testing.T) {
	in := `{"items":[
		{"id":"config.toml::Node/Name","file":"config.toml","key":"Node/Name","mine":"a","new":"b"},
		{"id":"x\" ontoggle=\"alert(1)::k","file":"x\" ontoggle=\"alert(1)","key":"k"},
		{"id":"config.yaml::other","file":"config.yaml","key":"k"},
		{"id":"api.yaml::a\"b","file":"api.yaml","key":"a\"b"}
	],"added_by_file":{"evil\"":3,"api.yaml":1},"download_source":"primary"}`
	out := sanitizeConfigPreview(in)
	if strings.Contains(out, "ontoggle") || strings.Contains(out, "evil") || strings.Contains(out, `a\"b`) {
		t.Fatalf("hostile entries survived: %s", out)
	}
	if !strings.Contains(out, "Node/Name") || !strings.Contains(out, `"api.yaml":1`) {
		t.Errorf("valid entries dropped: %s", out)
	}
	if got := sanitizeConfigPreview("not json"); got != `{"items":[]}` {
		t.Errorf("garbage output not replaced: %s", got)
	}
}

func TestTrustsUpstreamKey(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.3.88": false, "v0.3.89": true, "v0.3.92": true, "0.3.90": true,
		"v0.3.93": false, "v0.3.93-knh.1": false, "v0.3.87-knh.6": false, "dev": false, "": false,
	} {
		if got := trustsUpstreamKey(v); got != want {
			t.Errorf("trustsUpstreamKey(%q) = %v, want %v", v, got, want)
		}
	}
}
