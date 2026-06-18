package klever

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultAPIURLs returns the indexer (api) and node API base URLs for a network.
// Unknown networks fall back to mainnet.
func DefaultAPIURLs(network string) (apiURL, nodeURL string) {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "testnet":
		return "https://api.testnet.klever.org", "https://node.testnet.klever.org"
	default:
		return "https://api.mainnet.klever.org", "https://node.mainnet.klever.org"
	}
}

// minRequestInterval is the minimum spacing between outgoing Klever requests.
// Klever rate-limits per IP (and the node API is stricter than the indexer), so
// we smooth bursts — especially the initial block backfill — to stay under it.
const minRequestInterval = 175 * time.Millisecond

// Client is a thin HTTP client over the Klever indexer API.
//
// Requests are both capped in concurrency (semaphore) and paced to a minimum
// interval, because Klever rate-limits per IP and the monitor shares that
// budget across all its calls.
type Client struct {
	http *http.Client
	sem  chan struct{}

	urlMu  sync.RWMutex
	apiURL string // guarded by urlMu so the indexer can be swapped live

	paceMu  sync.Mutex
	lastReq time.Time
}

// NewClient builds a Client. apiURL serves the indexer (blocks + validators).
// maxInflight caps concurrent requests (min 1).
func NewClient(apiURL string, maxInflight int) *Client {
	if maxInflight < 1 {
		maxInflight = 1
	}
	return &Client{
		http:   &http.Client{Timeout: 15 * time.Second},
		apiURL: strings.TrimRight(apiURL, "/"),
		sem:    make(chan struct{}, maxInflight),
	}
}

// SetBaseURL swaps the indexer API base URL at runtime (e.g. when the operator
// configures their own indexer in Settings). No-op for an empty URL.
func (c *Client) SetBaseURL(apiURL string) {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if apiURL == "" {
		return
	}
	c.urlMu.Lock()
	c.apiURL = apiURL
	c.urlMu.Unlock()
}

// baseURL returns the current indexer API base URL.
func (c *Client) baseURL() string {
	c.urlMu.RLock()
	defer c.urlMu.RUnlock()
	return c.apiURL
}

// pace blocks until at least minRequestInterval has elapsed since the previous
// request was dispatched, reserving the slot so concurrent callers stay spaced.
func (c *Client) pace(ctx context.Context) {
	c.paceMu.Lock()
	now := time.Now()
	earliest := c.lastReq.Add(minRequestInterval)
	if now.Before(earliest) {
		c.lastReq = earliest
		c.paceMu.Unlock()
		t := time.NewTimer(time.Until(earliest))
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
		}
		return
	}
	c.lastReq = now
	c.paceMu.Unlock()
}

// RecentBlocks returns a page of recent blocks (newest-first), each with its
// producer and consensus group. One call batches up to `limit` blocks, so the
// timeline window is fetched in a request or two — and blocks[0] is the chain
// head (nonce + epoch), which removes any need for the node API.
func (c *Client) RecentBlocks(ctx context.Context, page, limit int) ([]IndexerBlock, error) {
	url := fmt.Sprintf("%s/v1.0/block/list?page=%d&limit=%d", c.baseURL(), page, limit)
	var env blocksEnvelope
	if err := c.getJSON(ctx, url, &env); err != nil {
		return nil, err
	}
	return env.Data.Blocks, nil
}

// Validators returns the full validator list across pages. Returning every
// entry — not just elected ones — lets the monitor resolve a managed validator's
// stats whatever its state (elected, waiting, eligible, or jailed).
//
// NB: the page size is controlled by `limit`. The API silently ignores
// `pageSize` and falls back to 10 per page, which previously made this stop
// after the first 10 validators and report everyone else as off-chain. maxPages
// is a safety backstop well above the real validator count (~200).
func (c *Client) Validators(ctx context.Context) ([]RawValidator, error) {
	const limit = 100
	const maxPages = 25
	var all []RawValidator
	for page := 1; page <= maxPages; page++ {
		url := fmt.Sprintf("%s/v1.0/validator/list?page=%d&limit=%d", c.baseURL(), page, limit)
		var env validatorListEnvelope
		if err := c.getJSON(ctx, url, &env); err != nil {
			return nil, err
		}
		all = append(all, env.Data.Validators...)
		if len(env.Data.Validators) < limit {
			break // last page
		}
	}
	return all, nil
}

// getJSON performs a paced GET through the in-flight limiter, retrying on
// 429/503 with the server's Retry-After (capped) or exponential backoff.
func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	const maxRetries = 4
	const maxBackoff = 8 * time.Second
	for attempt := 0; ; attempt++ {
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		c.pace(ctx)
		status, body, retryAfter, err := c.do(ctx, url)
		<-c.sem
		if err != nil {
			return err
		}
		if (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) && attempt < maxRetries {
			wait := retryAfter
			if wait <= 0 {
				wait = time.Duration(500*(1<<attempt)) * time.Millisecond // 0.5s, 1s, 2s, 4s
			}
			if wait > maxBackoff {
				wait = maxBackoff
			}
			t := time.NewTimer(wait)
			select {
			case <-t.C:
				continue
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			}
		}
		if status != http.StatusOK {
			return fmt.Errorf("klever GET %s: HTTP %d", url, status)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s: %w", url, err)
		}
		return nil
	}
}

// do issues the request and returns status, body, and the parsed Retry-After
// delay (0 if absent/unparseable).
func (c *Client) do(ctx context.Context, url string) (int, []byte, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, retryAfter, err
	}
	return resp.StatusCode, body, retryAfter, nil
}

// parseRetryAfter reads a Retry-After header expressed in seconds (the form
// Klever uses). Non-numeric / empty values yield 0.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}
