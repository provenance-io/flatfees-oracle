// Package retry provides a small helper for retrying gRPC calls on transient
// server errors. It is deliberately narrow: it inspects the gRPC status code,
// retries only on codes that are safe to replay, respects ctx cancellation
// between attempts, and gives up cleanly with the last error wrapped for
// context.
package retry

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Config controls Do's behaviour.
type Config struct {
	// Attempts is the maximum number of attempts, including the first. Values
	// < 1 are treated as 1 (i.e. no retries).
	Attempts int
	// Backoff is the base delay between attempts; the nth retry waits
	// n*Backoff (linear). Keeps behaviour predictable and cheap to reason about
	// inside the oracle's 2-minute budget.
	Backoff time.Duration
}

// Default returns a Config suitable for idempotent chain reads: three
// attempts, 500 ms base backoff (~1.5 s of extra wall time on a fully-retried
// call).
func Default() Config {
	return Config{Attempts: 3, Backoff: 500 * time.Millisecond}
}

// Broadcast returns a more conservative Config for the tx-touching path: two
// attempts, 500 ms backoff. Replay protection on the message makes a single
// retry safe; more attempts add cost without meaningfully improving success
// odds.
func Broadcast() Config {
	return Config{Attempts: 2, Backoff: 500 * time.Millisecond}
}

// Do calls fn, retrying up to cfg.Attempts-1 additional times on transient
// gRPC errors. It respects ctx cancellation between attempts. Non-retryable
// errors are returned immediately. When every attempt fails, the last error is
// wrapped with the attempt count for observability.
func Do(ctx context.Context, cfg Config, fn func() error) error {
	if cfg.Attempts < 1 {
		cfg.Attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= cfg.Attempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cfg.Backoff * time.Duration(attempt-1)):
			}
		}
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !IsRetryable(err) {
			return err
		}
	}
	return fmt.Errorf("exhausted %d attempts: %w", cfg.Attempts, lastErr)
}

// IsRetryable reports whether err is a transient gRPC status worth retrying.
// Non-gRPC errors and permanent gRPC errors (InvalidArgument, NotFound,
// Unauthenticated, PermissionDenied, FailedPrecondition, etc.) are treated as
// non-retryable — replaying them just wastes budget.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unavailable,
		codes.DeadlineExceeded,
		codes.Aborted,
		codes.ResourceExhausted:
		return true
	default:
		return false
	}
}
