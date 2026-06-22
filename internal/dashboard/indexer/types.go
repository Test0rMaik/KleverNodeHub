package indexer

// nodeStatusResp is the JSON envelope returned by GET <node>/node/status.
type nodeStatusResp struct {
	Data struct {
		Metrics nodeMetrics `json:"metrics"`
	} `json:"data"`
	Error string `json:"error"`
	Code  string `json:"code"`
}

// nodeMetrics holds the fields we care about from the metrics map.
type nodeMetrics struct {
	AppVersion           string `json:"klv_app_version"`
	LatestTagVersion     string `json:"klv_latest_tag_software_version"`
	IsSyncing            int    `json:"klv_is_syncing"`
	Nonce                uint64 `json:"klv_nonce"`
	HighestFinalNonce    uint64 `json:"klv_highest_final_nonce"`
	ProbableHighestNonce uint64 `json:"klv_probable_highest_nonce"`
	CurrentSlot          uint64 `json:"klv_current_slot"`
	SynchronizedSlot     uint64 `json:"klv_synchronized_slot"`
	EpochNumber          uint64 `json:"klv_epoch_number"`
	NodeDisplayName      string `json:"klv_node_display_name"`
	NodeType             string `json:"klv_node_type"`
	PeerType             string `json:"klv_peer_type"`
	NumConnectedPeers    int    `json:"klv_num_connected_peers"`
	NodeUptimeSeconds    int64  `json:"klv_node_uptime_seconds"`
	CPULoadPercent       int    `json:"klv_cpu_load_percent"`
	MemLoadPercent       int    `json:"klv_mem_load_percent"`
	MemTotal             int64  `json:"klv_mem_total"`
	MemUsedGolang        int64  `json:"klv_mem_used_golang"`
	DiskAvailableBytes   int64  `json:"klv_disk_available_bytes"`
	DiskTotalBytes       int64  `json:"klv_disk_total_bytes"`
	DiskUsagePercent     int    `json:"klv_disk_usage_percent"`
	DBSizeBytes          int64  `json:"klv_db_size_bytes"`
	NumTxProcessed       int64  `json:"klv_num_transactions_processed"`
	ConsensusState       string `json:"klv_consensus_state"`
	ConsensusSlotState   string `json:"klv_consensus_slot_state"`
	NetworkRecvBPS       int64  `json:"klv_network_recv_bps"`
	NetworkSentBPS       int64  `json:"klv_network_sent_bps"`
	ChainID              string `json:"klv_chain_id"`
}

// esClusterHealth mirrors GET <es>/_cluster/health (standard Elasticsearch API).
type esClusterHealth struct {
	ClusterName         string  `json:"cluster_name"`
	Status              string  `json:"status"`
	TimedOut            bool    `json:"timed_out"`
	NumberOfNodes       int     `json:"number_of_nodes"`
	NumberOfDataNodes   int     `json:"number_of_data_nodes"`
	ActivePrimaryShards int     `json:"active_primary_shards"`
	ActiveShards        int     `json:"active_shards"`
	RelocatingShards    int     `json:"relocating_shards"`
	InitializingShards  int     `json:"initializing_shards"`
	UnassignedShards    int     `json:"unassigned_shards"`
	ActiveShardsPercent float64 `json:"active_shards_percent_as_number"`
}
