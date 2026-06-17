package klever

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// klvPrecision is KLV's on-chain decimal precision (1 KLV = 1e6 base units).
const klvPrecision = 1_000_000.0

// ManagedNode is the minimal view of a NodeHub-managed node the monitor needs:
// its IDs (for metric attribution), on-chain BLS key, and a human label.
type ManagedNode struct {
	ID       string
	ServerID string
	BLS      string
	Name     string
}

// NodesFunc returns the validators NodeHub currently manages. It is called on
// every poll so added/removed nodes are picked up without a restart.
type NodesFunc func() []ManagedNode

// MetricsWriter persists per-node validator metrics so the existing alert
// engine can fire rules on them (e.g. missed blocks, jailed). Satisfied by
// *store.MetricsStore.
type MetricsWriter interface {
	InsertNodeMetrics(nodeID, serverID string, metrics map[string]float64, ts int64) error
}

// Metric names emitted per managed validator node.
const (
	MetricMissedBlocks = "validator_missed_blocks" // missed signing this epoch
	MetricLeaderMisses = "validator_leader_misses" // rounds it was due to lead but didn't
	MetricJailed       = "validator_jailed"        // 1 if jailed, else 0
)

// electionsKey is the KV key under which the election history is persisted.
const electionsKey = "validator_elections"

// KVStore is the minimal persistence the monitor needs for election history.
// Satisfied by *store.SettingsStore.
type KVStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
}

// blockRec is the compact per-block record kept in the rolling window.
type blockRec struct {
	nonce       uint64
	producerBLS string              // normalized
	signers     map[string]struct{} // normalized consensus group
	hasSigners  bool
}

// Monitor polls the Klever chain and maintains, for the managed validators, a
// rolling window of the last N blocks (produced / missed / idle) plus the
// authoritative per-epoch counters from the validator list.
type Monitor struct {
	client   *Client
	nodes    NodesFunc
	network  string
	window   int
	interval time.Duration

	maxPerTick int
	statsEvery int
	metrics    MetricsWriter

	// Election history persistence. electKV/elect are written only by the single
	// tick goroutine; reads in buildLocked/ElectionHistory happen under m.mu.
	electKV     KVStore
	elect       *ElectionHistory
	electLoaded bool

	mu         sync.RWMutex
	have       map[uint64]blockRec
	validators map[string]RawValidator // normalized BLS -> stats
	overview   Overview
	ticks      int
	lastErr    string
	latest     *Snapshot
}

// NewMonitor creates a monitor. window is how many recent blocks the timeline
// keeps; interval is the poll cadence.
func NewMonitor(client *Client, nodes NodesFunc, network string, window int, interval time.Duration) *Monitor {
	if window < 1 {
		window = 100
	}
	if interval <= 0 {
		interval = 6 * time.Second
	}
	return &Monitor{
		client:     client,
		nodes:      nodes,
		network:    network,
		window:     window,
		interval:   interval,
		maxPerTick: 30,
		statsEvery: 5, // refresh validator stats every 5 ticks (~30s)
		have:       make(map[uint64]blockRec),
		validators: make(map[string]RawValidator),
	}
}

// SetMetricsWriter wires a sink for per-validator metrics. Optional; when nil,
// the monitor still serves snapshots but emits no metrics for the alert engine.
func (m *Monitor) SetMetricsWriter(w MetricsWriter) {
	m.metrics = w
}

// SetElectionStore wires persistence for the monthly election history. Optional;
// when nil, the monitor doesn't track elections.
func (m *Monitor) SetElectionStore(kv KVStore) {
	m.electKV = kv
}

// ElectionHistory returns a copy of the persisted monthly election history.
func (m *Monitor) ElectionHistory() ElectionHistory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := ElectionHistory{History: map[string]map[string]int{}}
	if m.elect == nil {
		return out
	}
	out.CurrentMonth = m.elect.CurrentMonth
	out.LastEpoch = m.elect.LastEpoch
	for month, counts := range m.elect.History {
		cp := make(map[string]int, len(counts))
		for k, v := range counts {
			cp[k] = v
		}
		out.History[month] = cp
	}
	return out
}

// ensureElectionsLoaded loads the persisted history on first use. Called only
// from the tick goroutine before the lock is taken.
func (m *Monitor) ensureElectionsLoaded() {
	if m.electLoaded || m.electKV == nil {
		return
	}
	m.electLoaded = true
	e := &ElectionHistory{History: map[string]map[string]int{}}
	if raw, err := m.electKV.Get(electionsKey); err == nil && raw != "" {
		var loaded ElectionHistory
		if json.Unmarshal([]byte(raw), &loaded) == nil {
			if loaded.History == nil {
				loaded.History = map[string]map[string]int{}
			}
			e = &loaded
		}
	}
	m.mu.Lock()
	m.elect = e
	m.mu.Unlock()
}

// updateElectionsLocked counts one election per elected managed validator when a
// new epoch is observed, into the current calendar month's bucket. Returns true
// if the history changed and should be persisted. Caller holds m.mu.
func (m *Monitor) updateElectionsLocked(month string, epoch uint64, managed []ManagedNode) bool {
	if m.elect == nil {
		return false
	}
	dirty := false
	if m.elect.CurrentMonth != month {
		m.elect.CurrentMonth = month
		dirty = true
	}
	if epoch > m.elect.LastEpoch {
		bucket := m.elect.History[month]
		if bucket == nil {
			bucket = map[string]int{}
			m.elect.History[month] = bucket
		}
		for _, node := range managed {
			bls := normalizeBLS(node.BLS)
			if bls == "" {
				continue
			}
			if v, ok := m.validators[bls]; ok && v.List == "elected" {
				bucket[bls]++
			}
		}
		m.elect.LastEpoch = epoch
		dirty = true
	}
	return dirty
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

// Snapshot returns the latest rendered snapshot, or a not-ready placeholder
// before the first successful poll.
func (m *Monitor) Snapshot() *Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.latest != nil {
		return m.latest
	}
	return &Snapshot{Network: m.network, Window: m.window, Ready: false, Error: m.lastErr}
}

func (m *Monitor) tick(ctx context.Context) {
	ov, err := m.client.Overview(ctx)
	if err != nil {
		m.recordError(err)
		return
	}
	head := ov.Nonce
	if head == 0 {
		m.recordError(errString("chain overview returned head nonce 0"))
		return
	}

	var desiredStart uint64
	if head >= uint64(m.window) {
		desiredStart = head - uint64(m.window) + 1
	}

	// Decide which block nonces we still need (newest first), and whether this
	// tick should also refresh the validator stats.
	m.mu.Lock()
	m.pruneLocked(desiredStart, head)
	missing := make([]uint64, 0, m.maxPerTick)
	for n := head; n >= desiredStart && len(missing) < m.maxPerTick; n-- {
		if _, ok := m.have[n]; !ok {
			missing = append(missing, n)
		}
		if n == 0 {
			break
		}
	}
	refreshStats := m.ticks%m.statsEvery == 0 || len(m.validators) == 0
	m.mu.Unlock()

	// Fetch outside the lock.
	fetched := make([]blockRec, 0, len(missing))
	for _, n := range missing {
		blk, err := m.client.BlockByNonce(ctx, n)
		if err != nil {
			log.Printf("validator-monitor: block %d: %v", n, err)
			continue
		}
		fetched = append(fetched, toRec(blk))
	}

	var vals []RawValidator
	if refreshStats {
		if v, err := m.client.Validators(ctx); err != nil {
			log.Printf("validator-monitor: validator list: %v", err)
		} else {
			vals = v
		}
	}

	managed := m.nodes()
	m.ensureElectionsLoaded()
	month := time.Now().Format("2006-01")

	m.mu.Lock()
	for _, r := range fetched {
		m.have[r.nonce] = r
	}
	m.pruneLocked(desiredStart, head)
	if vals != nil {
		nv := make(map[string]RawValidator, len(vals))
		for _, v := range vals {
			if v.BLSPublicKey != "" {
				nv[normalizeBLS(v.BLSPublicKey)] = v
			}
		}
		m.validators = nv
	}
	m.overview = *ov
	m.ticks++
	m.lastErr = ""
	electionsDirty := m.updateElectionsLocked(month, ov.EpochNumber, managed)
	m.latest = m.buildLocked(head, managed)
	stats := m.validators
	var electPayload []byte
	if electionsDirty {
		electPayload, _ = json.Marshal(m.elect)
	}
	m.mu.Unlock()

	m.emitMetrics(managed, stats)
	if electPayload != nil && m.electKV != nil {
		if err := m.electKV.Set(electionsKey, string(electPayload)); err != nil {
			log.Printf("validator-monitor: persist elections: %v", err)
		}
	}
}

// emitMetrics writes per-node validator metrics so the alert engine can fire
// rules (missed blocks, jailed) through the normal pipeline. Best-effort.
func (m *Monitor) emitMetrics(managed []ManagedNode, stats map[string]RawValidator) {
	if m.metrics == nil {
		return
	}
	ts := nowUnix()
	for _, node := range managed {
		if node.ID == "" {
			continue
		}
		v, ok := stats[normalizeBLS(node.BLS)]
		if !ok {
			continue // not on chain (yet) — nothing authoritative to report
		}
		jailed := 0.0
		if v.List == "jailed" {
			jailed = 1.0
		}
		metrics := map[string]float64{
			MetricMissedBlocks: float64(v.ValidatorSuccessRate.NumFailure),
			MetricLeaderMisses: float64(v.LeaderSuccessRate.NumFailure),
			MetricJailed:       jailed,
		}
		if err := m.metrics.InsertNodeMetrics(node.ID, node.ServerID, metrics, ts); err != nil {
			log.Printf("validator-monitor: write metrics for node %s: %v", node.ID, err)
		}
	}
}

// pruneLocked drops blocks outside the current [start, head] window.
func (m *Monitor) pruneLocked(start, head uint64) {
	for n := range m.have {
		if n < start || n > head {
			delete(m.have, n)
		}
	}
}

func (m *Monitor) recordError(err error) {
	m.mu.Lock()
	m.lastErr = err.Error()
	if m.latest != nil {
		// Surface the error but keep serving the last good snapshot.
		cp := *m.latest
		cp.Error = err.Error()
		m.latest = &cp
	}
	m.mu.Unlock()
	log.Printf("validator-monitor: %v", err)
}

// buildLocked renders the snapshot from current state. Caller holds m.mu.
func (m *Monitor) buildLocked(head uint64, managed []ManagedNode) *Snapshot {
	// Ordered window nonces (oldest -> newest) so the timeline reads left-to-right.
	nonces := make([]uint64, 0, len(m.have))
	for n := range m.have {
		nonces = append(nonces, n)
	}
	sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })

	snap := &Snapshot{
		Epoch:     m.overview.EpochNumber,
		HeadNonce: head,
		Window:    m.window,
		Network:   m.network,
		UpdatedAt: nowUnix(),
		Ready:     true,
	}

	seen := make(map[string]struct{})
	for _, node := range managed {
		bls := normalizeBLS(node.BLS)
		if bls == "" {
			continue
		}
		if _, dup := seen[bls]; dup {
			continue
		}
		seen[bls] = struct{}{}

		v, onChain := m.validators[bls]
		state := "unknown"
		if onChain {
			state = v.List
		}
		elected := state == "elected"

		electionsMonth := 0
		if m.elect != nil {
			if bucket := m.elect.History[m.elect.CurrentMonth]; bucket != nil {
				electionsMonth = bucket[bls]
			}
		}

		vv := ValidatorView{
			BLS:            node.BLS,
			Name:           firstNonEmpty(v.Name, node.Name),
			NodeName:       node.Name,
			State:          state,
			OnChain:        onChain,
			ElectionsMonth: electionsMonth,
			Commission:     float64(v.Commission) / 100.0, // basis points -> percent
			SelfStake:      float64(v.SelfStake) / klvPrecision,
			Allowance:      float64(v.AccumulatedFees) / klvPrecision,
			Produced:       v.LeaderSuccessRate.NumSuccess,
			LeaderMisses:   v.LeaderSuccessRate.NumFailure,
			Signed:         v.ValidatorSuccessRate.NumSuccess,
			Missed:         v.ValidatorSuccessRate.NumFailure,
			Timeline:       make([]TimelineCell, 0, len(nonces)),
		}

		for _, n := range nonces {
			rec := m.have[n]
			status := "idle"
			switch {
			case rec.producerBLS == bls:
				status = "produced"
			case elected && rec.hasSigners:
				if _, signed := rec.signers[bls]; !signed {
					status = "missed"
				}
			}
			vv.Timeline = append(vv.Timeline, TimelineCell{Nonce: n, Status: status})
		}

		snap.Validators = append(snap.Validators, vv)

		snap.Summary.Managed++
		snap.Summary.TotalStaking += vv.SelfStake
		snap.Summary.TotalAllowance += vv.Allowance
		snap.Summary.Produced += vv.Produced
		snap.Summary.Missed += vv.Missed
		switch state {
		case "elected":
			snap.Summary.Elected++
		case "jailed":
			snap.Summary.Jailed++
		}
	}

	sort.Slice(snap.Validators, func(i, j int) bool {
		return strings.ToLower(snap.Validators[i].Name) < strings.ToLower(snap.Validators[j].Name)
	})
	return snap
}

func toRec(blk *IndexerBlock) blockRec {
	r := blockRec{
		nonce:       blk.Nonce,
		producerBLS: normalizeBLS(blk.ProducerBLS),
	}
	if len(blk.Validators) > 0 {
		r.hasSigners = true
		r.signers = make(map[string]struct{}, len(blk.Validators))
		for _, s := range blk.Validators {
			r.signers[normalizeBLS(s)] = struct{}{}
		}
	}
	return r
}

// normalizeBLS lowercases the key and strips an optional 0x prefix so keys from
// the PEM header and the API compare equal.
func normalizeBLS(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimPrefix(s, "0x")
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

type errString string

func (e errString) Error() string { return string(e) }
