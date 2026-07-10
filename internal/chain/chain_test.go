package chain

import (
	"context"
	"errors"
	"math/big"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
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
