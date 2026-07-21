package retry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func fastConfig(attempts int) Config {
	return Config{Attempts: attempts, Backoff: time.Millisecond}
}

func TestDoSucceedsOnFirstAttempt(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastConfig(3), func() error {
		calls++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "no retry needed on immediate success")
}

func TestDoRetriesTransientAndSucceeds(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastConfig(3), func() error {
		calls++
		if calls < 3 {
			return status.Error(codes.Unavailable, "server unavailable")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls, "should retry until success")
}

func TestDoDoesNotRetryPermanentErrors(t *testing.T) {
	permanent := []codes.Code{
		codes.InvalidArgument,
		codes.NotFound,
		codes.Unauthenticated,
		codes.PermissionDenied,
		codes.FailedPrecondition,
	}
	for _, c := range permanent {
		t.Run(c.String(), func(t *testing.T) {
			calls := 0
			err := Do(context.Background(), fastConfig(5), func() error {
				calls++
				return status.Error(c, "nope")
			})
			require.Error(t, err)
			assert.Equal(t, 1, calls, "permanent %s must not retry", c)
		})
	}
}

func TestDoDoesNotRetryNonGRPCErrors(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastConfig(5), func() error {
		calls++
		return errors.New("plain old error")
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "non-gRPC errors must not retry")
}

func TestDoExhaustsRetriesAndWrapsLastError(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastConfig(3), func() error {
		calls++
		return status.Error(codes.Unavailable, "still down")
	})
	require.Error(t, err)
	assert.Equal(t, 3, calls, "must attempt exactly cfg.Attempts times")
	assert.ErrorContains(t, err, "exhausted 3 attempts")
	assert.ErrorContains(t, err, "still down")
	// The wrapped error must still be reachable via errors.As so callers can
	// inspect the underlying gRPC status.
	st, ok := status.FromError(err)
	require.True(t, ok, "wrapped error must remain a gRPC status")
	assert.Equal(t, codes.Unavailable, st.Code())
}

func TestDoRespectsContextCancellationBetweenAttempts(t *testing.T) {
	// Give ctx 5 ms; each retry backs off 20 ms → the cancellation should fire
	// during the first backoff.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	calls := 0
	err := Do(ctx, Config{Attempts: 5, Backoff: 20 * time.Millisecond}, func() error {
		calls++
		return status.Error(codes.Unavailable, "down")
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, calls, "must not attempt again after ctx expires")
}

func TestDoTreatsAttemptsBelowOneAsOne(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Config{Attempts: 0, Backoff: time.Millisecond}, func() error {
		calls++
		return status.Error(codes.Unavailable, "down")
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "Attempts < 1 means exactly one attempt, still no retries")
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("nope"), false},
		{"wrapped plain error", fmt.Errorf("outer: %w", errors.New("inner")), false},
		{"unavailable", status.Error(codes.Unavailable, ""), true},
		{"deadline exceeded", status.Error(codes.DeadlineExceeded, ""), true},
		{"aborted", status.Error(codes.Aborted, ""), true},
		{"resource exhausted", status.Error(codes.ResourceExhausted, ""), true},
		{"invalid argument", status.Error(codes.InvalidArgument, ""), false},
		{"not found", status.Error(codes.NotFound, ""), false},
		{"unauthenticated", status.Error(codes.Unauthenticated, ""), false},
		{"failed precondition", status.Error(codes.FailedPrecondition, ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsRetryable(tc.err))
		})
	}
}

func TestBroadcastConfigIsMoreConservative(t *testing.T) {
	// Sanity: reads retry more aggressively than the tx path.
	assert.Greater(t, Default().Attempts, Broadcast().Attempts,
		"chain-read retries should exceed broadcast retries")
}
