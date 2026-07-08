package tx

import (
	"context"
	"log/slog"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	flatfeestypes "github.com/provenance-io/provenance/x/flatfees/types"
)

// Estimator estimates fees and gas for unsigned transactions without depending on chain types.
type Estimator interface {
	EstimateTxFees(ctx context.Context, txBytes []byte, gasAdjustment float32) (*flatfeestypes.QueryCalculateTxFeesResponse, error)
}

// AccountFetcher provides account number and sequence for an address without coupling to chain logic.
type AccountFetcher func(ctx context.Context, address string) (accNum, sequence uint64, err error)

// Submitter combines signing, fee estimation, account lookup, and broadcasting for ordered and unordered flows.
type Submitter struct {
	Signer        *Signer
	Estimator     Estimator
	Broadcaster   *Broadcaster
	Account       AccountFetcher
	GasAdjustment float32
	Logger        *slog.Logger // optional
}

// SubmitOrdered builds, signs, and broadcasts an ordered tx using account sequence for replay protection.
func (s *Submitter) SubmitOrdered(ctx context.Context, msg sdk.Msg) (string, error) {
	accNum, seq, err := s.Account(ctx, s.Signer.Address())
	if err != nil {
		return "", err
	}
	simBytes, err := s.Signer.SimOrdered(msg)
	if err != nil {
		return "", err
	}
	feeResp, err := s.Estimator.EstimateTxFees(ctx, simBytes, s.GasAdjustment)
	if err != nil {
		return "", err
	}
	s.logFees("ordered", accNum, feeResp)

	signed, err := s.Signer.BuildOrdered(ctx, msg, feeResp.TotalFees, feeResp.EstimatedGas, accNum, seq)
	if err != nil {
		return "", err
	}
	return s.Broadcaster.BroadcastAndConfirm(ctx, signed)
}

// SubmitUnordered builds, signs, and broadcasts an unordered tx using timeout-based replay protection.
func (s *Submitter) SubmitUnordered(ctx context.Context, msg sdk.Msg, accNum uint64, timeout time.Duration) (string, error) {
	if accNum == 0 {
		var err error
		if accNum, _, err = s.Account(ctx, s.Signer.Address()); err != nil {
			return "", err
		}
	}
	simBytes, err := s.Signer.SimUnordered(msg, timeout)
	if err != nil {
		return "", err
	}
	feeResp, err := s.Estimator.EstimateTxFees(ctx, simBytes, s.GasAdjustment)
	if err != nil {
		return "", err
	}
	s.logFees("unordered", accNum, feeResp)

	signed, err := s.Signer.BuildUnordered(ctx, msg, feeResp.TotalFees, feeResp.EstimatedGas, accNum, timeout)
	if err != nil {
		return "", err
	}
	return s.Broadcaster.BroadcastAndConfirm(ctx, signed)
}

func (s *Submitter) logFees(mode string, accNum uint64, feeResp *flatfeestypes.QueryCalculateTxFeesResponse) {
	if s.Logger == nil {
		return
	}
	s.Logger.Info("fees estimated", "mode", mode, "account_number", accNum, "estimated_gas", feeResp.EstimatedGas, "total_fees", feeResp.TotalFees.String())
}
