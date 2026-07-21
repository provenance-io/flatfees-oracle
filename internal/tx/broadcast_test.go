package tx

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeTxSvc implements txtypes.ServiceClient for tests. Only BroadcastTx and
// GetTx are used in production code; the other methods panic so an accidental
// call surfaces as an obvious test failure rather than a silent no-op.
type fakeTxSvc struct {
	broadcastFn    func(context.Context, *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error)
	getTxFn        func(context.Context, *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error)
	broadcastCalls int
	getTxCalls     int
}

func (f *fakeTxSvc) BroadcastTx(ctx context.Context, in *txtypes.BroadcastTxRequest, _ ...grpc.CallOption) (*txtypes.BroadcastTxResponse, error) {
	f.broadcastCalls++
	return f.broadcastFn(ctx, in)
}

func (f *fakeTxSvc) GetTx(ctx context.Context, in *txtypes.GetTxRequest, _ ...grpc.CallOption) (*txtypes.GetTxResponse, error) {
	f.getTxCalls++
	return f.getTxFn(ctx, in)
}

// Unused methods on the interface — panic so accidental use is visible.
func (*fakeTxSvc) Simulate(context.Context, *txtypes.SimulateRequest, ...grpc.CallOption) (*txtypes.SimulateResponse, error) {
	panic("Simulate is not expected to be called")
}
func (*fakeTxSvc) GetTxsEvent(context.Context, *txtypes.GetTxsEventRequest, ...grpc.CallOption) (*txtypes.GetTxsEventResponse, error) {
	panic("GetTxsEvent is not expected to be called")
}
func (*fakeTxSvc) GetBlockWithTxs(context.Context, *txtypes.GetBlockWithTxsRequest, ...grpc.CallOption) (*txtypes.GetBlockWithTxsResponse, error) {
	panic("GetBlockWithTxs is not expected to be called")
}
func (*fakeTxSvc) TxDecode(context.Context, *txtypes.TxDecodeRequest, ...grpc.CallOption) (*txtypes.TxDecodeResponse, error) {
	panic("TxDecode is not expected to be called")
}
func (*fakeTxSvc) TxEncode(context.Context, *txtypes.TxEncodeRequest, ...grpc.CallOption) (*txtypes.TxEncodeResponse, error) {
	panic("TxEncode is not expected to be called")
}
func (*fakeTxSvc) TxEncodeAmino(context.Context, *txtypes.TxEncodeAminoRequest, ...grpc.CallOption) (*txtypes.TxEncodeAminoResponse, error) {
	panic("TxEncodeAmino is not expected to be called")
}
func (*fakeTxSvc) TxDecodeAmino(context.Context, *txtypes.TxDecodeAminoRequest, ...grpc.CallOption) (*txtypes.TxDecodeAminoResponse, error) {
	panic("TxDecodeAmino is not expected to be called")
}

// successBroadcast returns a broadcastFn that always reports CheckTx success with the given hash.
func successBroadcast(hash string) func(context.Context, *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
	return func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
		return &txtypes.BroadcastTxResponse{
			TxResponse: &sdk.TxResponse{Code: 0, TxHash: hash},
		}, nil
	}
}

// successGetTx returns a getTxFn that always reports DeliverTx success at height 100.
func successGetTx(hash string) func(context.Context, *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
	return func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
		return &txtypes.GetTxResponse{
			TxResponse: &sdk.TxResponse{Code: 0, TxHash: hash, Height: 100},
		}, nil
	}
}

func TestBroadcastSuccess(t *testing.T) {
	svc := &fakeTxSvc{broadcastFn: successBroadcast("ABC123")}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.Broadcast(context.Background(), []byte("txbytes"))
	require.NoError(t, err)
	assert.Equal(t, "ABC123", hash)
	assert.Equal(t, 1, svc.broadcastCalls)
}

func TestBroadcastForwardsTxBytesAndUsesSyncMode(t *testing.T) {
	var got *txtypes.BroadcastTxRequest
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, in *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			got = in
			return &txtypes.BroadcastTxResponse{TxResponse: &sdk.TxResponse{TxHash: "H"}}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	payload := []byte{0x01, 0x02, 0x03}
	_, err := b.Broadcast(context.Background(), payload)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, payload, got.TxBytes)
	assert.Equal(t, txtypes.BroadcastMode_BROADCAST_MODE_SYNC, got.Mode,
		"Broadcast must use SYNC mode so CheckTx result is available")
}

func TestBroadcastCheckTxFailureSurfacesHashAndLog(t *testing.T) {
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return &txtypes.BroadcastTxResponse{
				TxResponse: &sdk.TxResponse{Code: 7, TxHash: "FAILED123", RawLog: "insufficient fee"},
			}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.Broadcast(context.Background(), []byte("tx"))
	require.Error(t, err)
	assert.Equal(t, "FAILED123", hash, "hash must still be returned on CheckTx failure")
	assert.ErrorContains(t, err, "checkTx")
	assert.ErrorContains(t, err, "code 7")
	assert.ErrorContains(t, err, "insufficient fee")
}

func TestBroadcastRPCErrorSurfacesWrappedError(t *testing.T) {
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return nil, errors.New("rpc down")
		},
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.Broadcast(context.Background(), []byte("tx"))
	require.Error(t, err)
	assert.Empty(t, hash, "no hash should be returned when the RPC itself failed")
	assert.ErrorContains(t, err, "broadcast tx")
	assert.ErrorContains(t, err, "rpc down")
}

func TestBroadcastNilTxResponseIsError(t *testing.T) {
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return &txtypes.BroadcastTxResponse{TxResponse: nil}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	_, err := b.Broadcast(context.Background(), []byte("tx"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "nil tx response")
}

func TestConfirmPollsUntilFound(t *testing.T) {
	var calls int
	svc := &fakeTxSvc{
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			calls++
			if calls < 3 {
				return nil, errors.New("tx not found")
			}
			return &txtypes.GetTxResponse{
				TxResponse: &sdk.TxResponse{Code: 0, TxHash: "ABC", Height: 100},
			}, nil
		},
	}
	b := NewBroadcasterWithClient(svc) // 10 ms poll interval, 1 s timeout

	resp, err := b.Confirm(context.Background(), "ABC")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, int64(100), resp.Height)
	assert.GreaterOrEqual(t, calls, 3, "confirm must poll until the tx lands")
}

func TestConfirmDeliverTxFailureReturnsRespAndError(t *testing.T) {
	svc := &fakeTxSvc{
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			return &txtypes.GetTxResponse{
				TxResponse: &sdk.TxResponse{Code: 5, TxHash: "ABC", RawLog: "insufficient funds"},
			}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	resp, err := b.Confirm(context.Background(), "ABC")
	require.Error(t, err)
	require.NotNil(t, resp, "TxResponse must be returned even on failure so callers can log events/rawlog")
	assert.Equal(t, uint32(5), resp.Code)
	assert.ErrorContains(t, err, "deliverTx")
	assert.ErrorContains(t, err, "code 5")
	assert.ErrorContains(t, err, "insufficient funds")
}

func TestConfirmTimeoutSurfacesLastError(t *testing.T) {
	svc := &fakeTxSvc{
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			return nil, errors.New("tx not found")
		},
	}
	b := NewBroadcasterWithClient(svc)
	b.PollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := b.Confirm(ctx, "ABC")
	require.Error(t, err)
	assert.ErrorContains(t, err, "not confirmed")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "tx not found",
		"last poll error must appear so operators know what GetTx was reporting")
}

func TestConfirmNilTxResponseTimesOutReadably(t *testing.T) {
	// GetTx returns (non-nil, nil) with an empty TxResponse for the whole
	// window. Without the errNilTxResponse sentinel the wrapped error would
	// render "%!w(<nil>)"; assert the readable message instead.
	svc := &fakeTxSvc{
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			return &txtypes.GetTxResponse{TxResponse: nil}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)
	b.PollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := b.Confirm(ctx, "ABC")
	require.Error(t, err)
	assert.ErrorIs(t, err, errNilTxResponse,
		"nil TxResponse must be recorded as the sentinel, not silently as nil")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "not confirmed")
	assert.NotContains(t, err.Error(), "%!w",
		"formatting must not fall through to %%!w(<nil>)")
}

func TestConfirmHandlesNilResponseWithoutPanic(t *testing.T) {
	// Some proxies/mocks can return (nil, nil). The old code would nil-deref on
	// resp.TxResponse; the new guard treats it as a transient failure.
	svc := &fakeTxSvc{
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			return nil, nil //nolint:nilnil // intentional: exercising the nil,nil guard
		},
	}
	b := NewBroadcasterWithClient(svc)
	b.PollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	require.NotPanics(t, func() {
		_, err := b.Confirm(ctx, "ABC")
		require.Error(t, err)
		assert.ErrorIs(t, err, errNilTxResponse)
	})
}

func TestConfirmRespectsContextCancellation(t *testing.T) {
	svc := &fakeTxSvc{
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			return nil, errors.New("tx not found")
		},
	}
	b := NewBroadcasterWithClient(svc)
	b.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := b.Confirm(ctx, "ABC")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"context cancellation must terminate the poll loop")
}

func TestBroadcastRetriesTransientGRPCErrors(t *testing.T) {
	// Broadcast() config allows 2 attempts total. One transient failure then
	// success should complete cleanly and NOT double-broadcast on the chain
	// (BroadcastTx must be called twice — the retry — and no more).
	var attempts int
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			attempts++
			if attempts == 1 {
				return nil, status.Error(codes.Unavailable, "node restarting")
			}
			return &txtypes.BroadcastTxResponse{TxResponse: &sdk.TxResponse{Code: 0, TxHash: "H"}}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.Broadcast(context.Background(), []byte("tx"))
	require.NoError(t, err)
	assert.Equal(t, "H", hash)
	assert.Equal(t, 2, attempts, "should retry exactly once on Unavailable")
}

func TestBroadcastDoesNotRetryCheckTxFailure(t *testing.T) {
	// CheckTx failure is a chain-level rejection (Code != 0), not a gRPC error.
	// It must NOT be retried — the tx is already known-bad.
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return &txtypes.BroadcastTxResponse{
				TxResponse: &sdk.TxResponse{Code: 11, TxHash: "H", RawLog: "out of gas"},
			}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.Broadcast(context.Background(), []byte("tx"))
	require.Error(t, err)
	assert.Equal(t, "H", hash)
	assert.Equal(t, 1, svc.broadcastCalls, "CheckTx failure must not trigger a retry")
}

func TestBroadcastAndConfirmHappyPath(t *testing.T) {
	svc := &fakeTxSvc{
		broadcastFn: successBroadcast("H"),
		getTxFn:     successGetTx("H"),
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.BroadcastAndConfirm(context.Background(), []byte("tx"))
	require.NoError(t, err)
	assert.Equal(t, "H", hash)
	assert.Equal(t, 1, svc.broadcastCalls)
	assert.Equal(t, 1, svc.getTxCalls)
}

func TestBroadcastAndConfirmGRPCErrorReturnsOriginalWhenConfirmAlsoFails(t *testing.T) {
	// gRPC failure means the tx MIGHT be in the mempool (lost-ack) — the new
	// belt-and-suspenders path polls Confirm on the computed hash. If Confirm
	// also can't find the tx, we return the ORIGINAL broadcast error so the
	// operator sees why the wire attempt failed, not just "not confirmed".
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return nil, errors.New("connection refused") // non-retryable, non-gRPC
		},
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			return nil, errors.New("tx not found")
		},
	}
	b := NewBroadcasterWithClient(svc)
	b.PollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	hash, err := b.BroadcastAndConfirm(ctx, []byte("tx"))
	require.Error(t, err)
	assert.Empty(t, hash, "no hash to surface when the wire attempt never got a response and Confirm couldn't find the tx")
	assert.ErrorIs(t, err, ErrBroadcastRPC, "must return the original broadcast error, not the confirm timeout")
	assert.ErrorContains(t, err, "connection refused")
	assert.GreaterOrEqual(t, svc.getTxCalls, 1, "belt-and-suspenders Confirm must have been attempted")
}

func TestBroadcastAndConfirmReturnsHashOnConfirmFailure(t *testing.T) {
	svc := &fakeTxSvc{
		broadcastFn: successBroadcast("H"),
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			return &txtypes.GetTxResponse{
				TxResponse: &sdk.TxResponse{Code: 5, TxHash: "H", RawLog: "oops"},
			}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.BroadcastAndConfirm(context.Background(), []byte("tx"))
	require.Error(t, err)
	assert.Equal(t, "H", hash, "hash must be surfaced so operators can look the failed tx up on chain")
}

func TestComputeTxHashMatchesCosmosFormat(t *testing.T) {
	// Cosmos SDK tx hashes are SHA-256 over the encoded bytes, upper-hex.
	// SHA-256("hello") = 2CF24DBA5FB0A30E26E83B2AC5B9E29E1B161E5C1FA7425E73043362938B9824
	got := ComputeTxHash([]byte("hello"))
	assert.Equal(t, "2CF24DBA5FB0A30E26E83B2AC5B9E29E1B161E5C1FA7425E73043362938B9824", got)
	assert.Len(t, got, 64, "SHA-256 hex is 64 chars")
}

func TestBroadcastAndConfirmRecoversFromDuplicateInCache(t *testing.T) {
	// Simulates: first broadcast reached the node and landed, retry rejected
	// as ErrTxInMempoolCache (Codespace "sdk", code 19, hash present). The
	// tx is on chain — BroadcastAndConfirm must call Confirm despite the
	// error and report success.
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return &txtypes.BroadcastTxResponse{
				TxResponse: &sdk.TxResponse{
					Codespace: "sdk",
					Code:      19,
					TxHash:    "NODEHASH",
					RawLog:    "tx already in mempool",
				},
			}, nil
		},
		getTxFn: successGetTx("NODEHASH"), // the original tx lands
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.BroadcastAndConfirm(context.Background(), []byte("tx"))
	require.NoError(t, err, "duplicate-in-cache must be treated as success once the tx confirms")
	assert.Equal(t, "NODEHASH", hash, "must return the hash the node reported")
	assert.GreaterOrEqual(t, svc.getTxCalls, 1, "Confirm must run despite the broadcast error")
}

func TestBroadcastDuplicateInMempoolWrapsSentinel(t *testing.T) {
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return &txtypes.BroadcastTxResponse{
				TxResponse: &sdk.TxResponse{
					Codespace: "sdk", Code: 19, TxHash: "H", RawLog: "tx already in mempool",
				},
			}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.Broadcast(context.Background(), []byte("tx"))
	require.Error(t, err)
	assert.Equal(t, "H", hash)
	assert.ErrorIs(t, err, ErrTxAlreadyInMempool,
		"code 19 in the sdk codespace must be wrapped with ErrTxAlreadyInMempool")
	assert.NotErrorIs(t, err, ErrCheckTxRejected,
		"the duplicate case is NOT a real CheckTx rejection")
}

func TestBroadcastNonMempoolCode19IsRealRejection(t *testing.T) {
	// Code 19 in a NON-"sdk" codespace is some other module's error, not our
	// duplicate sentinel. Must fall through to ErrCheckTxRejected.
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return &txtypes.BroadcastTxResponse{
				TxResponse: &sdk.TxResponse{
					Codespace: "flatfees", Code: 19, TxHash: "H", RawLog: "some module error",
				},
			}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	_, err := b.Broadcast(context.Background(), []byte("tx"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCheckTxRejected,
		"code 19 outside the sdk codespace is unrelated to mempool caching")
	assert.NotErrorIs(t, err, ErrTxAlreadyInMempool)
}

func TestBroadcastAndConfirmRecoversFromCompleteBroadcastFailure(t *testing.T) {
	// Simulates: EVERY broadcast attempt lost its response (double failure).
	// No hash comes back from the node. BroadcastAndConfirm must fall back to
	// the locally-computed hash and confirm on that.
	txBytes := []byte("some signed bytes")
	expectedHash := ComputeTxHash(txBytes)

	var confirmedHash string
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return nil, status.Error(codes.Unavailable, "always down")
		},
		getTxFn: func(_ context.Context, in *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			confirmedHash = in.Hash
			return &txtypes.GetTxResponse{
				TxResponse: &sdk.TxResponse{Code: 0, TxHash: in.Hash, Height: 100},
			}, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.BroadcastAndConfirm(context.Background(), txBytes)
	require.NoError(t, err, "double-failure recovery must succeed when the tx lands")
	assert.Equal(t, expectedHash, hash, "recovered hash must be the locally-computed one")
	assert.Equal(t, expectedHash, confirmedHash, "Confirm must be called with the computed hash")
}

func TestBroadcastAndConfirmSkipsConfirmOnRealCheckTxRejection(t *testing.T) {
	// Real CheckTx rejection ("insufficient fees") — the tx did NOT enter
	// the mempool. BroadcastAndConfirm must return immediately without
	// polling GetTx, since Confirm would just spin against the ctx deadline.
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return &txtypes.BroadcastTxResponse{
				TxResponse: &sdk.TxResponse{
					Codespace: "sdk", Code: 13, TxHash: "REJECTED", RawLog: "insufficient fees",
				},
			}, nil
		},
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			t.Fatal("GetTx must NOT be called on a real CheckTx rejection")
			return nil, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	hash, err := b.BroadcastAndConfirm(context.Background(), []byte("tx"))
	require.Error(t, err)
	assert.Equal(t, "REJECTED", hash, "hash from CheckTx failure still surfaces")
	assert.ErrorIs(t, err, ErrCheckTxRejected,
		"real rejection must be wrapped with ErrCheckTxRejected")
	assert.ErrorContains(t, err, "insufficient fees")
	assert.Equal(t, 0, svc.getTxCalls, "no ctx budget wasted polling for a rejected tx")
}
