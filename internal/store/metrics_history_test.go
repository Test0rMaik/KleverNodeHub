package store

import (
	"testing"
	"time"
)

func TestFilterHistory_KeepsReadMetricsAndRuleMetrics(t *testing.T) {
	s := setupMetricsStore(t)
	if err := NewAlertStore(s.db).CreateRule(&AlertRule{
		ID: "custom-1", Name: "Custom", MetricName: "klv_tx_pool_load",
		Condition: "gt", Threshold: 1, Severity: "warning", NodeFilter: "*",
	}); err != nil {
		t.Fatal(err)
	}

	got := s.FilterHistory(map[string]float64{
		"klv_nonce":         1, // chart
		"validator_jailed":  0, // validator alert
		"klv_tx_pool_load":  2, // only kept because a rule uses it
		"klv_slot_duration": 4, // read by nothing
		"klv_total_supply":  5, // read by nothing
	})
	for _, k := range []string{"klv_nonce", "validator_jailed", "klv_tx_pool_load"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s was dropped, want kept", k)
		}
	}
	for _, k := range []string{"klv_slot_duration", "klv_total_supply"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was kept, want dropped", k)
		}
	}
}

func TestPurgeUnusedHistory(t *testing.T) {
	s := setupMetricsStore(t)
	if err := NewAlertStore(s.db).CreateRule(&AlertRule{
		ID: "custom-1", Name: "Custom", MetricName: "klv_tx_pool_load",
		Condition: "gt", Threshold: 1, Severity: "warning", NodeFilter: "*",
	}); err != nil {
		t.Fatal(err)
	}

	// More junk rows than one rowid window, so the purge walks several windows.
	base := time.Now().Unix() - 3600
	junk := purgeWindow + 500
	tx, err := s.db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < junk; i++ {
		if _, err := tx.Exec(`INSERT INTO metrics_recent (node_id, server_id, metric_name, metric_value, collected_at)
			VALUES ('node-1', 'server-1', 'klv_total_supply', ?, ?)`, float64(i), base+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertNodeMetrics("node-1", "server-1", map[string]float64{
		"klv_nonce": 1, "klv_tx_pool_load": 2,
	}, base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"klv_total_supply", "klv_nonce"} {
		if _, err := s.db.db.Exec(`INSERT INTO metrics_archive
			(node_id, server_id, metric_name, avg_value, min_value, max_value, sample_count, bucket_start, bucket_end)
			VALUES ('node-1', 'server-1', ?, 1, 1, 1, 1, ?, ?)`, name, base-600, base-300); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := s.PurgeUnusedHistory()
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if want := int64(junk + 1); removed != want {
		t.Errorf("removed = %d, want %d", removed, want)
	}

	count := func(table, name string) int {
		var n int
		if err := s.db.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE metric_name = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("metrics_recent", "klv_total_supply"); n != 0 {
		t.Errorf("unused recent rows left: %d", n)
	}
	if n := count("metrics_archive", "klv_total_supply"); n != 0 {
		t.Errorf("unused archive rows left: %d", n)
	}
	if count("metrics_recent", "klv_nonce") != 1 || count("metrics_recent", "klv_tx_pool_load") != 1 || count("metrics_archive", "klv_nonce") != 1 {
		t.Error("kept metrics were deleted")
	}
}

func TestMigration_DropsUnusedMetricsIndex(t *testing.T) {
	s := setupMetricsStore(t)
	var n int
	if err := s.db.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_metrics_recent_node_time'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("idx_metrics_recent_node_time still exists")
	}
	if err := s.db.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_metrics_recent_name_time'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("idx_metrics_recent_name_time is missing")
	}
}
