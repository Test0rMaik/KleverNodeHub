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
	epoch       uint64
	producerBLS string              // normalized
	signers     map[string]struct{} // normalized consensus group (= elected set)
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
	apiURLFn   func() string // optional: resolves the indexer URL each tick (live override)

	// Election history persistence. electKV/elect are written only by the single
	// tick goroutine; reads in buildLocked/ElectionHistory happen under m.mu.
	electKV     KVStore
	elect       *ElectionHistory
	electLoaded bool

	// nextTickStatRefresh is set (by the tick goroutine only) when a new epoch
	// election is counted so the validator list is re-fetched on the very next
	// tick. This gives the Klever API a second chance to show the updated elected
	// state even if it lagged behind on the tick when the epoch was first observed.
	nextTickStatRefresh bool

	mu         sync.RWMutex
	have       map[uint64]blockRec
	validators map[string]RawValidator // normalized BLS -> stats
	headNonce  uint64
	epoch      uint64
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
		maxPerTick: 8, // gentle backfill: fills the 100-block window over a few ticks
		statsEvery: 5, // refresh validator stats every 5 ticks
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

// SetAPIURLProvider sets an optional resolver for the indexer API base URL,
// consulted at the start of every poll so an operator's custom indexer (set in
// Settings) takes effect live, without a restart.
func (m *Monitor) SetAPIURLProvider(fn func() string) {
	m.apiURLFn = fn
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
// new epoch is observed, into the current month's bucket. Returns true if the
// history changed and should be persisted. Caller holds m.mu.
//
// The elected set is taken from the block consensus group (the per-block
// `validators` array) of this epoch's blocks — not the validator list. The
// validator list can still report the PREVIOUS epoch's elected set for a short
// window after a boundary, which previously over-counted (e.g. an epoch with 1
// elected validator credited 2). Block data is authoritative and lag-free.
func (m *Monitor) updateElectionsLocked(month string, epoch uint64, managed []ManagedNode) bool {
	if m.elect == nil {
		return false
	}
	dirty := false
	if m.elect.CurrentMonth != month {
		m.elect.CurrentMonth = month
		dirty = true
	}
	if epoch <= m.elect.LastEpoch {
		return dirty
	}

	// Elected set for this epoch = union of the consensus groups of this epoch's
	// blocks currently in the window (every block carries the full elected set).
	elected := make(map[string]struct{})
	for _, rec := range m.have {
		if rec.epoch == epoch && rec.hasSigners {
			for s := range rec.signers {
				elected[s] = struct{}{}
			}
		}
	}
	if len(elected) == 0 {
		return dirty // no consensus data for this epoch yet; count on a later tick
	}

	bucket := m.elect.History[month]
	if bucket == nil {
		bucket = map[string]int{}
		m.elect.History[month] = bucket
	}
	seen := make(map[string]struct{})
	for _, node := range managed {
		bls := normalizeBLS(node.BLS)
		if bls == "" {
			continue
		}
		if _, dup := seen[bls]; dup {
			continue // two node records with the same BLS key must count once
		}
		seen[bls] = struct{}{}
		if _, ok := elected[bls]; ok {
			bucket[bls]++
		}
	}
	m.elect.LastEpoch = epoch
	dirty = true
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

// blockChunk is how many blocks one block/list request fetches. The API caps a
// page at 100 and the window is 100, so a single request covers the whole window
// (no second page in steady state). Older pages are only fetched while the
// window is still backfilling after startup.
const blockChunk = 100

func (m *Monitor) tick(ctx context.Context) {
	// Apply a live indexer-URL override (operator's own indexer) before fetching.
	if m.apiURLFn != nil {
		m.client.SetBaseURL(m.apiURLFn())
	}

	// One batched call gets the newest chunk of blocks; blocks[0] is the head
	// (nonce + epoch), so there's no separate node-API overview request.
	newest, err := m.client.RecentBlocks(ctx, 1, blockChunk)
	if err != nil {
		m.recordError(err)
		return
	}
	if len(newest) == 0 {
		m.recordError(errString("block list returned no blocks"))
		return
	}
	head := newest[0].Nonce
	epoch := newest[0].Epoch
	if head == 0 {
		m.recordError(errString("block list head nonce 0"))
		return
	}

	var desiredStart uint64
	if head >= uint64(m.window) {
		desiredStart = head - uint64(m.window) + 1
	}

	m.ensureElectionsLoaded()

	// Merge the newest chunk; decide whether to backfill older pages and whether
	// to refresh validator stats (also forced on an epoch change so the election
	// count and elected/jailed state are attributed with fresh data).
	m.mu.Lock()
	mergeBlocks(m.have, newest, desiredStart, head)
	m.pruneLocked(desiredStart, head)
	missing := countMissing(m.have, desiredStart, head)
	// epochChanged: true when the chain epoch advances. Two conditions are OR'd:
	//   1. epoch != m.epoch — fires exactly once on the first tick of each new
	//      epoch (m.epoch holds the previous tick's value, written in the second
	//      lock section below — that ordering must be preserved).
	//   2. m.elect != nil && epoch > m.elect.LastEpoch — keeps firing on every
	//      subsequent tick of the same epoch until the election is successfully
	//      counted. This covers the deferred-election case where the first tick
	//      has no consensus signer data yet (len(elected)==0), so LastEpoch is
	//      not advanced and the validator list must keep refreshing until it is.
	epochChanged := epoch != m.epoch || (m.elect != nil && epoch > m.elect.LastEpoch)
	nextStatRefresh := m.nextTickStatRefresh
	m.nextTickStatRefresh = false // consumed; may be re-set below if election counted
	refreshStats := m.ticks%m.statsEvery == 0 || len(m.validators) == 0 || epochChanged || nextStatRefresh
	m.mu.Unlock()

	// Backfill older pages only while the window has gaps (i.e. on startup),
	// capped per tick so it fills gently over a few ticks.
	var backfill []IndexerBlock
	if missing > 0 {
		neededPages := (m.window + blockChunk - 1) / blockChunk
		for page := 2; page <= neededPages && page <= 1+m.maxPerTick; page++ {
			older, err := m.client.RecentBlocks(ctx, page, blockChunk)
			if err != nil {
				log.Printf("validator-monitor: block list page %d: %v", page, err)
				break
			}
			if len(older) == 0 {
				break
			}
			backfill = append(backfill, older...)
			if older[len(older)-1].Nonce <= desiredStart {
				break
			}
		}
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
	month := time.Now().Format("2006-01")

	m.mu.Lock()
	mergeBlocks(m.have, backfill, desiredStart, head)
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
	m.headNonce = head
	m.epoch = epoch
	m.ticks++
	m.lastErr = ""
	prevLastEpoch := uint64(0)
	if m.elect != nil {
		prevLastEpoch = m.elect.LastEpoch
	}
	electionsDirty := m.updateElectionsLocked(month, epoch, managed)
	// If a new epoch was just counted the validator list fetch that happened on
	// this tick may have been stale (API lag at epoch boundaries). Force one
	// extra fetch on the next tick so the displayed state updates promptly.
	if m.elect != nil && m.elect.LastEpoch > prevLastEpoch {
		m.nextTickStatRefresh = true
	}
	m.latest = m.buildLocked(head, desiredStart, managed)
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

// mergeBlocks records each block within [start, head] into the window map.
func mergeBlocks(have map[uint64]blockRec, blocks []IndexerBlock, start, head uint64) {
	for i := range blocks {
		b := &blocks[i]
		if b.Nonce < start || b.Nonce > head {
			continue
		}
		have[b.Nonce] = toRec(b)
	}
}

// countMissing returns how many nonces in [start, head] are absent from have.
func countMissing(have map[uint64]blockRec, start, head uint64) int {
	missing := 0
	for n := start; n <= head; n++ {
		if _, ok := have[n]; !ok {
			missing++
		}
	}
	return missing
}

// emitMetrics writes per-node validator metrics so the alert engine can fire
// rules (missed blocks, jailed) through the normal pipeline. Best-effort.
func (m *Monitor) emitMetrics(managed []ManagedNode, stats map[string]RawValidator) {
	if m.metrics == nil {
		return
	}
	// Subtract jailed validators' leader misses from the epoch-level chain counter
	// so alert thresholds on MetricMissedBlocks are not tripped by phantom misses
	// from skipped rounds. This is an approximation; the display counter uses the
	// timeline for exact results, but the timeline is not available here.
	var jailedLeaderMisses int64
	for _, val := range stats {
		if val.List == "jailed" {
			jailedLeaderMisses += val.LeaderSuccessRate.NumFailure
		}
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
		missed := v.ValidatorSuccessRate.NumFailure
		if v.List == "elected" {
			missed -= jailedLeaderMisses
			if missed < 0 {
				missed = 0
			}
		}
		metrics := map[string]float64{
			MetricMissedBlocks: float64(missed),
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
// windowStart is desiredStart from tick() — the first nonce of the rolling
// window as already used for mergeBlocks/pruneLocked, passed in to avoid
// recomputing the same formula here.
func (m *Monitor) buildLocked(head, windowStart uint64, managed []ManagedNode) *Snapshot {
	// Floor at 1: nonce 0 is never a valid Klever block (tick() rejects head==0),
	// so including it in the timeline would produce a spurious "skipped" cell
	// whenever head < m.window (young chain / testnet).
	if windowStart == 0 {
		windowStart = 1
	}

	// Only label absent nonces "skipped" when the window is small enough for
	// backfill to cover it in a single tick. If window > blockChunk*(1+maxPerTick),
	// the oldest pages are never fetched and absent nonces would be falsely
	// "skipped" forever. In that case fall back to "idle" (same as pre-PR).
	canMarkSkipped := (m.window+blockChunk-1)/blockChunk <= 1+m.maxPerTick

	snap := &Snapshot{
		Epoch:     m.epoch,
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
			ChainMissed:    v.ValidatorSuccessRate.NumFailure,
			Timeline:       make([]TimelineCell, 0, m.window),
		}

		// Iterate the full nonce range. Absent nonces appear as "skipped" (jailed
		// leader — block was never produced). "missed" means a block existed and
		// the validator was in the elected signer set but absent from the actual
		// signer list. vv.Missed counts only "missed" cells: skipped rounds never
		// inflate it, so phantom epoch-counter inflation from jailed peers is
		// automatically excluded.
		for n := windowStart; n <= head; n++ {
			rec, exists := m.have[n]
			status := "idle"
			switch {
			case !exists && canMarkSkipped:
				status = "skipped"
			case rec.producerBLS == bls:
				status = "produced"
			case elected && rec.hasSigners:
				if _, signed := rec.signers[bls]; !signed {
					status = "missed"
				}
			}
			if status == "missed" {
				vv.Missed++
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
		epoch:       blk.Epoch,
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
