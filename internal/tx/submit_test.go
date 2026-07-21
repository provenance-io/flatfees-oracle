package tx

import (
	"context"
	"errors"
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	flatfeestypes "github.com/provenance-io/provenance/x/flatfees/types"
)

// fakeEstimator captures the arguments passed to EstimateTxFees so tests can
// assert what the Submitter forwarded, and returns the configured fees/gas or
// error.
type fakeEstimator struct {
	// Return values.
	gas  uint64
	fees sdk.Coins
	err  error

	// Recorded call state.
	calls       int
	lastTxBytes []byte
	lastGasAdj  float32
}

func (f *fakeEstimator) EstimateTxFees(_ context.Context, txBytes []byte, gasAdj float32) (*flatfeestypes.QueryCalculateTxFeesResponse, error) {
	f.calls++
	// Copy so a caller mutating its buffer can't retroactively change what we captured.
	f.lastTxBytes = append([]byte(nil), txBytes...)
	f.lastGasAdj = gasAdj
	if f.err != nil {
		return nil, f.err
	}
	return &flatfeestypes.QueryCalculateTxFeesResponse{
		EstimatedGas: f.gas,
		TotalFees:    f.fees,
	}, nil
}

// newTestEstimator returns a fakeEstimator with sensible non-zero defaults.
func newTestEstimator() *fakeEstimator {
	return &fakeEstimator{
		gas:  200_000,
		fees: sdk.NewCoins(sdk.NewCoin("nhash", math.NewIntFromUint64(1_000_000))),
	}
}

// accountCounter is a tiny recorder that lets a test assert how many times its
// AccountFetcher was invoked and with what address.
type accountCounter struct {
	calls    int
	lastAddr string
	accNum   uint64
	sequence uint64
	err      error
}

func (a *accountCounter) fetch(_ context.Context, addr string) (uint64, uint64, error) {
	a.calls++
	a.lastAddr = addr
	if a.err != nil {
		return 0, 0, a.err
	}
	return a.accNum, a.sequence, nil
}

// newTestSubmitter builds a Submitter with a real Signer (via signer_test.go's
// helper) and the given estimator, broadcaster service, and account fetcher.
func newTestSubmitter(t *testing.T, est Estimator, svc *fakeTxSvc, acct *accountCounter) (*Submitter, *Signer) {
	t.Helper()
	s, _ := newTestSigner(t)
	return &Submitter{
		Signer:        s,
		Estimator:     est,
		Broadcaster:   NewBroadcasterWithClient(svc),
		Account:       acct.fetch,
		GasAdjustment: 1.5,
	}, s
}

func TestSubmitOrderedHappyPath(t *testing.T) {
	est := newTestEstimator()
	svc := &fakeTxSvc{broadcastFn: successBroadcast("H"), getTxFn: successGetTx("H")}
	acct := &accountCounter{accNum: 7, sequence: 3}
	sub, s := newTestSubmitter(t, est, svc, acct)

	hash, err := sub.SubmitOrdered(context.Background(), testMsg(s.Address()))
	require.NoError(t, err)
	assert.Equal(t, "H", hash)

	assert.Equal(t, 1, acct.calls, "SubmitOrdered must look the account up exactly once")
	assert.Equal(t, s.Address(), acct.lastAddr, "account lookup must use the signer's address")

	assert.Equal(t, 1, est.calls, "estimator must be called exactly once")
	assert.Equal(t, float32(1.5), est.lastGasAdj, "gas adjustment must be forwarded to the estimator")
	assert.NotEmpty(t, est.lastTxBytes, "sim tx bytes must be forwarded to the estimator")

	assert.Equal(t, 1, svc.broadcastCalls, "signed tx must be broadcast exactly once")
	assert.Equal(t, 1, svc.getTxCalls, "confirm must poll GetTx at least once")
}

func TestSubmitOrderedAccountLookupErrorAbortsBeforeBroadcast(t *testing.T) {
	est := newTestEstimator()
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			t.Fatal("BroadcastTx must not be called when the account lookup fails")
			return nil, nil
		},
	}
	acct := &accountCounter{err: errors.New("account lookup failed")}
	sub, s := newTestSubmitter(t, est, svc, acct)

	hash, err := sub.SubmitOrdered(context.Background(), testMsg(s.Address()))
	require.Error(t, err)
	assert.Empty(t, hash)
	assert.ErrorContains(t, err, "account lookup failed")
	assert.Equal(t, 0, est.calls, "estimator must not be called when the account lookup fails")
}

func TestSubmitOrderedEstimatorErrorAbortsBeforeBroadcast(t *testing.T) {
	est := &fakeEstimator{err: errors.New("estimate boom")}
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			t.Fatal("BroadcastTx must not be called when the estimator fails")
			return nil, nil
		},
	}
	acct := &accountCounter{accNum: 7, sequence: 3}
	sub, s := newTestSubmitter(t, est, svc, acct)

	_, err := sub.SubmitOrdered(context.Background(), testMsg(s.Address()))
	require.Error(t, err)
	assert.ErrorContains(t, err, "estimate boom")
}

func TestSubmitOrderedBroadcastErrorSurfacesHash(t *testing.T) {
	est := newTestEstimator()
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return &txtypes.BroadcastTxResponse{
				TxResponse: &sdk.TxResponse{Code: 11, TxHash: "PARTIAL", RawLog: "out of gas"},
			}, nil
		},
	}
	acct := &accountCounter{accNum: 7, sequence: 3}
	sub, s := newTestSubmitter(t, est, svc, acct)

	hash, err := sub.SubmitOrdered(context.Background(), testMsg(s.Address()))
	require.Error(t, err)
	assert.Equal(t, "PARTIAL", hash, "hash from CheckTx failure must be surfaced")
	assert.ErrorContains(t, err, "code 11")
	assert.ErrorContains(t, err, "out of gas")
}

func TestSubmitUnorderedUsesConfiguredAccountNumberWithoutLookup(t *testing.T) {
	est := newTestEstimator()
	svc := &fakeTxSvc{broadcastFn: successBroadcast("H"), getTxFn: successGetTx("H")}
	acct := &accountCounter{accNum: 999, sequence: 999} // must never be read
	sub, s := newTestSubmitter(t, est, svc, acct)

	hash, err := sub.SubmitUnordered(context.Background(), testMsg(s.Address()), 42, 2*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "H", hash)
	assert.Equal(t, 0, acct.calls,
		"SubmitUnordered must NOT call the account fetcher when a non-zero accNum was supplied")
}

func TestSubmitUnorderedLooksUpAccountNumberWhenZero(t *testing.T) {
	est := newTestEstimator()
	svc := &fakeTxSvc{broadcastFn: successBroadcast("H"), getTxFn: successGetTx("H")}
	acct := &accountCounter{accNum: 7, sequence: 0}
	sub, s := newTestSubmitter(t, est, svc, acct)

	_, err := sub.SubmitUnordered(context.Background(), testMsg(s.Address()), 0, 2*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 1, acct.calls,
		"SubmitUnordered must call the account fetcher when accNum==0 (the 'look it up' sentinel)")
	assert.Equal(t, s.Address(), acct.lastAddr)
}

func TestSubmitUnorderedAccountLookupErrorAbortsBeforeSign(t *testing.T) {
	est := newTestEstimator()
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			t.Fatal("BroadcastTx must not be called when the account lookup fails")
			return nil, nil
		},
	}
	acct := &accountCounter{err: errors.New("no such account")}
	sub, s := newTestSubmitter(t, est, svc, acct)

	_, err := sub.SubmitUnordered(context.Background(), testMsg(s.Address()), 0, 2*time.Minute)
	require.Error(t, err)
	assert.ErrorContains(t, err, "no such account")
	assert.Equal(t, 0, est.calls, "estimator must not be called when the account lookup fails")
}

func TestSubmitUnorderedEstimatorErrorAbortsBeforeSign(t *testing.T) {
	est := &fakeEstimator{err: errors.New("estimate boom")}
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			t.Fatal("BroadcastTx must not be called when the estimator fails")
			return nil, nil
		},
	}
	acct := &accountCounter{accNum: 42, sequence: 0}
	sub, s := newTestSubmitter(t, est, svc, acct)

	_, err := sub.SubmitUnordered(context.Background(), testMsg(s.Address()), 42, 2*time.Minute)
	require.Error(t, err)
	assert.ErrorContains(t, err, "estimate boom")
}
