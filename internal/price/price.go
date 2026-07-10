// Package price fetches HASH-USD trades from the internal Figure Markets exchange
// API and aggregates them into a single volume-weighted average price (VWAP) over
// a trailing 7-day window (ending midnight Eastern). This 7-day VWAP is the agreed
// aggregation method for the conversion factor.
package price

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"
)

// ErrNoTrades is returned by GetPrice when the fetch succeeded but the trailing
// window contained no trades. Callers should treat it as "no update possible
// today, try again tomorrow" — not a failure that warrants paging.
var ErrNoTrades = errors.New("no trades in window")

const (
	// DefaultBaseURL is the internal Figure Markets HASH-USD trades endpoint.
	DefaultBaseURL = "https://www.figuremarkets.com/service-hft-exchange/api/v1/trades/HASH-USD"
	// defaultPageSize is the max trades per page the API returns.
	// The API limits this to 200 max.
	defaultPageSize = 200
	// windowDays is the trailing window (ending midnight Eastern) to aggregate.
	windowDays = 7
	// maxResponseBytes caps a single price-page response body. 200 trades at
	// ~500 bytes each is ~100 KiB; 32 MiB gives 300× headroom without letting
	// a runaway upstream OOM the pod.
	maxResponseBytes = 32 << 20 // 32 MiB
	// timeFormat is the nanosecond timestamp format used by the API.
	timeFormat = "2006-01-02T15:04:05.000000000Z"
)

// numStr stores API decimal (string or number) as-is for exact big.Rat parsing.
type numStr string

// Match is a single trade entry from the API.
type Match struct {
	ID               string `json:"id"`
	Price            numStr `json:"price"`
	Quantity         numStr `json:"quantity"`
	Created          string `json:"created"`
	SettlementTxHash string `json:"settlementTxHash"`
}

// response is the top-level API response shape.
type response struct {
	Denom    string  `json:"denom"`
	Symbol   string  `json:"symbol"`
	MarketID string  `json:"marketId"`
	Matches  []Match `json:"matches"`
}

// Client fetches and aggregates HASH-USD trades. The zero value is not usable;
// construct one with New.
type Client struct {
	BaseURL          string
	HTTP             *http.Client
	PageSize         int
	WindowDays       int
	MaxRetries       int           // additional attempts on transient failures
	RetryWait        time.Duration // base backoff between retries
	MaxResponseBytes int64         // cap on a single price-page response body
	// Now allows tests to pin the window. Defaults to time.Now.
	Now func() time.Time
}

// New returns a Client with sensible defaults.
func New() *Client {
	return &Client{
		BaseURL:          DefaultBaseURL,
		HTTP:             &http.Client{Timeout: 15 * time.Second},
		PageSize:         defaultPageSize,
		WindowDays:       windowDays,
		MaxRetries:       3,
		RetryWait:        500 * time.Millisecond,
		MaxResponseBytes: maxResponseBytes,
		Now:              time.Now,
	}
}

// Result is the aggregated price plus context for logging/auditing.
type Result struct {
	// PriceUSDPerHASH is the volume-weighted average price (USD per HASH) as an
	// exact rational.
	PriceUSDPerHASH *big.Rat
	// VolumeHASH is the total quantity traded across the window (HASH). Used
	// by liquidity guards to refuse thin-book updates.
	VolumeHASH *big.Rat
	// Trades is the number of trades aggregated.
	Trades int
	// WindowStart and WindowEnd bound the trades considered (UTC).
	WindowStart time.Time
	WindowEnd   time.Time
}

// GetPrice fetches all trades in the trailing window and returns their VWAP.
func (c *Client) GetPrice(ctx context.Context) (Result, error) {
	start, end, err := c.window()
	if err != nil {
		return Result{}, err
	}
	matches, err := c.fetchAll(ctx, start, end)
	if err != nil {
		return Result{}, err
	}
	if len(matches) == 0 {
		return Result{}, fmt.Errorf("%w %s..%s", ErrNoTrades,
			start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	vwap, err := VWAP(matches)
	if err != nil {
		return Result{}, err
	}
	volume, err := totalVolume(matches)
	if err != nil {
		return Result{}, err
	}
	return Result{
		PriceUSDPerHASH: vwap,
		VolumeHASH:      volume,
		Trades:          len(matches),
		WindowStart:     start,
		WindowEnd:       end,
	}, nil
}

// totalVolume sums the (positive) quantities across all trades. Trades with
// non-positive or unparseable quantities are excluded — matching VWAP's
// treatment — so the reported volume corresponds to the trades that actually
// participated in the average.
func totalVolume(matches []Match) (*big.Rat, error) {
	total := new(big.Rat)
	for i, m := range matches {
		q, ok := new(big.Rat).SetString(string(m.Quantity))
		if !ok {
			return nil, fmt.Errorf("trade %d: invalid quantity %q", i, m.Quantity)
		}
		if q.Sign() <= 0 {
			continue
		}
		total.Add(total, q)
	}
	return total, nil
}

// window returns the [start, end) UTC bounds: the WindowDays ending at midnight
// Eastern of the current date.
func (c *Client) window() (time.Time, time.Time, error) {
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("cannot load timezone America/New_York: %w", err)
	}
	now := c.Now().In(eastern)
	endLocal := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endUTC := endLocal.UTC()
	startUTC := endUTC.AddDate(0, 0, -c.WindowDays)
	return startUTC, endUTC, nil
}

// fetchAll paginates through all trades between start and end. It advances the
// cursor to the NEWEST trade's timestamp seen in the batch — not the last
// element's — so pagination is correct whether the API returns each page
// ascending, descending, or unsorted. A composite dedupe key drops the
// boundary overlap; trades outside [start, end) are discarded defensively.
func (c *Client) fetchAll(ctx context.Context, start, end time.Time) ([]Match, error) {
	var all []Match
	seen := make(map[string]struct{})
	cursor := start
	for {
		batch, err := c.fetchPage(ctx, cursor, end)
		if err != nil {
			return nil, err
		}

		added := 0
		var maxCreated time.Time // newest Created seen in this batch
		for _, m := range batch {
			t, err := time.Parse(timeFormat, m.Created)
			if err != nil {
				return nil, fmt.Errorf("parse created time %q: %w", m.Created, err)
			}
			if t.After(maxCreated) {
				maxCreated = t
			}
			key := dedupeKey(m)
			if _, dup := seen[key]; dup {
				continue
			}
			if t.Before(start) || !t.Before(end) {
				continue // API returned a trade outside the requested window; ignore
			}
			seen[key] = struct{}{}
			all = append(all, m)
			added++
		}

		// Stop when the page is short (nothing more to fetch) or when we
		// made no forward progress (server stuck, or every remaining trade
		// falls outside the window). Either way, further pages can't help.
		if len(batch) < c.PageSize || added == 0 {
			break
		}

		// Advance to the newest trade seen. Robust to whatever order the API
		// returns within a page: ascending → newest is batch[len-1];
		// descending → newest is batch[0]; unsorted → somewhere in between.
		// In every case the next page picks up from there and dedupe handles
		// the boundary overlap.
		cursor = maxCreated
	}
	return all, nil
}

// dedupeKey picks the best available unique identifier for a trade. Prefers
// the API's `id`; falls back to settlementTxHash + created + price + quantity
// so pagination stays correct even if the response ever omits `id`.
func dedupeKey(m Match) string {
	if m.ID != "" {
		return "id:" + m.ID
	}
	return "c:" + m.SettlementTxHash + "|" + m.Created + "|" + string(m.Price) + "|" + string(m.Quantity)
}

// fetchPage retrieves a single page, retrying transient failures with backoff.
func (c *Client) fetchPage(ctx context.Context, start, end time.Time) ([]Match, error) {
	url := fmt.Sprintf("%s?start_date=%s&end_date=%s&size=%d",
		c.BaseURL,
		start.UTC().Format(timeFormat),
		end.UTC().Format(timeFormat),
		c.PageSize,
	)

	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.RetryWait * time.Duration(attempt)):
			}
		}

		matches, retryable, err := c.doFetch(ctx, url)
		if err == nil {
			return matches, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("exhausted retries fetching %s: %w", url, lastErr)
}

// doFetch performs a single HTTP request. retryable indicates whether the caller
// should retry (network errors and 5xx/429 responses).
func (c *Client) doFetch(ctx context.Context, url string) (matches []Match, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("build request %s: %w", url, err)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // There's nothing we can do with an error from this.

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retry, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, url, body)
	}

	// Cap the body so a runaway upstream can't OOM the pod. json.Decoder
	// streams as it reads, so this bounds allocation too.
	body := http.MaxBytesReader(nil, resp.Body, c.MaxResponseBytes)
	var r response
	if err := json.NewDecoder(body).Decode(&r); err != nil {
		return nil, false, fmt.Errorf("decode response from %s: %w", url, err)
	}
	return r.Matches, false, nil
}

// VWAP computes the volume-weighted average price (USD per HASH) over the given
// trades: sum(price_i * quantity_i) / sum(quantity_i). Prices and quantities are
// parsed as exact decimals. Trades with non-positive quantity are skipped.
func VWAP(matches []Match) (*big.Rat, error) {
	numerator := new(big.Rat)   // sum(price * quantity)
	denominator := new(big.Rat) // sum(quantity)

	counted := 0
	for i, m := range matches {
		p, ok := new(big.Rat).SetString(string(m.Price))
		if !ok {
			return nil, fmt.Errorf("trade %d: invalid price %q", i, m.Price)
		}
		q, ok := new(big.Rat).SetString(string(m.Quantity))
		if !ok {
			return nil, fmt.Errorf("trade %d: invalid quantity %q", i, m.Quantity)
		}
		if q.Sign() <= 0 {
			continue
		}
		numerator.Add(numerator, new(big.Rat).Mul(p, q))
		denominator.Add(denominator, q)
		counted++
	}

	if counted == 0 || denominator.Sign() <= 0 {
		return nil, fmt.Errorf("no trades with positive quantity to average")
	}
	return new(big.Rat).Quo(numerator, denominator), nil
}

func (n *numStr) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*n = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*n = numStr(s)
		return nil
	}
	*n = numStr(b) // bare number form, e.g. 0.025 or 2.5e-2
	return nil
}
