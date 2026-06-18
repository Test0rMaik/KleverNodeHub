package handlers

import (
	"testing"
)

func TestParseOSArch(t *testing.T) {
	tests := []struct {
		input    string
		wantOS   string
		wantArch string
	}{
		{"linux/amd64", "linux", "amd64"},
		{"darwin/arm64", "darwin", "arm64"},
		{"windows/amd64", "windows", "amd64"},
		{"", "linux", "amd64"},        // default
		{"unknown", "linux", "amd64"}, // no slash → default
	}

	for _, tt := range tests {
		os, arch := ParseOSArch(tt.input)
		if os != tt.wantOS || arch != tt.wantArch {
			t.Errorf("ParseOSArch(%q) = (%q, %q), want (%q, %q)", tt.input, os, arch, tt.wantOS, tt.wantArch)
		}
	}
}

func TestAgentBinaryURL(t *testing.T) {
	tests := []struct {
		name, setting, os, arch, want string
	}{
		{"direct file", "https://my.site/klever-agent-linux", "linux", "amd64", "https://my.site/klever-agent-linux"},
		{"base url", "https://my.site/agents/", "linux", "amd64", "https://my.site/agents/klever-agent-linux-amd64"},
		{"base url no slash trim dup", "https://my.site/agents/", "windows", "amd64", "https://my.site/agents/klever-agent-windows-amd64.exe"},
		{"template", "https://my.site/klever-agent-{os}-{arch}", "linux", "arm64", "https://my.site/klever-agent-linux-arm64"},
		{"template os only", "https://my.site/agent-{os}", "linux", "amd64", "https://my.site/agent-linux"},
	}
	for _, tt := range tests {
		if got := agentBinaryURL(tt.setting, tt.os, tt.arch); got != tt.want {
			t.Errorf("%s: agentBinaryURL(%q,%q,%q) = %q, want %q", tt.name, tt.setting, tt.os, tt.arch, got, tt.want)
		}
	}

	if !isDirectAgentURL("https://my.site/klever-agent-linux") {
		t.Error("expected direct URL")
	}
	if isDirectAgentURL("https://my.site/agents/") {
		t.Error("base URL (trailing /) is not direct")
	}
	if isDirectAgentURL("https://my.site/klever-agent-{os}-{arch}") {
		t.Error("template URL is not direct")
	}
}
