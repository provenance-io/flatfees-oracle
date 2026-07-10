package chain

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	flatfeestypes "github.com/provenance-io/provenance/x/flatfees/types"

	"github.com/provenance-io/flatfees-oracle/internal/convert"
)

// fakeQC is an in-memory QueryClient for testing the read path.
type fakeQC struct {
	params   flatfeestypes.Params
	paramErr error
	fees     *flatfeestypes.QueryCalculateTxFeesResponse
	feesErr  error
}

func (f *fakeQC) Params(_ context.Context, _ *flatfeestypes.QueryParamsRequest, _ ...grpc.CallOption) (*flatfeestypes.QueryParamsResponse, error) {
	if f.paramErr != nil {
		return nil, f.paramErr
	}
	return &flatfeestypes.QueryParamsResponse{Params: f.params}, nil
}

func (f *fakeQC) CalculateTxFees(_ context.Context, _ *flatfeestypes.QueryCalculateTxFeesRequest, _ ...grpc.CallOption) (*flatfeestypes.QueryCalculateTxFeesResponse, error) {
	if f.feesErr != nil {
		return nil, f.feesErr
	}
	return f.fees, nil
}

func TestCurrentParams(t *testing.T) {
	want := flatfeestypes.Params{OracleAddresses: []string{"pb1oracle"}}
	r := NewReaderWithClient(&fakeQC{params: want})

	got, err := r.CurrentParams(context.Background())
	require.NoError(t, err, "CurrentParams")
	assert.Equal(t, []string{"pb1oracle"}, got.OracleAddresses, "OracleAddresses")
}

func TestCurrentParamsError(t *testing.T) {
	r := NewReaderWithClient(&fakeQC{paramErr: errors.New("rpc down")})
	_, err := r.CurrentParams(context.Background())
	assert.EqualError(t, err, "query flatfees params: rpc down")
}

func TestEstimateTxFees(t *testing.T) {
	r := NewReaderWithClient(&fakeQC{fees: &flatfeestypes.QueryCalculateTxFeesResponse{EstimatedGas: 123456}})
	resp, err := r.EstimateTxFees(context.Background(), []byte("tx"), 1.2)
	require.NoError(t, err, "EstimateTxFees")
	assert.Equal(t, uint64(123456), resp.EstimatedGas)
}

// flakeyQC is a QueryClient that returns a transient gRPC error for the first
// paramFailures / feeFailures calls, then succeeds. Used to verify the retry
// wrapper actually kicks in at the Reader boundary.
type flakeyQC struct {
	paramFailures int
	feeFailures   int
	params        flatfeestypes.Params
	fees          *flatfeestypes.QueryCalculateTxFeesResponse
	paramCalls    int
	feeCalls      int
}

func (f *flakeyQC) Params(_ context.Context, _ *flatfeestypes.QueryParamsRequest, _ ...grpc.CallOption) (*flatfeestypes.QueryParamsResponse, error) {
	f.paramCalls++
	if f.paramCalls <= f.paramFailures {
		return nil, status.Error(codes.Unavailable, "node warming up")
	}
	return &flatfeestypes.QueryParamsResponse{Params: f.params}, nil
}

func (f *flakeyQC) CalculateTxFees(_ context.Context, _ *flatfeestypes.QueryCalculateTxFeesRequest, _ ...grpc.CallOption) (*flatfeestypes.QueryCalculateTxFeesResponse, error) {
	f.feeCalls++
	if f.feeCalls <= f.feeFailures {
		return nil, status.Error(codes.Unavailable, "node warming up")
	}
	return f.fees, nil
}

func TestCurrentParamsRetriesTransientErrors(t *testing.T) {
	q := &flakeyQC{paramFailures: 2, params: flatfeestypes.Params{OracleAddresses: []string{"pb1x"}}}
	r := NewReaderWithClient(q)

	got, err := r.CurrentParams(context.Background())
	require.NoError(t, err, "CurrentParams should recover after transient errors")
	assert.Equal(t, []string{"pb1x"}, got.OracleAddresses)
	assert.Equal(t, 3, q.paramCalls, "should retry twice before succeeding")
}

func TestEstimateTxFeesRetriesTransientErrors(t *testing.T) {
	q := &flakeyQC{feeFailures: 1, fees: &flatfeestypes.QueryCalculateTxFeesResponse{EstimatedGas: 42}}
	r := NewReaderWithClient(q)

	resp, err := r.EstimateTxFees(context.Background(), []byte("tx"), 1.5)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), resp.EstimatedGas)
	assert.Equal(t, 2, q.feeCalls, "should retry once before succeeding")
}

// permanentQC returns a permanent (non-retryable) gRPC error every time.
type permanentQC struct{ paramCalls int }

func (p *permanentQC) Params(_ context.Context, _ *flatfeestypes.QueryParamsRequest, _ ...grpc.CallOption) (*flatfeestypes.QueryParamsResponse, error) {
	p.paramCalls++
	return nil, status.Error(codes.InvalidArgument, "no such module")
}
func (p *permanentQC) CalculateTxFees(_ context.Context, _ *flatfeestypes.QueryCalculateTxFeesRequest, _ ...grpc.CallOption) (*flatfeestypes.QueryCalculateTxFeesResponse, error) {
	return nil, status.Error(codes.InvalidArgument, "no such module")
}

func TestCurrentParamsDoesNotRetryPermanentErrors(t *testing.T) {
	q := &permanentQC{}
	r := NewReaderWithClient(q)

	_, err := r.CurrentParams(context.Background())
	require.Error(t, err)
	assert.Equal(t, 1, q.paramCalls, "permanent InvalidArgument must not be retried")
}

func TestIsFactorSet(t *testing.T) {
	nonZero := flatfeestypes.ConversionFactor{
		DefinitionAmount: sdk.NewCoin("musd", math.NewInt(50)),
		ConvertedAmount:  sdk.NewCoin("nhash", math.NewInt(1_000_000_000)),
	}
	assert.True(t, IsFactorSet(nonZero), "non-zero factor must be considered set")

	zeroDef := flatfeestypes.ConversionFactor{
		DefinitionAmount: sdk.NewCoin("musd", math.ZeroInt()),
		ConvertedAmount:  sdk.NewCoin("nhash", math.NewInt(1_000_000_000)),
	}
	assert.False(t, IsFactorSet(zeroDef), "zero definition must not be considered set")

	zeroConv := flatfeestypes.ConversionFactor{
		DefinitionAmount: sdk.NewCoin("musd", math.NewInt(50)),
		ConvertedAmount:  sdk.NewCoin("nhash", math.ZeroInt()),
	}
	assert.False(t, IsFactorSet(zeroConv), "zero converted must not be considered set")

	assert.False(t, IsFactorSet(flatfeestypes.ConversionFactor{}),
		"empty (bootstrap) factor must not be considered set")
}

func TestImpliedPrice(t *testing.T) {
	// def=50 musd, conv=1e9 nhash → P = 50 * 1e6 / 1e9 = 0.05 USD/HASH
	f := flatfeestypes.ConversionFactor{
		DefinitionAmount: sdk.NewCoin("musd", math.NewInt(50)),
		ConvertedAmount:  sdk.NewCoin("nhash", math.NewInt(1_000_000_000)),
	}
	p := ImpliedPrice(f)
	require.NotNil(t, p)
	want := big.NewRat(5, 100)
	assert.Zerof(t, p.Cmp(want), "ImpliedPrice = %s, want 0.05", p.FloatString(6))
}

func TestImpliedPriceRoundTripsThroughCompute(t *testing.T) {
	// Every price in the convert package's tier examples must round-trip:
	//   Compute(P) → ToModuleFactor → ImpliedPrice ≈ P (within rounding budget)
	for _, price := range []string{"1", "0.05", "0.01", "0.009", "0.005", "0.00001", "0.000005"} {
		t.Run(price, func(t *testing.T) {
			r, ok := new(big.Rat).SetString(price)
			require.True(t, ok)

			cf, err := convert.Compute(r)
			require.NoError(t, err)
			mf, err := ToModuleFactor(cf)
			require.NoError(t, err)

			implied := ImpliedPrice(mf)
			require.NotNil(t, implied)

			// definition_amount is rounded to the nearest integer musd, so the
			// implied price can differ from P by at most 0.5 musd worth of value.
			// That means |implied - P| / P <= 0.5 * 1e-6 / P * (1e9 / conv).
			// The tests below only need the diff to be tiny — 1% is huge slack.
			diff := new(big.Rat).Sub(implied, r)
			diff.Abs(diff)
			relBound := new(big.Rat).Mul(r, big.NewRat(1, 100)) // 1% of P
			assert.LessOrEqualf(t, diff.Cmp(relBound), 0,
				"implied %s differs from P=%s by more than 1%%", implied.FloatString(12), price)
		})
	}
}

func TestImpliedPriceReturnsNilForUnsetFactor(t *testing.T) {
	assert.Nil(t, ImpliedPrice(flatfeestypes.ConversionFactor{}),
		"ImpliedPrice must return nil on a bootstrap/unset factor so callers can decide what to do")
}

// fakeAcctQC implements accountQC for AccountInfo tests. It can return a
// sequence of gRPC errors before returning its configured response, which
// lets us exercise the retry path deterministically.
type fakeAcctQC struct {
	resp                  *authtypes.QueryAccountResponse
	err                   error
	failuresBeforeSuccess int
	calls                 int
}

func (f *fakeAcctQC) Account(_ context.Context, _ *authtypes.QueryAccountRequest, _ ...grpc.CallOption) (*authtypes.QueryAccountResponse, error) {
	f.calls++
	if f.calls <= f.failuresBeforeSuccess {
		return nil, status.Error(codes.Unavailable, "node warming up")
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// newTestCodec builds a codec with the interface registrations needed to
// pack + unpack BaseAccount responses.
func newTestCodec(t *testing.T) codec.Codec {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	authtypes.RegisterInterfaces(registry)
	return codec.NewProtoCodec(registry)
}

// packAccount builds a QueryAccountResponse carrying an Any-wrapped
// BaseAccount with the given number and sequence.
func packAccount(t *testing.T, accNum, seq uint64) *authtypes.QueryAccountResponse {
	t.Helper()
	baseAcc := &authtypes.BaseAccount{
		Address:       "pb1testaccount",
		AccountNumber: accNum,
		Sequence:      seq,
	}
	anyAcc, err := codectypes.NewAnyWithValue(baseAcc)
	require.NoError(t, err, "pack BaseAccount into Any")
	return &authtypes.QueryAccountResponse{Account: anyAcc}
}

func TestAccountInfoSuccess(t *testing.T) {
	qc := &fakeAcctQC{resp: packAccount(t, 7, 42)}

	accNum, seq, err := accountInfoFromQC(context.Background(), qc, newTestCodec(t), "pb1testaccount")
	require.NoError(t, err)
	assert.Equal(t, uint64(7), accNum, "account number")
	assert.Equal(t, uint64(42), seq, "sequence")
	assert.Equal(t, 1, qc.calls, "happy path must query exactly once")
}

func TestAccountInfoRetriesTransientErrors(t *testing.T) {
	qc := &fakeAcctQC{
		resp:                  packAccount(t, 9, 100),
		failuresBeforeSuccess: 2,
	}

	accNum, seq, err := accountInfoFromQC(context.Background(), qc, newTestCodec(t), "pb1testaccount")
	require.NoError(t, err, "must recover after transient failures")
	assert.Equal(t, uint64(9), accNum)
	assert.Equal(t, uint64(100), seq)
	assert.Equal(t, 3, qc.calls, "should retry twice before succeeding")
}

func TestAccountInfoDoesNotRetryPermanentErrors(t *testing.T) {
	qc := &fakeAcctQC{err: status.Error(codes.NotFound, "no such account")}

	_, _, err := accountInfoFromQC(context.Background(), qc, newTestCodec(t), "pb1missing")
	require.Error(t, err)
	assert.ErrorContains(t, err, "query account pb1missing")
	assert.ErrorContains(t, err, "no such account")
	assert.Equal(t, 1, qc.calls, "NotFound is permanent — must not retry")
}

func TestAccountInfoExhaustsRetriesAndWraps(t *testing.T) {
	// All attempts fail with a transient error → retry helper exhausts and
	// AccountInfo wraps with "query account <addr>".
	qc := &fakeAcctQC{err: status.Error(codes.Unavailable, "still down")}

	_, _, err := accountInfoFromQC(context.Background(), qc, newTestCodec(t), "pb1down")
	require.Error(t, err)
	assert.ErrorContains(t, err, "query account pb1down")
	assert.ErrorContains(t, err, "still down")
	assert.Equal(t, 3, qc.calls, "default retry config allows three attempts")
}

func TestAccountInfoUnpackFailure(t *testing.T) {
	// Response with a nil Account payload — the unpack step must fail with
	// "unpack account <addr>" (and NOT retry, since it's a codec problem).
	qc := &fakeAcctQC{resp: &authtypes.QueryAccountResponse{Account: nil}}

	_, _, err := accountInfoFromQC(context.Background(), qc, newTestCodec(t), "pb1testaccount")
	require.Error(t, err)
	assert.ErrorContains(t, err, "unpack account pb1testaccount")
	assert.Equal(t, 1, qc.calls, "unpack errors don't trigger a retry")
}

func TestToModuleFactorAndSame(t *testing.T) {
	cf, err := convert.Compute(big.NewRat(5, 100)) // $0.05 -> def 50 musd, conv 1e9 nhash
	require.NoError(t, err, "Compute(big.NewRat(5, 100))")
	mf, err := ToModuleFactor(cf)
	require.NoError(t, err, "ToModuleFactor(cf)")

	assert.Equal(t, convert.DenomMusd, mf.DefinitionAmount.Denom, "DefinitionAmount.Denom")
	assert.Equal(t, convert.DenomNhash, mf.ConvertedAmount.Denom, "ConvertedAmount.Denom")
	assert.Equal(t, "50", mf.DefinitionAmount.Amount.String(), "DefinitionAmount.Amount")

	same := flatfeestypes.ConversionFactor{
		DefinitionAmount: sdk.NewCoin(convert.DenomMusd, mf.DefinitionAmount.Amount),
		ConvertedAmount:  sdk.NewCoin(convert.DenomNhash, mf.ConvertedAmount.Amount),
	}
	assert.True(t, SameFactor(mf, same), "identical factors should match\nexpected: %#v\n  actual: %#v", same, mf)

	other, err := convert.Compute(big.NewRat(6, 100))
	require.NoError(t, err, "Compute(big.NewRat(6, 100))")
	omf, err := ToModuleFactor(other)
	require.NoError(t, err, "ToModuleFactor(other)")
	assert.False(t, SameFactor(mf, omf), "different factors should not match")
}

func TestIsAuthorizedOracle(t *testing.T) {
	p := flatfeestypes.Params{OracleAddresses: []string{"pb1a", "pb1b"}}

	tests := []struct {
		name   string
		params flatfeestypes.Params
		addr   string
		exp    bool
	}{
		{name: "nil list", params: flatfeestypes.Params{OracleAddresses: nil}, addr: "abc", exp: false},
		{name: "empty list", params: flatfeestypes.Params{OracleAddresses: []string{}}, addr: "abc", exp: false},
		{name: "first of two", params: p, addr: "pb1a", exp: true},
		{name: "second of two", params: p, addr: "pb1b", exp: true},
		{name: "not in two", params: p, addr: "pb1zzz", exp: false},
		{name: "leading space", params: p, addr: " pb1a", exp: false},
		{name: "trailing space", params: p, addr: "pb1a ", exp: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var act bool
			testFunc := func() {
				act = IsAuthorizedOracle(tc.params, tc.addr)
			}
			require.NotPanics(t, testFunc, "IsAuthorizedOracle")
			assert.Equal(t, tc.exp, act, "IsAuthorizedOracle result")
		})
	}
}
