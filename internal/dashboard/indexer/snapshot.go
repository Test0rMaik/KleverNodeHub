package indexer

import "time"

// Snapshot is the full payload returned by GET /api/indexer/status.
type Snapshot struct {
	UpdatedAt  int64  `json:"updated_at"`
	Ready      bool   `json:"ready"`
	Error      string `json:"error,omitempty"`
	Configured bool   `json:"configured"`

	// Node identity & version
	NodeName        string `json:"node_name,omitempty"`
	NodeType        string `json:"node_type,omitempty"`
	AppVersion      string `json:"app_version,omitempty"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	ChainID         string `json:"chain_id,omitempty"`

	// Sync & chain
	Synced               bool   `json:"synced"`
	EpochNumber          uint64 `json:"epoch_number,omitempty"`
	Nonce                uint64 `json:"nonce,omitempty"`
	ProbableHighestNonce uint64 `json:"probable_highest_nonce,omitempty"`
	BlockLag             int64  `json:"block_lag"`
	ConsensusState       string `json:"consensus_state,omitempty"`
	ConsensusSlotState   string `json:"consensus_slot_state,omitempty"`
	TxProcessed          int64  `json:"tx_processed,omitempty"`
	UptimeSeconds        int64  `json:"uptime_seconds,omitempty"`
	ConnectedPeers       int    `json:"connected_peers,omitempty"`

	// Resource metrics
	CPUPercent     int   `json:"cpu_percent"`
	MemPercent     int   `json:"mem_percent"`
	DiskPercent    int   `json:"disk_percent"`
	DBSizeBytes    int64 `json:"db_size_bytes,omitempty"`
	DiskTotalBytes int64 `json:"disk_total_bytes,omitempty"`
	DiskAvailBytes int64 `json:"disk_avail_bytes,omitempty"`
	MemTotalBytes  int64 `json:"mem_total_bytes,omitempty"`
	MemUsedBytes   int64 `json:"mem_used_bytes,omitempty"`
	NetworkRecvBPS int64 `json:"network_recv_bps,omitempty"`
	NetworkSentBPS int64 `json:"network_sent_bps,omitempty"`

	// Elasticsearch (optional — only populated when ES credentials are configured)
	ESConfigured       bool    `json:"es_configured"`
	ESStatus           string  `json:"es_status,omitempty"`
	ESClusterName      string  `json:"es_cluster_name,omitempty"`
	ESNodes            int     `json:"es_nodes,omitempty"`
	ESDataNodes        int     `json:"es_data_nodes,omitempty"`
	ESActiveShards     int     `json:"es_active_shards,omitempty"`
	ESPrimaryShards    int     `json:"es_primary_shards,omitempty"`
	ESUnassignedShards int     `json:"es_unassigned_shards,omitempty"`
	ESShardsPercent    float64 `json:"es_shards_percent,omitempty"`
	ESError            string  `json:"es_error,omitempty"`
}

func nowUnix() int64 { return time.Now().Unix() }
