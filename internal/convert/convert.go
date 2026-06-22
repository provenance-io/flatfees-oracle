// Package convert turns a HASH price (USD per HASH) into the flatfees module's
// ConversionFactor, using a price-tiered scale for the nhash converted_amount so
// the musd definition_amount stays a healthy integer at every price.
//
// The module applies: actual cost = defined cost * converted_amount / definition_amount.
// The fee denom (nhash) per musd is 1e6 / P, so:
//
//	converted_amount / definition_amount = 1e6 / P
//	definition_amount                    = converted_amount * P / 1e6
//
// converted_amount grows by 1000x as the price drops a tier, keeping
// definition_amount an integer roughly in the [10, 10000) range:
//
//	HASH price (USD/HASH)        converted_amount (nhash)     definition_amount (musd)
//	>= $0.01                     1e9                          1000 * P
//	>= $0.00001 and < $0.01      1e12                         1e6 * P
//	< $0.00001                   1e15                         1e9 * P
//
// Scaling facts: 1 musd = $0.001 (1000 musd = $1.00); 1 HASH = 1e9 nhash.
package convert

import (
	"fmt"
	"math/big"
)

// Denominations used by the flatfees ConversionFactor.
const (
	DenomMusd  = "musd"
	DenomNhash = "nhash"
)

// Tier thresholds and converted_amount scales. Expressed as big values so the
// math is exact.
var (
	// thresholdHigh is $0.01 (USD/HASH): at or above this, use the 1e9 scale.
	thresholdHigh = big.NewRat(1, 100)
	// thresholdMid is $0.00001 (USD/HASH): at or above this (but below high),
	// use the 1e12 scale; below it, use the 1e15 scale.
	thresholdMid = big.NewRat(1, 100000)

	convertedHigh = pow10(9)  // price >= $0.01
	convertedMid  = pow10(12) // $0.00001 <= price < $0.01
	convertedLow  = pow10(15) // price < $0.00001

	// oneMillion is the 1e6 divisor from the cost-formula relationship.
	oneMillion = big.NewInt(1_000_000)
)

// Tier identifies which price band a factor was computed in. Useful for logging.
type Tier string

const (
	TierHigh Tier = "high" // price >= $0.01
	TierMid  Tier = "mid"  // $0.00001 <= price < $0.01
	TierLow  Tier = "low"  // price < $0.00001
)

// ConversionFactor is the computed flatfees conversion factor. Amounts are exact
// integers; the chain layer maps these to cosmossdk.io/math.Int Coins.
type ConversionFactor struct {
	// DefinitionAmount is the musd side of the factor (the USD-denominated anchor).
	DefinitionAmount *big.Int
	// ConvertedAmount is the nhash side of the factor (the fee-denominated side).
	ConvertedAmount *big.Int
	// Tier records which price band was used (for observability).
	Tier Tier
}

// Equal reports whether two factors are identical (same amounts and tier-agnostic
// value). Used for the skip-if-unchanged check against the on-chain value.
func (f ConversionFactor) Equal(other ConversionFactor) bool {
	if f.DefinitionAmount == nil || other.DefinitionAmount == nil ||
		f.ConvertedAmount == nil || other.ConvertedAmount == nil {
		return false
	}
	return f.DefinitionAmount.Cmp(other.DefinitionAmount) == 0 &&
		f.ConvertedAmount.Cmp(other.ConvertedAmount) == 0
}

// String renders the factor as "definition_amount musd = converted_amount nhash".
func (f ConversionFactor) String() string {
	return fmt.Sprintf("%s %s = %s %s",
		f.DefinitionAmount, DenomMusd, f.ConvertedAmount, DenomNhash)
}

// selectTier returns the converted_amount (nhash) scale and tier label for a price.
func selectTier(priceUSDPerHASH *big.Rat) (*big.Int, Tier) {
	switch {
	case priceUSDPerHASH.Cmp(thresholdHigh) >= 0:
		return new(big.Int).Set(convertedHigh), TierHigh
	case priceUSDPerHASH.Cmp(thresholdMid) >= 0:
		return new(big.Int).Set(convertedMid), TierMid
	default:
		return new(big.Int).Set(convertedLow), TierLow
	}
}

// Compute builds the ConversionFactor for a HASH price expressed in USD per HASH.
// The price must be strictly positive.
func Compute(priceUSDPerHASH *big.Rat) (ConversionFactor, error) {
	if priceUSDPerHASH == nil || priceUSDPerHASH.Sign() <= 0 {
		return ConversionFactor{}, fmt.Errorf("price must be > 0, got %v", priceUSDPerHASH)
	}

	converted, tier := selectTier(priceUSDPerHASH)

	// definition_amount = converted_amount * price / 1e6 (rounded to nearest musd).
	def := new(big.Rat).SetInt(converted)
	def.Mul(def, priceUSDPerHASH)
	def.Quo(def, new(big.Rat).SetInt(oneMillion))

	definition := roundHalfUp(def)
	if definition.Sign() <= 0 {
		// Should be unreachable given the tiers, but the module forbids a zero
		// definition_amount, so guard explicitly.
		return ConversionFactor{}, fmt.Errorf(
			"computed definition_amount rounded to %s for price %s; refusing",
			definition, priceUSDPerHASH.FloatString(12))
	}

	return ConversionFactor{
		DefinitionAmount: definition,
		ConvertedAmount:  converted,
		Tier:             tier,
	}, nil
}

// roundHalfUp rounds a non-negative rational to the nearest integer, ties away
// from zero: floor((2*num + den) / (2*den)).
func roundHalfUp(r *big.Rat) *big.Int {
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())

	twoNum := new(big.Int).Lsh(num, 1)     // 2*num
	twoNum.Add(twoNum, den)                // 2*num + den
	twoDen := new(big.Int).Lsh(den, 1)     // 2*den
	return new(big.Int).Quo(twoNum, twoDen) // integer (truncated) division
}

// pow10 returns 10^n as a *big.Int.
func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
