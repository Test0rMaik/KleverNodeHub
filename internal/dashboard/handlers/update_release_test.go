package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestVerifyAgentChecksum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("abc123  klever-agent-linux-amd64\n"))
	}))
	defer srv.Close()

	if err := verifyAgentChecksum(srv.Client(), srv.URL+"/ok", "klever-agent-linux-amd64", "abc123"); err != nil {
		t.Fatalf("matching checksum rejected: %v", err)
	}
	err := verifyAgentChecksum(srv.Client(), srv.URL+"/ok", "klever-agent-linux-amd64", "deadbeef")
	if err == nil || isUnavailable(err) {
		t.Fatalf("mismatch must be a hard error, got %v", err)
	}
	if err := verifyAgentChecksum(srv.Client(), srv.URL+"/missing", "x", "y"); !isUnavailable(err) {
		t.Fatalf("missing checksums file should be 'unavailable', got %v", err)
	}
}

func isUnavailable(err error) bool { return err != nil && errors.Is(err, errChecksumUnavailable) }
