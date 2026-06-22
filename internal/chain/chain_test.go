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
