package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/CTJaeger/KleverNodeHub/internal/crypto"
)

// A CA that exists but cannot be decrypted must never be silently replaced —
// that would orphan every registered agent.
func TestLoadOrCreateCA_RefusesToOverwriteUnloadableCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	ca, err := crypto.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.SaveToDir(dir, bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "ca.key.enc"))

	if _, err := loadOrCreateCA(dir, bytes.Repeat([]byte{2}, 32)); err == nil {
		t.Fatal("expected an error for a CA that cannot be decrypted")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "ca.key.enc"))
	if !bytes.Equal(before, after) {
		t.Fatal("existing CA key was overwritten")
	}

	// Fresh dir still creates a CA, and a correct key still loads it.
	fresh := filepath.Join(t.TempDir(), "ca")
	if _, err := loadOrCreateCA(fresh, bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatalf("fresh CA creation failed: %v", err)
	}
	if _, err := loadOrCreateCA(fresh, bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatalf("reload failed: %v", err)
	}
}
