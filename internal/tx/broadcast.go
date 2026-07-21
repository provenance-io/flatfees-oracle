package tx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	grpc1 "github.com/cosmos/gogoproto/grpc"

	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"

	"github.com/provenance-io/flatfees-oracle/internal/retry"
)

// errNilTxResponse is recorded in lastErr when GetTx returns success with no
// TxResponse — without this the timeout message would render `%!w(<nil>)`.
var errNilTxResponse = errors.New("GetTx returned nil TxResponse")

// Broadcast error sentinels. Callers use errors.Is to decide whether the tx
// may still land on chain despite the broadcast reporting failure.
var (
	// ErrBroadcastRPC wraps any gRPC-level failure from BroadcastTx (timeout,
	// unavailable, aborted, etc.). The tx MAY have reached the node before the
	// response was lost — BroadcastAndConfirm falls back to Confirm on the
	// locally-computed hash to catch that case.
	ErrBroadcastRPC = errors.New("broadcast tx")

	// ErrCheckTxRejected is a real CheckTx-level rejection (insufficient fees,
	// invalid signature, sequence mismatch, etc.). The tx did NOT enter the
	// mempool; BroadcastAndConfirm returns immediately without polling.
	ErrCheckTxRejected = errors.New("tx rejected in checkTx")

	// ErrTxAlreadyInMempool is Cosmos SDK's "tx already in mempool cache"
	// (RootCodespace, code 19). The node has the tx; BroadcastAndConfirm
	// runs Confirm to wait for it to land.
	ErrTxAlreadyInMempool = errors.New("tx already in mempool")
)

// cosmosSDKCodespace and cosmosCodeTxAlreadyInMempool identify the specific
// CheckTx failure that means "we've raced our own retry — the node already
// has this tx." Values pinned from cosmos-sdk/types/errors/errors.go's
// ErrTxInMempoolCache = errorsmod.Register(RootCodespace, 19, "tx already in mempool").
// Hardcoded here rather than importing sdkerrors to keep the surface small.
const (
	cosmosSDKCodespace           = "sdk"
	cosmosCodeTxAlreadyInMempool = uint32(19)
)

// Broadcaster submits signed transactions and waits for confirmation.
//
// The parent context passed to Confirm / BroadcastAndConfirm is the sole
// authority on how long the poll loop may run. Callers MUST use a context
// with a deadline (context.WithTimeout / WithDeadline); Confirm has no
// internal timeout and will loop until the ctx is done or the tx lands.
type Broadcaster struct {
	svc          txtypes.ServiceClient
	PollInterval time.Duration
}

func NewBroadcaster(conn grpc1.ClientConn) *Broadcaster {
	return &Broadcaster{
		svc:          txtypes.NewServiceClient(conn),
		PollInterval: 2 * time.Second,
	}
}

func NewBroadcasterWithClient(svc txtypes.ServiceClient) *Broadcaster {
	return &Broadcaster{svc: svc, PollInterval: 10 * time.Millisecond}
}

// Broadcast submits txBytes in SYNC mode and returns the transaction hash or a
// classified error. Errors are wrapped with one of ErrBroadcastRPC,
// ErrCheckTxRejected, or ErrTxAlreadyInMempool so BroadcastAndConfirm can
// decide whether to bother polling for confirmation.
func (b *Broadcaster) Broadcast(ctx context.Context, txBytes []byte) (string, error) {
	var resp *txtypes.BroadcastTxResponse
	err := retry.Do(ctx, retry.Broadcast(), func() error {
		var callErr error
		resp, callErr = b.svc.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
			TxBytes: txBytes,
			Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
		})
		return callErr
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrBroadcastRPC, err)
	}
	if resp == nil || resp.TxResponse == nil {
		return "", fmt.Errorf("%w: nil tx response", ErrBroadcastRPC)
	}
	if resp.TxResponse.Code != 0 {
		if isTxAlreadyInMempool(resp.TxResponse) {
			return resp.TxResponse.TxHash, fmt.Errorf("%w: %s",
				ErrTxAlreadyInMempool, resp.TxResponse.RawLog)
		}
		return resp.TxResponse.TxHash, fmt.Errorf("%w: code %d: %s",
			ErrCheckTxRejected, resp.TxResponse.Code, resp.TxResponse.RawLog)
	}
	return resp.TxResponse.TxHash, nil
}

// isTxAlreadyInMempool reports whether resp is Cosmos SDK's
// ErrTxInMempoolCache — the response we get when a retry beat the original
// broadcast's ack back and the node already has the tx.
func isTxAlreadyInMempool(resp *sdk.TxResponse) bool {
	return resp.Codespace == cosmosSDKCodespace && resp.Code == cosmosCodeTxAlreadyInMempool
}

// Confirm polls GetTx until the tx is included in a block, a DeliverTx failure
// is reported, or the context is done.
//
// IMPORTANT: ctx MUST have a deadline. Confirm has no internal timeout; called
// with context.Background() it will loop indefinitely on a lost tx. Every
// caller in this codebase wraps with context.WithTimeout on entry to the
// submit phase — new callers must do the same.
//
// On ctx cancellation the returned error wraps BOTH ctx.Err() and the last
// poll error so operators can see what GetTx was reporting when time ran out.
func (b *Broadcaster) Confirm(ctx context.Context, hash string) (*sdk.TxResponse, error) {
	var lastErr error
	for {
		resp, err := b.svc.GetTx(ctx, &txtypes.GetTxRequest{Hash: hash})
		if err == nil && resp != nil && resp.TxResponse != nil {
			if resp.TxResponse.Code != 0 {
				return resp.TxResponse, fmt.Errorf("tx %s failed in deliverTx: code %d: %s",
					hash, resp.TxResponse.Code, resp.TxResponse.RawLog)
			}
			return resp.TxResponse, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = errNilTxResponse
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("tx %s not confirmed: %w; last poll error: %w",
				hash, ctx.Err(), lastErr)
		case <-time.After(b.PollInterval):
		}
	}
}

// BroadcastAndConfirm broadcasts txBytes and waits for on-chain confirmation.
//
// Selective belt-and-suspenders on the "lost ack + retry rejected as
// duplicate" race:
//
//   - ErrTxAlreadyInMempool — the node knows about the tx; poll Confirm on
//     the hash the node returned. Success on Confirm becomes success overall.
//   - ErrBroadcastRPC — every broadcast attempt failed at the transport
//     layer, so the tx MAY or MAY NOT have reached the node. Poll Confirm on
//     the locally-computed hash. Success becomes success; timeout returns
//     the original error.
//   - Anything else (ErrCheckTxRejected — insufficient fees, bad signature,
//     sequence mismatch, etc.) — the tx did NOT enter the mempool. Return
//     immediately; Confirm would just spin against the ctx deadline to no
//     purpose.
func (b *Broadcaster) BroadcastAndConfirm(ctx context.Context, txBytes []byte) (string, error) {
	hash, err := b.Broadcast(ctx, txBytes)
	if err != nil {
		confirmHash, ok := recoveryHash(hash, txBytes, err)
		if !ok {
			// Real CheckTx rejection (or unknown wrapper). Confirm can't help.
			return hash, err
		}
		if _, confirmErr := b.Confirm(ctx, confirmHash); confirmErr == nil {
			// Broadcast complained, but the tx made it to chain. Consider the
			// run a success and surface the recovered hash.
			return confirmHash, nil
		}
		return hash, err
	}
	if _, err := b.Confirm(ctx, hash); err != nil {
		return hash, err
	}
	return hash, nil
}

// recoveryHash decides whether the broadcast error is one where the tx might
// still make it to chain, and if so, what hash to poll on. Returns ok=false
// for terminal CheckTx rejections where Confirm would be a waste of budget.
func recoveryHash(nodeHash string, txBytes []byte, err error) (string, bool) {
	switch {
	case errors.Is(err, ErrTxAlreadyInMempool):
		// Node returned the hash; prefer it. Fall back to the computed hash
		// in the (unlikely) case the response omitted TxHash.
		if nodeHash != "" {
			return nodeHash, true
		}
		return ComputeTxHash(txBytes), true
	case errors.Is(err, ErrBroadcastRPC):
		// No trustworthy hash from the node; use the locally-computed one.
		return ComputeTxHash(txBytes), true
	default:
		return "", false
	}
}

// ComputeTxHash returns the SHA-256 hex-uppercase hash Cosmos SDK uses to
// identify a transaction. Deterministic from the signed bytes, so it can be
// computed without ever hearing back from the node — the escape hatch for
// double-failure recovery in BroadcastAndConfirm.
func ComputeTxHash(txBytes []byte) string {
	sum := sha256.Sum256(txBytes)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
