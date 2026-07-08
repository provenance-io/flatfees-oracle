package tx

import (
	"context"
	"errors"
	"fmt"
	"time"

	grpc1 "github.com/cosmos/gogoproto/grpc"

	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
)

// Broadcaster submits signed transactions and waits for confirmation.
type Broadcaster struct {
	svc          txtypes.ServiceClient
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func NewBroadcaster(conn grpc1.ClientConn) *Broadcaster {
	return &Broadcaster{
		svc:          txtypes.NewServiceClient(conn),
		PollInterval: 2 * time.Second,
		PollTimeout:  60 * time.Second,
	}
}

func NewBroadcasterWithClient(svc txtypes.ServiceClient) *Broadcaster {
	return &Broadcaster{svc: svc, PollInterval: 10 * time.Millisecond, PollTimeout: time.Second}
}

// Broadcast submits txBytes in SYNC mode and returns the transaction hash or a CheckTx error.
func (b *Broadcaster) Broadcast(ctx context.Context, txBytes []byte) (string, error) {
	resp, err := b.svc.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
		TxBytes: txBytes,
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	})
	if err != nil {
		return "", fmt.Errorf("broadcast tx: %w", err)
	}
	if resp.TxResponse == nil {
		return "", errors.New("broadcast tx: nil tx response")
	}
	if resp.TxResponse.Code != 0 {
		return resp.TxResponse.TxHash, fmt.Errorf("tx rejected in checkTx: code %d: %s",
			resp.TxResponse.Code, resp.TxResponse.RawLog)
	}
	return resp.TxResponse.TxHash, nil
}

// Confirm waits for the transaction to be included and verifies it succeeded.
func (b *Broadcaster) Confirm(ctx context.Context, hash string) (*sdk.TxResponse, error) {
	deadline := time.Now().Add(b.PollTimeout)
	var lastErr error
	for {
		resp, err := b.svc.GetTx(ctx, &txtypes.GetTxRequest{Hash: hash})
		if err == nil && resp.TxResponse != nil {
			if resp.TxResponse.Code != 0 {
				return resp.TxResponse, fmt.Errorf("tx %s failed in deliverTx: code %d: %s",
					hash, resp.TxResponse.Code, resp.TxResponse.RawLog)
			}
			return resp.TxResponse, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("tx %s not confirmed within %s: %w", hash, b.PollTimeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(b.PollInterval):
		}
	}
}

func (b *Broadcaster) BroadcastAndConfirm(ctx context.Context, txBytes []byte) (string, error) {
	hash, err := b.Broadcast(ctx, txBytes)
	if err != nil {
		return hash, err
	}
	if _, err := b.Confirm(ctx, hash); err != nil {
		return hash, err
	}
	return hash, nil
}
