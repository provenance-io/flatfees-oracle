// Package chain talks to a Provenance node's x/flatfees module: it reads the
// current conversion factor (and oracle list), maps a computed factor into the
// module's types, estimates gas/fees via CalculateTxFees, and submits a
// MsgUpdateConversionFactorRequest signed by the oracle key.
//
// It imports the real Provenance and Cosmos SDK types:
//   - github.com/provenance-io/provenance/x/flatfees/types
//   - github.com/cosmos/cosmos-sdk/types (sdk.Coin)
//   - cosmossdk.io/math (math.Int)
//
// IMPORTANT build note: because this module imports github.com/provenance-io/
// provenance, go.mod MUST mirror provenance's `replace` directives (notably the
// cosmos-sdk -> provenance-io/cosmos-sdk fork). Run `go mod tidy` locally after
// adding the deps. This code could not be compiled in the authoring environment
// (no Go toolchain / no module network), so verify signatures against
// cosmos-sdk v0.53 when wiring the broadcast path.
package chain

import (
	"context"
	"fmt"
	"math/big"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	grpc1 "github.com/cosmos/gogoproto/grpc"
	"google.golang.org/grpc"

	flatfeestypes "github.com/provenance-io/provenance/x/flatfees/types"

	"github.com/provenance-io/flatfees-oracle/internal/convert"
	"github.com/provenance-io/flatfees-oracle/internal/retry"
)

// QueryClient is the subset of the flatfees query client this package needs.
// flatfeestypes.QueryClient satisfies it; narrowing the interface keeps the
// read path unit-testable with a fake.
type QueryClient interface {
	Params(ctx context.Context, in *flatfeestypes.QueryParamsRequest, opts ...grpc.CallOption) (*flatfeestypes.QueryParamsResponse, error)
	CalculateTxFees(ctx context.Context, in *flatfeestypes.QueryCalculateTxFeesRequest, opts ...grpc.CallOption) (*flatfeestypes.QueryCalculateTxFeesResponse, error)
}

// Reader reads flatfees state from a node.
type Reader struct {
	qc QueryClient
}

// NewReader builds a Reader from a gRPC client connection (e.g. the one returned
// by grpc.Dial against the node's gRPC endpoint).
func NewReader(conn grpc1.ClientConn) *Reader {
	return &Reader{qc: flatfeestypes.NewQueryClient(conn)}
}

// NewReaderWithClient is for tests: inject a fake QueryClient.
func NewReaderWithClient(qc QueryClient) *Reader {
	return &Reader{qc: qc}
}

// CurrentParams returns the live flatfees params, including the current
// conversion factor and the set of authorized oracle addresses. Retries on
// transient gRPC errors so a single node blip doesn't fail the daily run.
func (r *Reader) CurrentParams(ctx context.Context) (flatfeestypes.Params, error) {
	var params flatfeestypes.Params
	err := retry.Do(ctx, retry.Default(), func() error {
		resp, err := r.qc.Params(ctx, &flatfeestypes.QueryParamsRequest{})
		if err != nil {
			return err
		}
		params = resp.Params
		return nil
	})
	if err != nil {
		return flatfeestypes.Params{}, fmt.Errorf("query flatfees params: %w", err)
	}
	return params, nil
}

// EstimateTxFees runs the CalculateTxFees query for an encoded (unsigned) tx,
// returning the estimated gas and the flat total fee to set on the tx. Retries
// on transient gRPC errors.
func (r *Reader) EstimateTxFees(ctx context.Context, txBytes []byte, gasAdjustment float32) (*flatfeestypes.QueryCalculateTxFeesResponse, error) {
	var resp *flatfeestypes.QueryCalculateTxFeesResponse
	err := retry.Do(ctx, retry.Default(), func() error {
		var callErr error
		resp, callErr = r.qc.CalculateTxFees(ctx, &flatfeestypes.QueryCalculateTxFeesRequest{
			TxBytes:       txBytes,
			GasAdjustment: gasAdjustment,
		})
		return callErr
	})
	if err != nil {
		return nil, fmt.Errorf("calculate tx fees: %w", err)
	}
	return resp, nil
}

// ToModuleFactor maps a computed convert.ConversionFactor into the flatfees
// module's ConversionFactor, with the fixed denoms (musd / nhash).
func ToModuleFactor(f convert.ConversionFactor) (flatfeestypes.ConversionFactor, error) {
	defAmt, ok := intFromBig(f.DefinitionAmount)
	if !ok {
		return flatfeestypes.ConversionFactor{}, fmt.Errorf("invalid definition_amount %v", f.DefinitionAmount)
	}
	convAmt, ok := intFromBig(f.ConvertedAmount)
	if !ok {
		return flatfeestypes.ConversionFactor{}, fmt.Errorf("invalid converted_amount %v", f.ConvertedAmount)
	}
	return flatfeestypes.ConversionFactor{
		DefinitionAmount: sdk.NewCoin(convert.DenomMusd, defAmt),
		ConvertedAmount:  sdk.NewCoin(convert.DenomNhash, convAmt),
	}, nil
}

// SameFactor reports whether a freshly computed factor equals the one currently
// on chain. Used for the skip-if-unchanged decision. Compared field-by-field to
// avoid depending on a specific sdk.Coin equality method name across SDK versions.
func SameFactor(current flatfeestypes.ConversionFactor, computed flatfeestypes.ConversionFactor) bool {
	return coinEqual(current.DefinitionAmount, computed.DefinitionAmount) &&
		coinEqual(current.ConvertedAmount, computed.ConvertedAmount)
}

func coinEqual(a, b sdk.Coin) bool {
	return a.Denom == b.Denom && a.Amount.Equal(b.Amount)
}

// BuildUpdateMsg constructs the MsgUpdateConversionFactorRequest. authority must
// be a bech32 address that is registered in the module's oracle_addresses.
func BuildUpdateMsg(authority string, factor flatfeestypes.ConversionFactor) *flatfeestypes.MsgUpdateConversionFactorRequest {
	return &flatfeestypes.MsgUpdateConversionFactorRequest{
		Authority:        authority,
		ConversionFactor: factor,
	}
}

// IsAuthorizedOracle reports whether addr is in the module's oracle address list.
func IsAuthorizedOracle(params flatfeestypes.Params, addr string) bool {
	for _, o := range params.OracleAddresses {
		if o == addr {
			return true
		}
	}
	return false
}

// IsFactorSet reports whether the on-chain factor has real amounts on both
// sides. Fresh chains / bootstrap runs may present an all-zero factor; the
// sanity-band check must skip when there's nothing meaningful to compare
// against.
func IsFactorSet(f flatfeestypes.ConversionFactor) bool {
	return !f.DefinitionAmount.Amount.IsNil() && !f.DefinitionAmount.Amount.IsZero() &&
		!f.ConvertedAmount.Amount.IsNil() && !f.ConvertedAmount.Amount.IsZero()
}

// ImpliedPrice recovers the USD-per-HASH price implied by a flatfees factor.
//
// From the module convention:
//
//	converted_amount / definition_amount = 1e6 / P
//
// where converted_amount is in nhash and definition_amount is in musd.
// Rearranging:
//
//	P = definition_amount * 1e6 / converted_amount
//
// Returns nil for a zero/unset factor (bootstrap case — caller should check
// IsFactorSet first if that matters).
func ImpliedPrice(f flatfeestypes.ConversionFactor) *big.Rat {
	if !IsFactorSet(f) {
		return nil
	}
	def := new(big.Rat).SetInt(f.DefinitionAmount.Amount.BigInt())
	conv := new(big.Rat).SetInt(f.ConvertedAmount.Amount.BigInt())
	// P = def * 1e6 / conv
	p := new(big.Rat).Mul(def, new(big.Rat).SetInt64(1_000_000))
	return p.Quo(p, conv)
}

// intFromBig converts a *big.Int to cosmossdk.io/math.Int, rejecting nil/negative.
func intFromBig(b *big.Int) (math.Int, bool) {
	if b == nil || b.Sign() < 0 {
		return math.Int{}, false
	}
	return math.NewIntFromBigInt(b), true
}

// accountQC is the narrow subset of authtypes.QueryClient AccountInfo needs.
// Extracted so tests can inject a fake without implementing the full auth
// query surface. authtypes.QueryClient satisfies it structurally.
type accountQC interface {
	Account(ctx context.Context, in *authtypes.QueryAccountRequest, opts ...grpc.CallOption) (*authtypes.QueryAccountResponse, error)
}

// AccountInfo queries the auth module for an account's number and sequence.
// Retries on transient gRPC errors; the unpack step is deterministic and not
// retried.
func AccountInfo(ctx context.Context, conn grpc1.ClientConn, cdc codec.Codec, addr string) (accNum, sequence uint64, err error) {
	return accountInfoFromQC(ctx, authtypes.NewQueryClient(conn), cdc, addr)
}

// accountInfoFromQC is the testable core of AccountInfo. Kept unexported so
// callers pin against the gRPC ClientConn wrapper; tests exercise this
// directly with a fake accountQC.
func accountInfoFromQC(ctx context.Context, qc accountQC, cdc codec.Codec, addr string) (uint64, uint64, error) {
	var resp *authtypes.QueryAccountResponse
	err := retry.Do(ctx, retry.Default(), func() error {
		var callErr error
		resp, callErr = qc.Account(ctx, &authtypes.QueryAccountRequest{Address: addr})
		return callErr
	})
	if err != nil {
		return 0, 0, fmt.Errorf("query account %s: %w", addr, err)
	}
	if resp == nil || resp.Account == nil {
		return 0, 0, fmt.Errorf("unpack account %s: nil account payload in response", addr)
	}
	var acc sdk.AccountI
	if err := cdc.UnpackAny(resp.Account, &acc); err != nil {
		return 0, 0, fmt.Errorf("unpack account %s: %w", addr, err)
	}
	if acc == nil {
		return 0, 0, fmt.Errorf("unpack account %s: codec returned nil account", addr)
	}
	return acc.GetAccountNumber(), acc.GetSequence(), nil
}
