// Package price fetches HASH-USD trades from the internal Figure Markets exchange
// API and aggregates them into a single volume-weighted average price (VWAP) over
// a trailing 7-day window (ending midnight Eastern). This 7-day VWAP is the agreed
// aggregation method for the conversion factor.
package price

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"
)

const (
	// DefaultBaseURL is the internal Figure Markets HASH-USD trades endpoint.
	DefaultBaseURL = "https://www.figuremarkets.com/service-hft-exchange/api/v1/trades/HASH-USD"
	// defaultPageSize is the max trades per page the API returns.
	// The API limits this to 200 max.
	defaultPageSize = 200
	// windowDays is the trailing window (ending midnight Eastern) to aggregate.
	windowDays = 7
	// timeFormat is the nanosecond timestamp format used by the API.
	timeFormat = "2006-01-02T15:04:05.000000000Z"
)

// Match is a single trade entry from the API.
type Match struct {
	ID               string `json:"id"`
	Price            string `json:"price"`
	Quantity         string `json:"quantity"`
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
	BaseURL    string
	HTTP       *http.Client
	PageSize   int
	WindowDays int
	MaxRetries int           // additional attempts on transient failures
	RetryWait  time.Duration // base backoff between retries
	// Now allows tests to pin the window. Defaults to time.Now.
	Now func() time.Time
}

// New returns a Client with sensible defaults.
func New() *Client {
	return &Client{
		BaseURL:    DefaultBaseURL,
		HTTP:       &http.Client{Timeout: 15 * time.Second},
		PageSize:   defaultPageSize,
		WindowDays: windowDays,
		MaxRetries: 3,
		RetryWait:  500 * time.Millisecond,
		Now:        time.Now,
	}
}

// Result is the aggregated price plus context for logging/auditing.
type Result struct {
	// PriceUSDPerHASH is the volume-weighted average price (USD per HASH) as an
	// exact rational.
	PriceUSDPerHASH *big.Rat
	// Trades is the number of trades aggregated.
	Trades int
	// WindowStart and WindowEnd bound the trades considered (UTC).
	WindowStart time.Time
	WindowEnd   time.Time
}

// GetPrice fetches all trades in the trailing window and returns their VWAP.
func (c *Client) GetPrice(ctx context.Context) (Result, error) {
	start, end := c.window()
	matches, err := c.fetchAll(ctx, start, end)
	if err != nil {
		return Result{}, err
	}
	if len(matches) == 0 {
		return Result{}, fmt.Errorf("no HASH-USD trades in window %s..%s",
			start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	vwap, err := VWAP(matches)
	if err != nil {
		return Result{}, err
	}
	return Result{
		PriceUSDPerHASH: vwap,
		Trades:          len(matches),
		WindowStart:     start,
		WindowEnd:       end,
	}, nil
}

// window returns the [start, end) UTC bounds: the WindowDays ending at midnight
// Eastern of the current date.
func (c *Client) window() (time.Time, time.Time) {
	now := c.Now()
	eastern, err := time.LoadLocation("America/New_York")
	if err == nil {
		now = now.In(eastern)
	}
	endLocal := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endUTC := endLocal.UTC()
	startUTC := endUTC.AddDate(0, 0, -c.WindowDays)
	return startUTC, endUTC
}

// fetchAll paginates through all trades between start and end.
func (c *Client) fetchAll(ctx context.Context, start, end time.Time) ([]Match, error) {
	var all []Match
	cursor := start
	for {
		batch, err := c.fetchPage(ctx, cursor, end)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < c.PageSize {
			break
		}
		last := batch[len(batch)-1]
		t, err := time.Parse(timeFormat, last.Created)
		if err != nil {
			return nil, fmt.Errorf("parse created time %q: %w", last.Created, err)
		}
		cursor = t.Add(time.Millisecond)
	}
	return all, nil
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retry, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, url, body)
	}

	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
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
		p, ok := new(big.Rat).SetString(m.Price)
		if !ok {
			return nil, fmt.Errorf("trade %d: invalid price %q", i, m.Price)
		}
		q, ok := new(big.Rat).SetString(m.Quantity)
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
