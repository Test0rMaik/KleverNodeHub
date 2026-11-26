#!/bin/bash
set -euo pipefail

# Klever Node Hub — Agent Installation Script
# Usage: curl -sSL https://raw.githubusercontent.com/Test0rMaik/KleverNodeHub/main/scripts/install-agent.sh | bash -s -- --token TOKEN --dashboard URL

AGENT_BIN="/usr/local/bin/klever-agent"
AGENT_CONFIG_DIR="/etc/klever-agent"
AGENT_USER="klever-agent"
SERVICE_NAME="klever-agent"

# Release signing public key (SubjectPublicKeyInfo, base64) — the same key as
# internal/release.PublicKeyHex. Downloads are installed only if they are listed
# in a checksums.txt carrying this key's signature.
RELEASE_PUBKEY_B64="MCowBQYDK2VwAyEAgW3WzL9+A731TnL6OOKDfsBWVWBdgo8W7teI8ZikoTQ="

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RESET='\033[0m'

log()   { echo -e "${GREEN}[+]${RESET} $*"; }
warn()  { echo -e "${YELLOW}[!]${RESET} $*"; }
error() { echo -e "${RED}[✗]${RESET} $*" >&2; exit 1; }

# Parse arguments
TOKEN=""
DASHBOARD_URL=""
BINARY_URL_BASE=""  # optional: overrides the default GitHub release URL
ALLOW_UNSIGNED=""   # required with --binary-url-base: custom builds are not signed
CA_FINGERPRINT=""   # SHA-256 of the dashboard CA, pins the dashboard during registration
DATA_ROOTS=""       # optional, comma-separated: node data dirs the hub may use (agent.json allowed_data_roots)

while [[ $# -gt 0 ]]; do
    case "$1" in
        --token)           TOKEN="$2"; shift 2 ;;
        --dashboard)       DASHBOARD_URL="$2"; shift 2 ;;
        --binary-url-base) BINARY_URL_BASE="$2"; shift 2 ;;
        --allow-unsigned)  ALLOW_UNSIGNED=1; shift ;;
        --ca-fingerprint)  CA_FINGERPRINT="$2"; shift 2 ;;
        --data-root)       DATA_ROOTS="$2"; shift 2 ;;
        *)                 error "Unknown argument: $1" ;;
    esac
done

[[ -z "$TOKEN" ]] && error "Missing --token argument"
[[ -z "$DASHBOARD_URL" ]] && error "Missing --dashboard argument"
[[ -n "$BINARY_URL_BASE" && -z "$ALLOW_UNSIGNED" ]] && error "--binary-url-base installs an unsigned build: add --allow-unsigned to confirm"
if [[ -n "$DATA_ROOTS" ]]; then
    # Checked before anything is installed: an empty list would mean "no restriction".
    IFS=',' read -ra _roots <<< "$DATA_ROOTS"
    _n=0
    for _r in "${_roots[@]}"; do
        _r="${_r// /}"
        [[ -z "$_r" ]] && continue
        [[ "$_r" == /* ]] || error "--data-root entries must be absolute paths: ${_r}"
        _n=$((_n + 1))
    done
    [[ $_n -gt 0 ]] || error "--data-root is empty"
fi

# Check root
[[ $EUID -ne 0 ]] && error "This script must be run as root (sudo)"

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  GOARCH="amd64" ;;
    aarch64) GOARCH="arm64" ;;
    *)       error "Unsupported architecture: $ARCH" ;;
esac

GOOS="linux"

log "Klever Node Hub — Agent Installer"
log "Architecture: ${GOOS}/${GOARCH}"
log "Dashboard: ${DASHBOARD_URL}"

# Install Docker if not present
if ! command -v docker &>/dev/null; then
    log "Docker not found. Installing Docker..."
    if command -v apt-get &>/dev/null; then
        apt-get update -qq
        apt-get install -y -qq ca-certificates curl gnupg
        install -m 0755 -d /etc/apt/keyrings
        DISTRO=$(. /etc/os-release && echo "$ID")
        curl -fsSL "https://download.docker.com/linux/${DISTRO}/gpg" | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
        chmod a+r /etc/apt/keyrings/docker.gpg
        echo \
          "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/${DISTRO} \
          $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list
        apt-get update -qq
        apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin
    elif command -v yum &>/dev/null; then
        yum install -y -q yum-utils
        yum-config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
        yum install -y -q docker-ce docker-ce-cli containerd.io docker-buildx-plugin
    else
        warn "Could not detect package manager. Trying Docker convenience script..."
        curl -fsSL https://get.docker.com | sh
    fi
    systemctl enable docker
    systemctl start docker
    log "Docker installed and started"
else
    log "Docker already installed: $(docker --version)"
fi

# Install jq if not present
if ! command -v jq &>/dev/null; then
    log "Installing jq..."
    if command -v apt-get &>/dev/null; then
        apt-get install -y -qq jq
    elif command -v yum &>/dev/null; then
        yum install -y -q jq
    else
        warn "Could not install jq automatically. Please install it manually."
    fi
    log "jq installed"
else
    log "jq already installed"
fi

# Download agent binary
# Resolve the binary URL from --binary-url-base (same 3-form logic as the
# dashboard's agentBinaryURL helper) or fall back to the fork's GitHub release.
if [[ -n "$BINARY_URL_BASE" ]]; then
    if [[ "$BINARY_URL_BASE" == *"{os}"* ]] || [[ "$BINARY_URL_BASE" == *"{arch}"* ]]; then
        # Template form: replace {os} and {arch} placeholders.
        RELEASE_URL="${BINARY_URL_BASE//\{os\}/$GOOS}"
        RELEASE_URL="${RELEASE_URL//\{arch\}/$GOARCH}"
    elif [[ "$BINARY_URL_BASE" == */ ]]; then
        # Base-directory form: append the conventional binary name.
        RELEASE_URL="${BINARY_URL_BASE}klever-agent-${GOOS}-${GOARCH}"
    else
        # Direct-file form: use as-is.
        RELEASE_URL="$BINARY_URL_BASE"
    fi
else
    # Resolve "latest" once, so binary and checksums come from the same release.
    RELEASE_TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/Test0rMaik/KleverNodeHub/releases/latest" | sed 's#.*/tag/##')
    [[ "$RELEASE_TAG" =~ ^v[0-9A-Za-z.-]+$ ]] || error "Could not determine the latest release (got: ${RELEASE_TAG})"
    RELEASE_BASE="https://github.com/Test0rMaik/KleverNodeHub/releases/download/${RELEASE_TAG}"
    RELEASE_URL="${RELEASE_BASE}/klever-agent-${GOOS}-${GOARCH}"
fi

log "Downloading agent binary..."
AGENT_TMP=$(mktemp) # private file: a fixed /tmp path could be swapped between verify and install
if ! curl -fsSL -o "$AGENT_TMP" "$RELEASE_URL"; then
    error "Failed to download agent binary from: ${RELEASE_URL}"
fi

# Verify the download is actually an ELF binary, not an HTML error page.
MAGIC=$(head -c 4 "$AGENT_TMP" | od -A n -t x1 | tr -d ' \n')
if [[ "$MAGIC" != "7f454c46" ]]; then
    rm -f "$AGENT_TMP"
    error "Downloaded file is not a valid ELF binary (got content type error or 404). Check the binary URL: ${RELEASE_URL}"
fi

# Verify the download against the signed release checksums.
verify_release() {
    local bin="$1" base="$2" asset="$3" dir expected actual
    command -v openssl &>/dev/null || error "openssl is required to verify the agent download"
    # Ed25519 verification of raw data (pkeyutl -rawin) needs OpenSSL 3.
    [[ "$(openssl version | awk '{print $2}' | cut -d. -f1)" -ge 3 ]] 2>/dev/null \
        || error "OpenSSL 3 or newer is required to verify the agent download (found: $(openssl version))"
    dir=$(mktemp -d)
    trap 'rm -rf "$dir"' RETURN
    curl -fsSL -o "$dir/checksums.txt" "${base}/checksums.txt" || error "Failed to download release checksums"
    curl -fsSL -o "$dir/checksums.txt.sig" "${base}/checksums.txt.sig" || error "Release is not signed (no checksums.txt.sig)"
    printf -- '-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----\n' "$RELEASE_PUBKEY_B64" > "$dir/release.pub"
    base64 -d "$dir/checksums.txt.sig" > "$dir/sig.bin" 2>/dev/null || error "Malformed release signature"
    openssl pkeyutl -verify -pubin -inkey "$dir/release.pub" -rawin \
        -in "$dir/checksums.txt" -sigfile "$dir/sig.bin" &>/dev/null \
        || error "Release signature verification FAILED — not installing"
    grep -Fqx "# release ${RELEASE_TAG}" "$dir/checksums.txt" || error "Signed checksums are not for release ${RELEASE_TAG}"
    expected=$(awk -v a="$asset" '$2 == a || $2 == "*" a {print tolower($1)}' "$dir/checksums.txt")
    actual=$(sha256sum "$bin" | awk '{print $1}')
    [[ -n "$expected" && "$expected" == "$actual" ]] || error "Agent binary does not match the signed release checksum — not installing"
    log "Release ${RELEASE_TAG}: signature and checksum verified"
}

if [[ -z "$BINARY_URL_BASE" ]]; then
    verify_release "$AGENT_TMP" "$RELEASE_BASE" "klever-agent-${GOOS}-${GOARCH}"
else
    warn "Installing an UNSIGNED agent build from ${RELEASE_URL} (--allow-unsigned)"
fi

chmod 755 "$AGENT_TMP"
mv "$AGENT_TMP" "$AGENT_BIN"
log "Agent installed to ${AGENT_BIN}"

# Create config directory
mkdir -p "$AGENT_CONFIG_DIR"
chmod 700 "$AGENT_CONFIG_DIR"

# Create systemd service
cat > /etc/systemd/system/${SERVICE_NAME}.service << EOF
[Unit]
Description=Klever Node Hub Agent
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=${AGENT_BIN} --config-dir ${AGENT_CONFIG_DIR}
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "${SERVICE_NAME}"
log "Systemd service created and enabled"

# Register with dashboard
log "Registering with dashboard..."
# --register-only saves the registration and exits; the service runs the agent.
# An existing registration on this host is replaced (the old agent.json is kept
# as agent.json.bak-<time>).
if ! "${AGENT_BIN}" --register-only --register-token "$TOKEN" --dashboard-url "$DASHBOARD_URL" --ca-fingerprint "$CA_FINGERPRINT" --config-dir "$AGENT_CONFIG_DIR"; then
    error "Registration failed"
fi

if [[ -n "$DATA_ROOTS" ]]; then
    tmp=$(mktemp)
    if ! { jq --arg r "$DATA_ROOTS" '.allowed_data_roots = ($r | split(",") | map(gsub("^\\s+|\\s+$"; "")) | map(select(length > 0)))' \
             "$AGENT_CONFIG_DIR/agent.json" > "$tmp" && install -m 600 "$tmp" "$AGENT_CONFIG_DIR/agent.json"; }; then
        rm -f "$tmp"
        # Never leave an unrestricted agent behind when a restriction was asked for.
        systemctl disable --now "${SERVICE_NAME}" >/dev/null 2>&1 || true
        error "Failed to set allowed_data_roots — the agent is stopped and disabled; fix and rerun"
    fi
    rm -f "$tmp"
    log "Node data dirs restricted to: ${DATA_ROOTS}"
fi

# Start or restart the service (restart is a no-op if not yet running,
# and picks up the new binary when reinstalling over an existing agent).
systemctl restart "${SERVICE_NAME}"
log "Agent started successfully!"
log ""
log "Commands:"
log "  systemctl status ${SERVICE_NAME}   — Check status"
log "  journalctl -u ${SERVICE_NAME} -f   — View logs"
log "  systemctl restart ${SERVICE_NAME}  — Restart agent"
log ""
log "════════════════════════════════════════════════════════════"
log "  Installation complete! The agent runs as a systemd service."
log "  You can safely close this terminal now."
log "════════════════════════════════════════════════════════════"
