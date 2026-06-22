package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config holds the live-resolved settings used each poll tick.
type Config struct {
	NodeURL string // base URL of the Klever observer node REST API
	ESURL   string // Elasticsearch base URL (e.g. http://localhost:9200)
	ESUser  string // Elasticsearch basic-auth username (empty = skip ES)
	ESPass  string // Elasticsearch basic-auth password
}

// ConfigProvider is called on every tick so settings changes apply without restart.
type ConfigProvider func() Config

// Monitor polls the Klever observer node and optionally Elasticsearch,
// maintaining a cached snapshot served instantly to the browser.
type Monitor struct {
	cfgFn    ConfigProvider
	interval time.Duration
	client   *http.Client

	mu     sync.RWMutex
	latest *Snapshot
}

// NewMonitor creates a Monitor. interval defaults to 30s if zero or negative.
func NewMonitor(cfgFn ConfigProvider, interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Monitor{
		cfgFn:    cfgFn,
		interval: interval,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Start runs the poll loop until ctx is cancelled.
func (m *Monitor) Start(ctx context.Context) {
	go func() {
		m.tick(ctx)
		t := time.NewTicker(m.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.tick(ctx)
			}
		}
	}()
}

// Snapshot returns the latest cached snapshot (never nil).
func (m *Monitor) Snapshot() *Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.latest != nil {
		return m.latest
	}
	return &Snapshot{Ready: false, UpdatedAt: nowUnix()}
}

func (m *Monitor) tick(ctx context.Context) {
	cfg := m.cfgFn()
	snap := &Snapshot{UpdatedAt: nowUnix()}

	nodeURL := strings.TrimRight(strings.TrimSpace(cfg.NodeURL), "/")
	if nodeURL == "" {
		// No node URL configured — show unconfigured state.
		snap.Ready = true
		m.store(snap)
		return
	}
	snap.Configured = true

	var sr nodeStatusResp
	if err := m.getJSON(ctx, nodeURL+"/node/status", "", "", &sr); err != nil {
		snap.Error = "node unreachable: " + err.Error()
		m.store(snap)
		log.Printf("indexer-monitor: node status: %v", err)
		return
	}
	if sr.Error != "" {
		snap.Error = "node: " + sr.Error
		m.store(snap)
		log.Printf("indexer-monitor: node status error: %s", sr.Error)
		return
	}
	applyMetrics(snap, &sr.Data.Metrics)

	esURL := strings.TrimRight(strings.TrimSpace(cfg.ESURL), "/")
	if esURL != "" && cfg.ESUser != "" {
		snap.ESConfigured = true
		var eh esClusterHealth
		if err := m.getJSON(ctx, esURL+"/_cluster/health", cfg.ESUser, cfg.ESPass, &eh); err != nil {
			snap.ESError = err.Error()
			log.Printf("indexer-monitor: es health: %v", err)
		} else {
			snap.ESStatus = eh.Status
			snap.ESClusterName = eh.ClusterName
			snap.ESNodes = eh.NumberOfNodes
			snap.ESDataNodes = eh.NumberOfDataNodes
			snap.ESActiveShards = eh.ActiveShards
			snap.ESPrimaryShards = eh.ActivePrimaryShards
			snap.ESUnassignedShards = eh.UnassignedShards
			snap.ESShardsPercent = eh.ActiveShardsPercent
		}
	}

	snap.Ready = true
	m.store(snap)
}

func applyMetrics(snap *Snapshot, mx *nodeMetrics) {
	snap.NodeName = mx.NodeDisplayName
	snap.NodeType = mx.NodeType
	snap.ChainID = mx.ChainID
	snap.EpochNumber = mx.EpochNumber
	snap.AppVersion = mx.AppVersion
	snap.LatestVersion = mx.LatestTagVersion
	snap.UpdateAvailable = mx.LatestTagVersion != "" &&
		extractVersion(mx.AppVersion) != mx.LatestTagVersion
	snap.Synced = mx.IsSyncing == 0
	snap.Nonce = mx.Nonce
	snap.ProbableHighestNonce = mx.ProbableHighestNonce
	if mx.ProbableHighestNonce > mx.Nonce {
		snap.BlockLag = int64(mx.ProbableHighestNonce - mx.Nonce)
	}
	snap.ConsensusState = mx.ConsensusState
	snap.ConsensusSlotState = mx.ConsensusSlotState
	snap.TxProcessed = mx.NumTxProcessed
	snap.UptimeSeconds = mx.NodeUptimeSeconds
	snap.ConnectedPeers = mx.NumConnectedPeers
	snap.CPUPercent = mx.CPULoadPercent
	snap.MemPercent = mx.MemLoadPercent
	snap.DiskPercent = mx.DiskUsagePercent
	snap.DBSizeBytes = mx.DBSizeBytes
	snap.DiskTotalBytes = mx.DiskTotalBytes
	snap.DiskAvailBytes = mx.DiskAvailableBytes
	snap.MemTotalBytes = mx.MemTotal
	snap.MemUsedBytes = mx.MemUsedGolang
	snap.NetworkRecvBPS = mx.NetworkRecvBPS
	snap.NetworkSentBPS = mx.NetworkSentBPS
}

// extractVersion returns the semver portion of a Klever app-version string.
// "v1.7.17-0-g333f6ec9/go1.25.10/..." → "v1.7.17"
func extractVersion(s string) string {
	if i := strings.IndexAny(s, "-/"); i > 0 {
		return s[:i]
	}
	return s
}

func (m *Monitor) store(snap *Snapshot) {
	m.mu.Lock()
	m.latest = snap
	m.mu.Unlock()
}

func (m *Monitor) getJSON(ctx context.Context, url, user, pass string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
