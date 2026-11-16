package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// historyMetrics are the node metrics that something reads back from the
// metric history. A node reports ~100 numeric values every poll; storing all
// of them made the history grow to many GB while only these are ever queried.
// Live values for the overview come from node metadata, not from here.
//
// Keep in sync with the readers:
//   - web/templates/node.html (charts + status: fetchMetric/updateStatusFromLatest)
//   - handlers/servers.go nodeMetricNames (node list enrichment)
//   - handlers/performance.go + alerting/regression.go (block-time regression)
//   - alerting/defaults.go (builtin rules) — any alert rule's metric is also
//     kept dynamically, see historyNames
//   - klever/monitor.go validator metrics
var historyMetrics = []string{
	"klv_nonce",
	"klv_num_connected_peers",
	"klv_num_transactions_processed",
	"klv_network_recv_bps",
	"klv_network_sent_bps",
	"klv_cpu_load_percent",
	"klv_mem_load_percent",
	"klv_epoch_number",
	"klv_consensus_state",
	"klv_is_syncing",
	"klv_probable_highest_nonce",
	"klv_block_process_duration_ms",
	"validator_missed_blocks",
	"validator_leader_misses",
	"validator_jailed",
}

// historyNamesTTL bounds how stale the cached allowlist can get. Rule writes
// through AlertStore invalidate it immediately (DB.rulesGen); the TTL only
// covers rules changed by another process (e.g. the seed tool).
const historyNamesTTL = time.Minute

// historyNames returns historyMetrics plus the metric of every alert rule
// (enabled or not, so toggling a rule keeps its history). Cached for
// historyNamesTTL so the per-message filter doesn't hit the DB.
func (s *MetricsStore) historyNames() (map[string]struct{}, error) {
	s.histMu.Lock()
	defer s.histMu.Unlock()
	gen := s.db.rulesGen.Load()
	if s.histNames != nil && s.histGen == gen && time.Since(s.histLoaded) < historyNamesTTL {
		return s.histNames, nil
	}

	names := make(map[string]struct{}, len(historyMetrics)+8)
	for _, n := range historyMetrics {
		names[n] = struct{}{}
	}
	rows, err := s.db.db.Query(`SELECT DISTINCT metric_name FROM alert_rules`)
	if err != nil {
		return nil, fmt.Errorf("list alert rule metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan alert rule metric: %w", err)
		}
		names[n] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list alert rule metrics: %w", err)
	}

	s.histNames = names
	s.histGen = gen
	s.histLoaded = time.Now()
	return names, nil
}

// FilterHistory returns only the metrics worth keeping in the history. If the
// alert rules can't be read it keeps everything: losing a rule's data would
// silently break that alert, while storing too much for a minute is harmless.
func (s *MetricsStore) FilterHistory(metrics map[string]float64) map[string]float64 {
	names, err := s.historyNames()
	if err != nil {
		return metrics
	}
	out := make(map[string]float64, len(names))
	for k, v := range metrics {
		if _, ok := names[k]; ok {
			out[k] = v
		}
	}
	return out
}

// PurgeUnusedHistory deletes stored history (raw and archived) for metrics
// that are no longer recorded — the backlog from before the allowlist. Runs
// in batches so agent writes interleave. The freed pages are reused by SQLite;
// the file only shrinks with CompactDB.
func (s *MetricsStore) PurgeUnusedHistory() (int64, error) {
	names, err := s.historyNames()
	if err != nil {
		return 0, err
	}
	args := make([]any, 0, len(names))
	for n := range names {
		args = append(args, n)
	}
	where := "metric_name NOT IN (?" + strings.Repeat(",?", len(args)-1) + ")"

	var total int64
	for _, table := range []string{"metrics_recent", "metrics_archive"} {
		n, err := s.deleteByRowidWindows(table, where, args...)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// purgeWindow is how many rowids one deleteByRowidWindows step covers.
const purgeWindow = 20000

// deleteByRowidWindows deletes rows matching where by walking the table in
// fixed rowid windows. Unlike deleteInBatches (fine when matches sit at the
// front, e.g. oldest rows), this stays linear when kept rows are interleaved
// with deleted ones: a LIMIT-based batch would rescan the growing prefix of
// kept rows every time, which is quadratic on a multi-GB table.
func (s *MetricsStore) deleteByRowidWindows(table, where string, args ...any) (int64, error) {
	var lo, hi sql.NullInt64
	if err := s.db.db.QueryRow(`SELECT MIN(rowid), MAX(rowid) FROM `+table).Scan(&lo, &hi); err != nil {
		return 0, fmt.Errorf("purge %s: %w", table, err)
	}
	if !lo.Valid {
		return 0, nil
	}
	q := fmt.Sprintf(`DELETE FROM %s WHERE rowid >= ? AND rowid < ? AND %s`, table, where)
	var total int64
	for start := lo.Int64; start <= hi.Int64; start += purgeWindow {
		s.mu.Lock()
		result, err := s.db.db.Exec(q, append([]any{start, start + purgeWindow}, args...)...)
		s.mu.Unlock()
		if err != nil {
			return total, fmt.Errorf("purge %s: %w", table, err)
		}
		n, _ := result.RowsAffected()
		total += n
		time.Sleep(10 * time.Millisecond) // yield between windows
	}
	return total, nil
}
