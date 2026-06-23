package convert

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ratFromDecimal parses a decimal string like "0.005" into an exact *big.Rat.
func ratFromDecimal(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	require.Truef(t, ok, "could not parse rational from %q", s)
	return r
}

func TestCompute(t *testing.T) {
	cases := []struct {
		name          string
		price         string // USD per HASH
		wantTier      Tier
		wantConverted string
		wantDef       string // musd
	}{
		// Tier 1: price >= $0.01 -> converted 1e9, def = 1000*P.
		{name: "high_at_boundary", price: "0.01", wantTier: TierHigh, wantConverted: "1000000000", wantDef: "10"},
		{name: "high_mid_range", price: "0.05", wantTier: TierHigh, wantConverted: "1000000000", wantDef: "50"},
		{name: "high_dollar", price: "1", wantTier: TierHigh, wantConverted: "1000000000", wantDef: "1000"},
		{name: "high_large", price: "12.5", wantTier: TierHigh, wantConverted: "1000000000", wantDef: "12500"},

		// Tier 2: $0.00001 <= price < $0.01 -> converted 1e12, def = 1e6*P.
		{name: "mid_just_below_high", price: "0.009", wantTier: TierMid, wantConverted: "1000000000000", wantDef: "9000"},
		{name: "mid_mid", price: "0.005", wantTier: TierMid, wantConverted: "1000000000000", wantDef: "5000"},
		{name: "mid_at_lower_boundary", price: "0.00001", wantTier: TierMid, wantConverted: "1000000000000", wantDef: "10"},

		// Tier 3: price < $0.00001 -> converted 1e15, def = 1e9*P.
		{name: "low_just_below_mid", price: "0.0000099", wantTier: TierLow, wantConverted: "1000000000000000", wantDef: "9900"},
		{name: "low_small", price: "0.000005", wantTier: TierLow, wantConverted: "1000000000000000", wantDef: "5000"},
		{name: "low_tiny", price: "0.00000001", wantTier: TierLow, wantConverted: "1000000000000000", wantDef: "10"},

		// Rounding: nearest, ties away from zero.
		{name: "round_down", price: "0.0123", wantTier: TierHigh, wantConverted: "1000000000", wantDef: "12"},    // 12.3 -> 12
		{name: "round_half_up", price: "0.0125", wantTier: TierHigh, wantConverted: "1000000000", wantDef: "13"}, // 12.5 -> 13
		{name: "round_up", price: "0.0127", wantTier: TierHigh, wantConverted: "1000000000", wantDef: "13"},      // 12.7 -> 13
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Compute(ratFromDecimal(t, tc.price))
			require.NoErrorf(t, err, "Compute(%s)", tc.price)
			assert.Equal(t, tc.wantTier, got.Tier, "tier")
			assert.Equal(t, tc.wantConverted, got.ConvertedAmount.String(), "converted_amount")
			assert.Equal(t, tc.wantDef, got.DefinitionAmount.String(), "definition_amount")
		})
	}
}

// TestComputeValueEquivalence checks that converted_amount nhash and
// definition_amount musd represent (approximately) the same USD value, within the
// integer-rounding error of definition_amount (at most half a musd).
func TestComputeValueEquivalence(t *testing.T) {
	prices := []string{"0.05", "0.01", "0.009", "0.005", "0.00001", "0.000005", "0.0000099"}
	thousand := big.NewInt(1000)
	billion := pow10(9)

	for _, p := range prices {
		t.Run(p, func(t *testing.T) {
			price := ratFromDecimal(t, p)
			f, err := Compute(price)
			require.NoError(t, err, "Compute(%q)", p)

			// value(musd) = converted/1e9 (HASH) * price (USD) * 1000 (musd/USD)
			val := new(big.Rat).SetInt(f.ConvertedAmount)
			val.Quo(val, new(big.Rat).SetInt(billion))
			val.Mul(val, price)
			val.Mul(val, new(big.Rat).SetInt(thousand))

			diff := new(big.Rat).Sub(val, new(big.Rat).SetInt(f.DefinitionAmount))
			diff.Abs(diff)
			assert.LessOrEqualf(t, diff.Cmp(big.NewRat(1, 2)), 0,
				"value mismatch: converted worth %s musd vs definition %s musd",
				val.FloatString(6), f.DefinitionAmount)
		})
	}
}

func TestComputeRejectsNonPositive(t *testing.T) {
	for _, p := range []string{"0", "-1", "-0.5"} {
		_, err := Compute(ratFromDecimal(t, p))
		assert.ErrorContainsf(t, err, "price must be > 0", "Compute(%s)", p)
	}
	_, err := Compute(nil)
	assert.ErrorContains(t, err, "price must be > 0")
}

func TestEqual(t *testing.T) {
	a, err := Compute(ratFromDecimal(t, "0.05"))
	require.NoError(t, err)
	b, err := Compute(ratFromDecimal(t, "0.05"))
	require.NoError(t, err)
	c, err := Compute(ratFromDecimal(t, "0.06"))
	require.NoError(t, err)

	assert.True(t, a.Equal(a), "factors should be Equal to themselves")
	assert.True(t, a.Equal(b), "identical factors should be Equal")
	assert.True(t, b.Equal(a), "identical factors should be Equal (swapped)")
	assert.False(t, a.Equal(c), "different factors should not be Equal")
	assert.False(t, c.Equal(a), "different factors should not be Equal (swapped)")
	assert.False(t, (ConversionFactor{}).Equal(a), "empty factor should not equal a real factor")
	assert.False(t, a.Equal(ConversionFactor{}), "real factor should not equal a empty factor")
}
