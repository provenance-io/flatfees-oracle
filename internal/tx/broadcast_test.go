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
	b.PollTimeout = 50 * time.Millisecond
	b.PollInterval = 10 * time.Millisecond

	_, err := b.Confirm(context.Background(), "ABC")
	require.Error(t, err)
	assert.ErrorContains(t, err, "not confirmed within")
	assert.ErrorContains(t, err, "tx not found")
}

func TestConfirmRespectsContextCancellation(t *testing.T) {
	svc := &fakeTxSvc{
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			return nil, errors.New("tx not found")
		},
	}
	b := NewBroadcasterWithClient(svc)
	b.PollTimeout = 5 * time.Second // deliberately longer than ctx deadline
	b.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := b.Confirm(ctx, "ABC")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"context cancellation must short-circuit the poll loop")
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

func TestBroadcastAndConfirmBroadcastErrorStopsShort(t *testing.T) {
	svc := &fakeTxSvc{
		broadcastFn: func(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
			return nil, errors.New("rpc down")
		},
		getTxFn: func(_ context.Context, _ *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
			t.Fatal("GetTx must not be called when Broadcast fails")
			return nil, nil
		},
	}
	b := NewBroadcasterWithClient(svc)

	_, err := b.BroadcastAndConfirm(context.Background(), []byte("tx"))
	require.Error(t, err)
	assert.Equal(t, 1, svc.broadcastCalls)
	assert.Equal(t, 0, svc.getTxCalls)
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
