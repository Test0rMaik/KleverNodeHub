# Deployment

## systemd Service Files

### Dashboard

```bash
# Create service user and install dir (owned by the service user so that
# self-update can replace the binary)
sudo useradd -r -s /bin/false klever
sudo mkdir -p /opt/klever-node-hub
sudo cp klever-node-hub-linux-amd64 /opt/klever-node-hub/klever-node-hub
sudo chmod 755 /opt/klever-node-hub/klever-node-hub
sudo chown -R klever: /opt/klever-node-hub

# Install service (data goes to /var/lib/klever-node-hub via StateDirectory)
sudo cp deploy/klever-node-hub.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now klever-node-hub

# View logs
journalctl -u klever-node-hub -f
```

Edit `/etc/systemd/system/klever-node-hub.service` to set your `--domain` and `--addr` flags.

#### Existing install in `/usr/local/bin` (self-update fails with "read-only file system")

The old unit had `ProtectSystem=full`, which makes `/usr` read-only for the service, and `/usr/local/bin` is not writable by the service user anyway — so the dashboard could never replace its own binary. Move it to a dedicated directory and keep your existing flags and data directory:

```bash
sudo systemctl stop klever-node-hub
sudo mkdir -p /opt/klever-node-hub
sudo mv /usr/local/bin/klever-node-hub /opt/klever-node-hub/
sudo chown -R klever: /opt/klever-node-hub          # use the User= from your unit

sudo systemctl edit klever-node-hub
#   [Service]
#   ExecStart=
#   ExecStart=/opt/klever-node-hub/klever-node-hub <your existing flags from `systemctl cat klever-node-hub`>
#   ReadWritePaths=/opt/klever-node-hub

sudo systemctl daemon-reload
sudo systemctl start klever-node-hub
```

### Agent

```bash
# Copy binary
sudo cp klever-agent-linux-amd64 /usr/local/bin/klever-agent
sudo chmod +x /usr/local/bin/klever-agent

# Install service (edit DASHBOARD_HOST first!)
sudo cp deploy/klever-agent.service /etc/systemd/system/
sudo nano /etc/systemd/system/klever-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now klever-agent

# View logs
journalctl -u klever-agent -f
```

The agent runs as root because it needs access to the Docker socket.

## Self-Update

When running as a systemd service with `Restart=always`, the dashboard self-update will:

1. Download and verify the new binary
2. Replace the current binary
3. Exit the process
4. systemd automatically restarts with the new version
