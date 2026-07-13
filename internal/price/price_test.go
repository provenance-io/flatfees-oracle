package price

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVWAP(t *testing.T) {
	// Two trades: 10 @ $0.05 and 30 @ $0.09.
	// VWAP = (0.05*10 + 0.09*30) / (10+30) = 3.2/40 = 0.08.
	matches := []Match{
		{Price: "0.05", Quantity: "10"},
		{Price: "0.09", Quantity: "30"},
	}
	got, err := VWAP(matches)
	require.NoError(t, err)
	assert.Zerof(t, got.Cmp(big.NewRat(8, 100)), "VWAP = %s, want 0.08", got.FloatString(6))
}

func TestVWAPSkipsZeroQuantity(t *testing.T) {
	matches := []Match{
		{Price: "0.05", Quantity: "0"},
		{Price: "0.10", Quantity: "5"},
	}
	got, err := VWAP(matches)
	require.NoError(t, err)
	assert.Zerof(t, got.Cmp(big.NewRat(10, 100)), "VWAP = %s, want 0.10", got.FloatString(6))
}

func TestVWAPErrors(t *testing.T) {
	_, err := VWAP(nil)
	assert.EqualError(t, err, "no trades with positive quantity to average")

	_, err = VWAP([]Match{{Price: "bad", Quantity: "1"}})
	assert.EqualError(t, err, `trade 0: invalid price "bad"`)

	_, err = VWAP([]Match{{Price: "1", Quantity: "nope"}})
	assert.EqualError(t, err, `trade 0: invalid quantity "nope"`)
}

// TestGetPricePaginates serves two pages and verifies pagination + VWAP.
func TestGetPricePaginates(t *testing.T) {
	pageSize := 2
	page1 := []Match{
		{ID: "1", Price: "0.05", Quantity: "10", Created: "2026-06-15T00:00:00.000000000Z"},
		{ID: "2", Price: "0.05", Quantity: "10", Created: "2026-06-15T00:00:01.000000000Z"},
	}
	page2 := []Match{
		{ID: "3", Price: "0.07", Quantity: "20", Created: "2026-06-15T00:00:02.000000000Z"},
	}

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		batch := page2
		if calls == 1 {
			batch = page1
		}
		_ = json.NewEncoder(w).Encode(response{Matches: batch})
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	c.PageSize = pageSize
	c.MaxRetries = 0
	c.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }

	res, err := c.GetPrice(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "expected two page fetches")
	assert.Equal(t, 3, res.Trades, "trade count")
	// VWAP = (0.05*10 + 0.05*10 + 0.07*20)/40 = 2.4/40 = 0.06
	assert.Zerof(t, res.PriceUSDPerHASH.Cmp(big.NewRat(6, 100)),
		"VWAP = %s, want 0.06", res.PriceUSDPerHASH.FloatString(6))
	// Total volume = 10 + 10 + 20 = 40 HASH
	assert.Zerof(t, res.VolumeHASH.Cmp(big.NewRat(40, 1)),
		"VolumeHASH = %s, want 40", res.VolumeHASH.FloatString(6))
}

// TestGetPricePaginatesReverseOrderedBatch: the API's paginate-by-start_date
// scheme is ascending (returns trades where created >= start_date, oldest N
// first), but within a page it hands them back reversed (newest-first). Under
// the old "cursor = batch[len-1]" logic that anchors on the OLDEST trade of
// the batch, so the next fetch's start_date barely moves and either re-fetches
// the same trades (dedupe → added==0 → break, LOSING later trades) or misses
// intermediate trades. The fix anchors on maxCreated, which advances properly.
func TestGetPricePaginatesReverseOrderedBatch(t *testing.T) {
	pageSize := 2
	// Four trades spread across the window, in ascending Created.
	all := []Match{
		{ID: "t1", Price: "0.05", Quantity: "10", Created: "2026-06-15T00:00:00.000000000Z"},
		{ID: "t2", Price: "0.06", Quantity: "20", Created: "2026-06-15T00:00:10.000000000Z"},
		{ID: "t3", Price: "0.07", Quantity: "20", Created: "2026-06-15T00:00:20.000000000Z"},
		{ID: "t4", Price: "0.08", Quantity: "30", Created: "2026-06-15T00:00:30.000000000Z"},
	}

	// Server: returns up to pageSize trades where Created >= start_date, but
	// hands them back in reversed (newest-first) order within the batch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startStr := r.URL.Query().Get("start_date")
		startT, err := time.Parse(time.RFC3339Nano, startStr)
		require.NoErrorf(t, err, "start_date=%q", startStr)

		var eligible []Match
		for _, m := range all {
			mt, _ := time.Parse(time.RFC3339Nano, m.Created)
			if !mt.Before(startT) { // Created >= startT
				eligible = append(eligible, m)
			}
		}
		if len(eligible) > pageSize {
			eligible = eligible[:pageSize]
		}
		// Reverse in place: newest-first within batch.
		for i, j := 0, len(eligible)-1; i < j; i, j = i+1, j-1 {
			eligible[i], eligible[j] = eligible[j], eligible[i]
		}
		_ = json.NewEncoder(w).Encode(response{Matches: eligible})
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	c.PageSize = pageSize
	c.MaxRetries = 0
	c.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }

	res, err := c.GetPrice(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 4, res.Trades,
		"all four trades must be counted despite each batch being newest-first")
	// Total volume = 10 + 20 + 20 + 30 = 80 HASH
	assert.Zerof(t, res.VolumeHASH.Cmp(big.NewRat(80, 1)),
		"VolumeHASH = %s, want 80", res.VolumeHASH.FloatString(6))
}

// TestGetPriceDedupesPageOverlap: the server returns the last trade of page 1
// as the first trade of page 2 (i.e. inclusive start_date). Cursor advancement
// refetches that boundary; the dedupe key must drop the repeat so the
// aggregated result matches the underlying set.
func TestGetPriceDedupesPageOverlap(t *testing.T) {
	pageSize := 2
	boundary := Match{ID: "2", Price: "0.05", Quantity: "10", Created: "2026-06-15T00:00:01.000000000Z"}
	page1 := []Match{
		{ID: "1", Price: "0.05", Quantity: "10", Created: "2026-06-15T00:00:00.000000000Z"},
		boundary,
	}
	page2 := []Match{
		boundary, // <- overlap
		{ID: "3", Price: "0.07", Quantity: "20", Created: "2026-06-15T00:00:02.000000000Z"},
	}
	page3 := []Match{} // short → stop

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var batch []Match
		switch calls {
		case 1:
			batch = page1
		case 2:
			batch = page2
		default:
			batch = page3
		}
		_ = json.NewEncoder(w).Encode(response{Matches: batch})
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	c.PageSize = pageSize
	c.MaxRetries = 0
	c.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }

	res, err := c.GetPrice(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, res.Trades, "boundary trade should be counted once, not twice")
	// VWAP = (0.05*10 + 0.05*10 + 0.07*20)/40 = 2.4/40 = 0.06
	assert.Zerof(t, res.PriceUSDPerHASH.Cmp(big.NewRat(6, 100)),
		"VWAP = %s, want 0.06", res.PriceUSDPerHASH.FloatString(6))
}

// TestGetPriceIgnoresOutOfWindowTrades: the server misbehaves and returns a
// trade whose timestamp falls after end. It must be discarded.
func TestGetPriceIgnoresOutOfWindowTrades(t *testing.T) {
	// window() for Now = 2026-06-16 12:00 UTC ends at 2026-06-16 00:00 Eastern
	// (== 2026-06-16 04:00 UTC). An "in-window" trade must be strictly before that.
	in := Match{ID: "in", Price: "0.05", Quantity: "10", Created: "2026-06-15T12:00:00.000000000Z"}
	out := Match{ID: "out", Price: "9.99", Quantity: "10", Created: "2026-06-16T23:59:00.000000000Z"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(response{Matches: []Match{in, out}})
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	c.MaxRetries = 0
	c.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }

	res, err := c.GetPrice(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Trades, "out-of-window trade must be dropped")
	assert.Zerof(t, res.PriceUSDPerHASH.Cmp(big.NewRat(5, 100)),
		"VWAP = %s, want 0.05", res.PriceUSDPerHASH.FloatString(6))
}

func TestGetPriceRetriesOn5xx(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(response{Matches: []Match{
			{ID: "1", Price: "0.05", Quantity: "1", Created: "2026-06-15T00:00:00.000000000Z"},
		}})
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	c.MaxRetries = 2
	c.RetryWait = time.Millisecond
	c.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }

	res, err := c.GetPrice(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "expected one retry")
	assert.Zerof(t, res.PriceUSDPerHASH.Cmp(big.NewRat(5, 100)),
		"VWAP = %s, want 0.05", res.PriceUSDPerHASH.FloatString(6))
}

func TestGetPriceEmptyWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(response{Matches: []Match{}})
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	c.MaxRetries = 0
	c.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }

	_, err := c.GetPrice(context.Background())
	assert.ErrorIs(t, err, ErrNoTrades, "should return ErrNoTrades sentinel so callers can skip cleanly")
}

// TestGetPriceHandlesVariablePrecisionTimestamps: the API returns Created
// timestamps with a variable number of fractional-second digits (production
// has seen 8 digits, e.g. .66608799Z). Parsing must accept anything up to 9
// digits, matching time.RFC3339Nano.
func TestGetPriceHandlesVariablePrecisionTimestamps(t *testing.T) {
	// One trade per fractional-second width from 0 to 9 digits.
	timestamps := []string{
		"2026-06-15T00:00:00Z",              // 0 digits
		"2026-06-15T00:00:01.1Z",            // 1
		"2026-06-15T00:00:02.12Z",           // 2
		"2026-06-15T00:00:03.123Z",          // 3
		"2026-06-15T00:00:04.66608799Z",     // 8 — the exact width that hit production
		"2026-06-15T00:00:05.123456789Z",    // 9
	}
	all := make([]Match, len(timestamps))
	for i, ts := range timestamps {
		all[i] = Match{
			ID:       fmt.Sprintf("t%d", i),
			Price:    "0.05",
			Quantity: "1",
			Created:  ts,
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(response{Matches: all})
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	c.MaxRetries = 0
	c.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }

	res, err := c.GetPrice(context.Background())
	require.NoError(t, err, "variable-precision fractional seconds must parse")
	assert.Equal(t, len(timestamps), res.Trades)
}

// TestGetPriceRejectsOversizedResponse: the response body exceeds
// MaxResponseBytes. The decode must fail (not OOM) and the error should be
// treated as non-retryable, so GetPrice surfaces it once.
func TestGetPriceRejectsOversizedResponse(t *testing.T) {
	// Serve a body larger than MaxResponseBytes. Padding with a JSON-safe
	// filler in "denom" ensures the decoder actually pulls the bytes.
	oversized := `{"denom":"` + strings.Repeat("x", 2048) + `","matches":[]}`

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(oversized))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	c.MaxRetries = 3           // deliberately > 0 to prove no retry on oversize
	c.MaxResponseBytes = 1024  // < oversized body
	c.RetryWait = time.Millisecond
	c.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }

	_, err := c.GetPrice(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "decode response",
		"MaxBytesReader failure must surface through the decode error path")
	assert.Equal(t, 1, calls, "oversized response is not retryable — must not retry")
}

// TestWindow verifies a 7-day span ending at Eastern midnight.
func TestWindow(t *testing.T) {
	c := New()
	c.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }
	start, end, err := c.window()
	require.Nil(t, err, "window err")
	assert.Equal(t, 7*24*time.Hour, end.Sub(start), "window span")
	assert.Truef(t, end.After(start), "end %s should be after start %s", end, start)
}
