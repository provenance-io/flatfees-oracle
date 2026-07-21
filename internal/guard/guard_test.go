package guard

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	require.Truef(t, ok, "could not parse rational from %q", s)
	return r
}

func TestCheckMovementInsideBand(t *testing.T) {
	// on-chain 0.05, new 0.10, band 10× → allowed (0.10 < 0.5)
	err := CheckMovement(rat(t, "0.10"), rat(t, "0.05"), 10)
	assert.NoError(t, err)
}

func TestCheckMovementAtUpperBoundary(t *testing.T) {
	// on-chain 0.05, new 0.50 (=10×), band 10× → allowed (== upper)
	err := CheckMovement(rat(t, "0.50"), rat(t, "0.05"), 10)
	assert.NoError(t, err, "exactly at the upper bound must pass")
}

func TestCheckMovementJustAboveUpper(t *testing.T) {
	// 0.05 * 10 = 0.5; 0.501 exceeds
	err := CheckMovement(rat(t, "0.501"), rat(t, "0.05"), 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMovementExceeded)
}

func TestCheckMovementJustBelowLower(t *testing.T) {
	// 0.05 / 10 = 0.005; 0.004 exceeds down
	err := CheckMovement(rat(t, "0.004"), rat(t, "0.05"), 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMovementExceeded)
}

func TestCheckMovementBigJumpUp(t *testing.T) {
	// 100× move on a 10× band — must trip
	err := CheckMovement(rat(t, "5.0"), rat(t, "0.05"), 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMovementExceeded)
	assert.Contains(t, err.Error(), "10.00x")
}

func TestCheckMovementBigJumpDown(t *testing.T) {
	err := CheckMovement(rat(t, "0.0005"), rat(t, "0.05"), 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMovementExceeded)
}

func TestCheckMovementSymmetric(t *testing.T) {
	// A 5× move up and a 5× move down must both pass a 10× band.
	err := CheckMovement(rat(t, "0.25"), rat(t, "0.05"), 10)
	assert.NoError(t, err, "5x up under 10x band should pass")

	err = CheckMovement(rat(t, "0.01"), rat(t, "0.05"), 10)
	assert.NoError(t, err, "5x down under 10x band should pass")
}

func TestCheckMovementDisabledWhenRatioLE1(t *testing.T) {
	// maxRatio <= 1 means "don't check"; any positive price passes.
	assert.NoError(t, CheckMovement(rat(t, "9999"), rat(t, "0.05"), 1))
	assert.NoError(t, CheckMovement(rat(t, "9999"), rat(t, "0.05"), 0))
	assert.NoError(t, CheckMovement(rat(t, "9999"), rat(t, "0.05"), -1))
}

func TestCheckMovementBootstrapSkips(t *testing.T) {
	// nil onChain (no factor yet) → check is skipped.
	assert.NoError(t, CheckMovement(rat(t, "0.05"), nil, 10))
}

func TestCheckMovementRejectsNonPositiveNew(t *testing.T) {
	require.Error(t, CheckMovement(nil, rat(t, "0.05"), 10))
	require.Error(t, CheckMovement(rat(t, "0"), rat(t, "0.05"), 10))
	require.Error(t, CheckMovement(rat(t, "-1"), rat(t, "0.05"), 10))
}

func TestCheckLiquidityPasses(t *testing.T) {
	err := CheckLiquidity(15, rat(t, "500"), 10, 100)
	assert.NoError(t, err)
}

func TestCheckLiquidityAtBoundary(t *testing.T) {
	// Exactly minTrades and exactly minVolume should both pass.
	err := CheckLiquidity(10, rat(t, "100"), 10, 100)
	assert.NoError(t, err, "boundary values must be inclusive")
}

func TestCheckLiquidityTooFewTrades(t *testing.T) {
	err := CheckLiquidity(9, rat(t, "500"), 10, 100)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTooFewTrades)
	assert.Contains(t, err.Error(), "9 < 10")
}

func TestCheckLiquidityInsufficientVolume(t *testing.T) {
	err := CheckLiquidity(20, rat(t, "50"), 10, 100)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientVolume)
}

func TestCheckLiquidityVolumeFloorDisabled(t *testing.T) {
	// minVolume <= 0 disables the volume check but still enforces MinTrades.
	err := CheckLiquidity(20, rat(t, "0"), 10, 0)
	assert.NoError(t, err, "minVolume=0 must disable the volume check")

	err = CheckLiquidity(1, rat(t, "1000"), 10, 0)
	require.Error(t, err, "MinTrades still enforced when volume check is off")
	assert.ErrorIs(t, err, ErrTooFewTrades)
}

func TestCheckLiquidityNilVolumeWithFloorFails(t *testing.T) {
	err := CheckLiquidity(20, nil, 10, 100)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientVolume)
}
