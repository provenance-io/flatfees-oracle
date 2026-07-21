// Package guard applies sanity checks against the computed price before the
// oracle submits an update. It defends against:
//
//   - runaway prints (single bad trade / wash trade / API bug) via a symmetric
//     movement band around the current on-chain factor;
//   - thin-book windows (too few trades or too little volume) via a liquidity
//     floor.
//
// Both checks are pure functions of the already-computed inputs and produce
// sentinel errors so callers can distinguish "skip today, try tomorrow" from
// a real internal failure.
package guard

import (
	"errors"
	"fmt"
	"math/big"
)

// Sentinel errors — callers use errors.Is to switch on the specific guard.
var (
	// ErrMovementExceeded is returned by CheckMovement when the new price is
	// outside the allowed multiplicative band around the on-chain price.
	ErrMovementExceeded = errors.New("price movement exceeds allowed band")
	// ErrTooFewTrades is returned by CheckLiquidity when Trades < MinTrades.
	ErrTooFewTrades = errors.New("too few trades in window")
	// ErrInsufficientVolume is returned by CheckLiquidity when the total HASH
	// volume in the window is below the configured floor.
	ErrInsufficientVolume = errors.New("insufficient trade volume in window")
)

// CheckMovement enforces a symmetric multiplicative band around onChain.
//
// If newPrice > onChain * maxRatio, or newPrice < onChain / maxRatio, the
// function returns an error wrapping ErrMovementExceeded with human-readable
// context.
//
// Special cases:
//   - onChain == nil signals bootstrap (no on-chain factor yet). Returns nil
//     — the caller decides whether liquidity alone is enough.
//   - maxRatio <= 1 disables the check entirely (any newPrice passes).
//   - newPrice <= 0 is always rejected as invalid input.
func CheckMovement(newPrice, onChain *big.Rat, maxRatio float32) error {
	if newPrice == nil || newPrice.Sign() <= 0 {
		return fmt.Errorf("new price must be > 0, got %v", newPrice)
	}
	if onChain == nil {
		return nil // bootstrap — nothing to compare against
	}
	if maxRatio <= 1 {
		return nil // check disabled
	}
	ratio := ratFromFloat32(maxRatio)

	upper := new(big.Rat).Mul(onChain, ratio) // onChain * maxRatio
	lower := new(big.Rat).Quo(onChain, ratio) // onChain / maxRatio

	if newPrice.Cmp(upper) > 0 {
		return fmt.Errorf("%w: new %s > %.2fx on-chain %s",
			ErrMovementExceeded, newPrice.FloatString(12), maxRatio, onChain.FloatString(12))
	}
	if newPrice.Cmp(lower) < 0 {
		return fmt.Errorf("%w: new %s < on-chain %s / %.2fx",
			ErrMovementExceeded, newPrice.FloatString(12), onChain.FloatString(12), maxRatio)
	}
	return nil
}

// CheckLiquidity enforces a floor on both trade count and total HASH volume.
// Returns nil if both are met, otherwise the appropriate sentinel wrapped
// with context.
func CheckLiquidity(trades int, volume *big.Rat, minTrades int, minVolumeHASH float64) error {
	if trades < minTrades {
		return fmt.Errorf("%w: %d < %d", ErrTooFewTrades, trades, minTrades)
	}
	if minVolumeHASH > 0 {
		if volume == nil {
			return fmt.Errorf("%w: volume unknown, floor %g HASH", ErrInsufficientVolume, minVolumeHASH)
		}
		floor := new(big.Rat).SetFloat64(minVolumeHASH)
		if floor == nil {
			// SetFloat64 returns nil for NaN/Inf; treat as configuration bug.
			return fmt.Errorf("%w: invalid MIN_VOLUME_HASH %g", ErrInsufficientVolume, minVolumeHASH)
		}
		if volume.Cmp(floor) < 0 {
			return fmt.Errorf("%w: %s < %g HASH", ErrInsufficientVolume, volume.FloatString(6), minVolumeHASH)
		}
	}
	return nil
}

// ratFromFloat32 converts a float32 to a *big.Rat via float64. Safe because
// float32 fits into float64 exactly, and *big.Rat.SetFloat64 preserves the
// float bit-pattern (any imprecision is inherent to the float, not the
// conversion).
func ratFromFloat32(f float32) *big.Rat {
	return new(big.Rat).SetFloat64(float64(f))
}
