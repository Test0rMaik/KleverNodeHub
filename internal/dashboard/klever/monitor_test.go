package klever

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"3":   3 * time.Second,
		"10":  10 * time.Second,
		"":    0,
		"abc": 0,
		"0":   0,
		"-5":  0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

// mockChain serves canned indexer API responses for a tiny 5-block window.
// Validator "aa" is managed; "bb" is some other producer. /v1.0/block/list
// returns the blocks newest-first (head = nonce 5, epoch 42) in one page.
func mockChain(t *testing.T) *httptest.Server {
	t.Helper()
	// nonce -> (producerBLS, validators array json), newest (5) to oldest (1).
	type blk struct {
		nonce             uint64
		producer, signers string
	}
	blocks := []blk{
		{5, "aa", `["aa","bb"]`}, // aa produced
		{4, "bb", `["aa","bb"]`}, // aa signed -> idle
		{3, "bb", `["bb"]`},      // aa elected but absent -> missed
		{2, "aa", `["aa","bb"]`}, // aa produced
		{1, "bb", `[]`},          // empty signer set -> idle (can't attribute a miss)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/block/list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_, _ = fmt.Fprint(w, `{"data":{"blocks":[]}}`)
			return
		}
		var parts []string
		for _, b := range blocks {
			parts = append(parts, fmt.Sprintf(`{"nonce":%d,"slot":%d,"epoch":42,"producerBLS":%q,"validators":%s}`,
				b.nonce, b.nonce, b.producer, b.signers))
		}
		_, _ = fmt.Fprintf(w, `{"data":{"blocks":[%s]}}`, strings.Join(parts, ","))
	})
	mux.HandleFunc("/v1.0/validator/list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_, _ = fmt.Fprint(w, `{"data":{"validators":[]}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"validators":[
			{"ownerAddress":"klv1own","blsPublicKey":"aa","name":"Community-Node-1","list":"elected",
			 "commission":3000,"selfStake":5000000,"accumulatedFees":1500000,
			 "leaderSuccessRate":{"numSuccess":2,"numFailure":1},
			 "validatorSuccessRate":{"numSuccess":3,"numFailure":1}},
			{"ownerAddress":"klv1other","blsPublicKey":"bb","name":"Other","list":"elected"}
		]}}`)
	})
	return httptest.NewServer(mux)
}

func TestMonitor_TimelineAndSummary(t *testing.T) {
	srv := mockChain(t)
	defer srv.Close()

	client := NewClient(srv.URL, 4)
	// Managed node uses 0x + uppercase to exercise BLS normalization.
	nodes := func() []ManagedNode {
		return []ManagedNode{{BLS: "0xAA", Name: "my-node"}}
	}
	m := NewMonitor(client, nodes, "mainnet", 5, 0)
	m.tick(context.Background())

	snap := m.Snapshot()
	if !snap.Ready {
		t.Fatalf("snapshot not ready: %q", snap.Error)
	}
	if snap.Epoch != 42 || snap.HeadNonce != 5 {
		t.Fatalf("epoch/head = %d/%d, want 42/5", snap.Epoch, snap.HeadNonce)
	}
	if len(snap.Validators) != 1 {
		t.Fatalf("expected 1 managed validator, got %d", len(snap.Validators))
	}
	v := snap.Validators[0]

	// On-chain name overrides the local node label once matched.
	if v.Name != "Community-Node-1" {
		t.Errorf("name = %q, want Community-Node-1", v.Name)
	}
	if v.State != "elected" {
		t.Errorf("state = %q, want elected", v.State)
	}
	if v.Commission != 30 { // 3000 basis points -> 30%
		t.Errorf("commission = %v, want 30", v.Commission)
	}
	if v.SelfStake != 5.0 { // 5000000 / 1e6
		t.Errorf("self_stake = %v, want 5", v.SelfStake)
	}
	if v.Produced != 2 || v.Missed != 1 || v.Signed != 3 || v.LeaderMisses != 1 {
		t.Errorf("counts produced/missed/signed/leaderMisses = %d/%d/%d/%d, want 2/1/3/1",
			v.Produced, v.Missed, v.Signed, v.LeaderMisses)
	}

	// Timeline ascending by nonce (1..5): idle, produced, missed, idle, produced.
	want := []string{"idle", "produced", "missed", "idle", "produced"}
	if len(v.Timeline) != len(want) {
		t.Fatalf("timeline len = %d, want %d", len(v.Timeline), len(want))
	}
	for i, cell := range v.Timeline {
		if cell.Nonce != uint64(i+1) {
			t.Errorf("cell %d nonce = %d, want %d", i, cell.Nonce, i+1)
		}
		if cell.Status != want[i] {
			t.Errorf("cell %d (nonce %d) status = %q, want %q", i, cell.Nonce, cell.Status, want[i])
		}
	}

	// Summary.
	if snap.Summary.Managed != 1 || snap.Summary.Elected != 1 || snap.Summary.Jailed != 0 {
		t.Errorf("summary managed/elected/jailed = %d/%d/%d, want 1/1/0",
			snap.Summary.Managed, snap.Summary.Elected, snap.Summary.Jailed)
	}
	if snap.Summary.Produced != 2 || snap.Summary.Missed != 1 {
		t.Errorf("summary produced/missed = %d/%d, want 2/1", snap.Summary.Produced, snap.Summary.Missed)
	}
	if snap.Summary.TotalStaking != 5.0 || snap.Summary.TotalAllowance != 1.5 {
		t.Errorf("summary staking/allowance = %v/%v, want 5/1.5", snap.Summary.TotalStaking, snap.Summary.TotalAllowance)
	}
}

func TestMonitor_UnmatchedNodeIsOffChain(t *testing.T) {
	srv := mockChain(t)
	defer srv.Close()

	client := NewClient(srv.URL, 4)
	nodes := func() []ManagedNode {
		return []ManagedNode{{BLS: "ff", Name: "ghost"}} // not in validator list
	}
	m := NewMonitor(client, nodes, "mainnet", 5, 0)
	m.tick(context.Background())

	snap := m.Snapshot()
	if len(snap.Validators) != 1 {
		t.Fatalf("expected 1 row, got %d", len(snap.Validators))
	}
	v := snap.Validators[0]
	if v.OnChain {
		t.Errorf("unmatched node should be off-chain")
	}
	if v.Name != "ghost" {
		t.Errorf("name = %q, want fallback ghost", v.Name)
	}
	// Off-chain validator never produced/missed in the window -> all idle.
	for _, c := range v.Timeline {
		if c.Status != "idle" {
			t.Errorf("nonce %d status = %q, want idle", c.Nonce, c.Status)
		}
	}
}

func TestClient_ParsesValidatorList(t *testing.T) {
	srv := mockChain(t)
	defer srv.Close()
	client := NewClient(srv.URL, 4)

	vals, err := client.Validators(context.Background())
	if err != nil {
		t.Fatalf("Validators: %v", err)
	}
	if len(vals) != 2 {
		t.Fatalf("got %d validators, want 2", len(vals))
	}
	if vals[0].BLSPublicKey != "aa" || vals[0].LeaderSuccessRate.NumSuccess != 2 {
		t.Errorf("parse mismatch: %+v", vals[0])
	}
}

type capturedMetric struct {
	nodeID, serverID string
	metrics          map[string]float64
}

type fakeMetricsWriter struct{ writes []capturedMetric }

func (f *fakeMetricsWriter) InsertNodeMetrics(nodeID, serverID string, metrics map[string]float64, ts int64) error {
	f.writes = append(f.writes, capturedMetric{nodeID, serverID, metrics})
	return nil
}

func TestMonitor_EmitsValidatorMetrics(t *testing.T) {
	srv := mockChain(t)
	defer srv.Close()

	client := NewClient(srv.URL, 4)
	nodes := func() []ManagedNode {
		return []ManagedNode{{ID: "node-1", ServerID: "srv-1", BLS: "0xAA", Name: "n1"}}
	}
	m := NewMonitor(client, nodes, "mainnet", 5, 0)
	sink := &fakeMetricsWriter{}
	m.SetMetricsWriter(sink)

	m.tick(context.Background())

	if len(sink.writes) != 1 {
		t.Fatalf("expected 1 metric write, got %d", len(sink.writes))
	}
	w := sink.writes[0]
	if w.nodeID != "node-1" || w.serverID != "srv-1" {
		t.Errorf("write ids = %s/%s, want node-1/srv-1", w.nodeID, w.serverID)
	}
	// Mock validator "aa": validatorSuccessRate.numFailure=1, leaderSuccessRate.numFailure=1, elected.
	if w.metrics[MetricMissedBlocks] != 1 {
		t.Errorf("%s = %v, want 1", MetricMissedBlocks, w.metrics[MetricMissedBlocks])
	}
	if w.metrics[MetricLeaderMisses] != 1 {
		t.Errorf("%s = %v, want 1", MetricLeaderMisses, w.metrics[MetricLeaderMisses])
	}
	if w.metrics[MetricJailed] != 0 {
		t.Errorf("%s = %v, want 0 (elected, not jailed)", MetricJailed, w.metrics[MetricJailed])
	}
}

func TestMonitor_NoMetricsForOffChainNode(t *testing.T) {
	srv := mockChain(t)
	defer srv.Close()

	client := NewClient(srv.URL, 4)
	nodes := func() []ManagedNode {
		return []ManagedNode{{ID: "node-x", ServerID: "srv-1", BLS: "ff", Name: "ghost"}}
	}
	m := NewMonitor(client, nodes, "mainnet", 5, 0)
	sink := &fakeMetricsWriter{}
	m.SetMetricsWriter(sink)

	m.tick(context.Background())

	if len(sink.writes) != 0 {
		t.Fatalf("off-chain node should emit no metrics, got %d", len(sink.writes))
	}
}

type fakeKV struct{ data map[string]string }

func newFakeKV() *fakeKV { return &fakeKV{data: map[string]string{}} }

func (f *fakeKV) Get(k string) (string, error) { return f.data[k], nil }
func (f *fakeKV) Set(k, v string) error        { f.data[k] = v; return nil }

func TestMonitor_TracksMonthlyElections(t *testing.T) {
	srv := mockChain(t)
	defer srv.Close()

	client := NewClient(srv.URL, 4)
	nodes := func() []ManagedNode {
		return []ManagedNode{{ID: "n1", ServerID: "s1", BLS: "0xAA", Name: "n1"}}
	}
	kv := newFakeKV()
	m := NewMonitor(client, nodes, "mainnet", 5, 0)
	m.SetElectionStore(kv)

	m.tick(context.Background())
	if got := m.Snapshot().Validators[0].ElectionsMonth; got != 1 {
		t.Fatalf("elections_month after first tick = %d, want 1", got)
	}

	// Same epoch on the next tick must not double-count.
	m.tick(context.Background())
	if got := m.Snapshot().Validators[0].ElectionsMonth; got != 1 {
		t.Errorf("elections_month after second tick (same epoch) = %d, want 1", got)
	}

	hist := m.ElectionHistory()
	if hist.CurrentMonth == "" {
		t.Error("expected a current month")
	}
	if hist.History[hist.CurrentMonth][normalizeBLS("0xAA")] != 1 {
		t.Errorf("history count = %v, want 1", hist.History[hist.CurrentMonth])
	}
	if kv.data[electionsKey] == "" {
		t.Error("election history was not persisted to the KV store")
	}
}

func TestMonitor_ElectionsSurviveRestart(t *testing.T) {
	srv := mockChain(t)
	defer srv.Close()
	client := NewClient(srv.URL, 4)
	nodes := func() []ManagedNode {
		return []ManagedNode{{ID: "n1", ServerID: "s1", BLS: "aa", Name: "n1"}}
	}
	kv := newFakeKV()

	m1 := NewMonitor(client, nodes, "mainnet", 5, 0)
	m1.SetElectionStore(kv)
	m1.tick(context.Background())

	// A fresh monitor sharing the KV (i.e. a restart) must not re-count the same
	// epoch — the persisted LastEpoch guards against it.
	m2 := NewMonitor(client, nodes, "mainnet", 5, 0)
	m2.SetElectionStore(kv)
	m2.tick(context.Background())
	if got := m2.Snapshot().Validators[0].ElectionsMonth; got != 1 {
		t.Errorf("elections_month after restart = %d, want 1 (no double count)", got)
	}
}

// TestClient_ValidatorsPaginates guards the bug where the API ignores pageSize
// (10/page) and we stopped after the first page. With limit honored, a full
// page (100) must trigger a fetch of the next page, where a managed validator
// might live. The mock returns 100 fillers on page 1 and the real one on page 2.
func TestClient_ValidatorsPaginates(t *testing.T) {
	socketResponder := func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		var sb strings.Builder
		sb.WriteString(`{"data":{"validators":[`)
		switch page {
		case "1":
			for i := 0; i < 100; i++ {
				if i > 0 {
					sb.WriteString(",")
				}
				fmt.Fprintf(&sb, `{"blsPublicKey":"filler%03d","list":"elected"}`, i)
			}
		case "2":
			sb.WriteString(`{"blsPublicKey":"deadbeef","name":"Mine","list":"elected"}`)
		}
		sb.WriteString(`]}}`)
		_, _ = io.WriteString(w, sb.String())
	}
	srv := httptest.NewServer(http.HandlerFunc(socketResponder))
	defer srv.Close()

	client := NewClient(srv.URL, 4)
	vals, err := client.Validators(context.Background())
	if err != nil {
		t.Fatalf("Validators: %v", err)
	}
	if len(vals) != 101 {
		t.Fatalf("expected 101 validators across 2 pages, got %d", len(vals))
	}
	found := false
	for _, v := range vals {
		if v.BLSPublicKey == "deadbeef" {
			found = true
		}
	}
	if !found {
		t.Error("page-2 validator was not fetched (pagination stopped early)")
	}
}

// TestMonitor_CountsElectionsWithFreshStatsOnEpochChange guards the bug where a
// new epoch was counted against a STALE validator set: a validator that only
// became elected in the new epoch was missed (count 0) while another was double
// counted. The fix forces a validator-stats refresh on epoch change.
func TestMonitor_CountsElectionsWithFreshStatsOnEpochChange(t *testing.T) {
	var mu sync.Mutex
	epoch := uint64(100)
	nonce := uint64(1000)
	ddElected := false

	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/block/list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_, _ = fmt.Fprint(w, `{"data":{"blocks":[]}}`)
			return
		}
		mu.Lock()
		e, n := epoch, nonce
		mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"data":{"blocks":[{"nonce":%d,"epoch":%d,"producerBLS":"aa","validators":["aa"]}]}}`, n, e)
	})
	mux.HandleFunc("/v1.0/validator/list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_, _ = fmt.Fprint(w, `{"data":{"validators":[]}}`)
			return
		}
		mu.Lock()
		dd := ddElected
		mu.Unlock()
		ddList := "waiting"
		if dd {
			ddList = "elected"
		}
		_, _ = fmt.Fprintf(w, `{"data":{"validators":[{"blsPublicKey":"aa","list":"elected"},{"blsPublicKey":"dd","list":%q}]}}`, ddList)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewClient(srv.URL, 4)
	nodes := func() []ManagedNode {
		return []ManagedNode{{ID: "a", BLS: "aa", Name: "A"}, {ID: "d", BLS: "dd", Name: "D"}}
	}
	m := NewMonitor(client, nodes, "mainnet", 1, 0)
	m.SetElectionStore(newFakeKV())

	// Epoch 100: only aa elected.
	m.tick(context.Background())
	// Epoch 101: dd is now elected too.
	mu.Lock()
	epoch, nonce, ddElected = 101, 1001, true
	mu.Unlock()
	m.tick(context.Background())

	hist := m.ElectionHistory()
	counts := hist.History[hist.CurrentMonth]
	if counts["aa"] != 2 {
		t.Errorf("aa elections = %d, want 2 (elected both epochs)", counts["aa"])
	}
	if counts["dd"] != 1 {
		t.Errorf("dd elections = %d, want 1 (counted on the epoch it became elected)", counts["dd"])
	}
}

// TestMonitor_DefersElectionCountWhenStatsStale guards against counting a new
// epoch against stale validator data when the stats fetch fails on the
// epoch-boundary tick (e.g. a 429). The epoch must be counted exactly once,
// after the stats refresh succeeds — never skipped, never doubled.
func TestMonitor_DefersElectionCountWhenStatsStale(t *testing.T) {
	var mu sync.Mutex
	failValidators := false

	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/block/list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_, _ = fmt.Fprint(w, `{"data":{"blocks":[]}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"blocks":[{"nonce":2000,"epoch":200,"producerBLS":"aa","validators":["aa"]}]}}`)
	})
	mux.HandleFunc("/v1.0/validator/list", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fail := failValidators
		mu.Unlock()
		if fail {
			// Any fetch failure exercises the defer path; a 404 avoids the
			// client's 429/503 backoff so the test stays fast.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("page") != "1" {
			_, _ = fmt.Fprint(w, `{"data":{"validators":[]}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"validators":[{"blsPublicKey":"aa","list":"elected"}]}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewClient(srv.URL, 4)
	nodes := func() []ManagedNode { return []ManagedNode{{ID: "a", BLS: "aa", Name: "A"}} }
	m := NewMonitor(client, nodes, "mainnet", 1, 0)
	m.SetElectionStore(newFakeKV())

	// Epoch 200 first observed while the validator endpoint is failing.
	mu.Lock()
	failValidators = true
	mu.Unlock()
	m.tick(context.Background())
	if got := m.ElectionHistory().History[m.ElectionHistory().CurrentMonth]["aa"]; got != 0 {
		t.Fatalf("count with stale stats = %d, want 0 (deferred)", got)
	}

	// Stats recover — the same epoch is now counted, exactly once.
	mu.Lock()
	failValidators = false
	mu.Unlock()
	m.tick(context.Background())
	m.tick(context.Background()) // a further tick must not double-count
	if got := m.ElectionHistory().History[m.ElectionHistory().CurrentMonth]["aa"]; got != 1 {
		t.Errorf("count after recovery = %d, want 1 (counted once)", got)
	}
}

func TestNormalizeBLS(t *testing.T) {
	cases := map[string]string{
		"0xAB":  "ab",
		"AB":    "ab",
		"  Ab ": "ab",
		"0xabc": "abc",
	}
	for in, want := range cases {
		if got := normalizeBLS(in); got != want {
			t.Errorf("normalizeBLS(%q) = %q, want %q", in, got, want)
		}
	}
}
