// Package config loads runtime configuration from the environment. All
// environment-specific values (chain, endpoints, oracle key/address) come from
// here so the same image runs unchanged on testnet and mainnet.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the oracle's runtime settings.
type Config struct {
	// Env is a label for logs/metrics, e.g. "testnet" or "mainnet".
	// Environment variable: ORACLE_ENV. Default "unknown".
	Env string
	// LogLevel is one of debug|info|warn|error.
	// Environment variable: LOG_LEVEL. Default "info".
	LogLevel string

	// PriceBaseURL overrides the Figure Markets trades endpoint (optional).
	// Environment variable: PRICE_BASE_URL.
	PriceBaseURL string

	// GRPCEndpoint is the Provenance node gRPC address (host:port).
	// Environment variable: GRPC_ENDPOINT.
	GRPCEndpoint string
	// GRPCInsecure, when true, uses plaintext transport for the Provenance
	// node gRPC connection. Only appropriate for in-cluster or localhost
	// endpoints on a trusted network. Default is false (use TLS) if not set.
	// Environment variable: GRPC_INSECURE.
	GRPCInsecure bool
	// ChainID is the target chain id.
	// Environment variable: CHAIN_ID.
	ChainID string
	// OracleAddress is the bech32 address of the signing oracle key; it must be
	// registered in the module's oracle_addresses.
	// Environment variable: ORACLE_ADDRESS.
	OracleAddress string

	// Hex-encoded secp256k1 private key for signing update transactions; must derive to OracleAddress.
	// Environment variable: PRIVATE_KEY_HEX.
	PrivateKeyHex string

	// GasAdjustment multiplies the simulated gas from CalculateTxFees.
	// Environment variable: GAS_ADJUSTMENT. Default 1.5.
	GasAdjustment float32

	// DryRun, when true, computes and logs the factor but never broadcasts.
	// Environment variable: DRY_RUN. Default false.
	DryRun bool

	// HTTPTimeout bounds outbound price requests.
	// Environment variable: HTTP_TIMEOUT. Default 15s.
	HTTPTimeout time.Duration

	// Unordered, if true, submits updates as unordered transactions without using account sequence numbers.
	// If false, submits updates as regular transactions by looking up the account's sequence first.
	// Environment variable: UNORDERED. Default true.
	Unordered bool

	// UnorderedTimeout sets the timeout for unordered transactions and must be less than 5 minutes.
	// Default is 2 minutes (2m) if not set. Ignored if Unordered is false.
	// Environment variable: UNORDERED_TIMEOUT.
	UnorderedTimeout time.Duration

	// AccountNumber, if non-zero, is used when signing unordered txs instead of
	// querying the chain each run. The account number is immutable, and a real
	// oracle account is never number 0, so zero means "not set — look it up".
	// Environment variable: ACCOUNT_NUMBER. Default 0.
	AccountNumber uint64

	// MaxPriceMoveRatio is the maximum multiplicative move (in either
	// direction) allowed between the computed price and the price implied by
	// the on-chain factor. If the new price is > N× or < 1/N× the on-chain
	// price, the update is refused. Values <= 1 disable the check.
	// Environment variable: MAX_PRICE_MOVE_RATIO. Default 10.
	MaxPriceMoveRatio float32

	// MinTrades is the minimum number of trades that must appear in the price
	// window before the oracle is willing to submit an update.
	// Environment variable: MIN_TRADES. Default 10.
	MinTrades int

	// MinVolumeHASH is the minimum total HASH volume required in the price
	// window before submitting. Expressed in whole HASH (not nhash).
	// Environment variable: MIN_VOLUME_HASH. Default 100.
	MinVolumeHASH float64

	// ForceUpdate, when true, bypasses the movement and liquidity guards for
	// this run. Intended as an operator escape hatch when the guards would
	// otherwise persistently block a legitimate move (e.g. after a real
	// large price swing). Never leave this set as a steady-state default.
	// Environment variable: FORCE_UPDATE. Default false.
	ForceUpdate bool
}

// Load reads configuration from environment variables, applying defaults and
// validating required fields.
func Load() (Config, error) {
	et := &errorTracker{}
	c := Config{
		Env:               getEnv("ORACLE_ENV", "unknown"),
		LogLevel:          strings.ToLower(getEnv("LOG_LEVEL", "info")),
		PriceBaseURL:      os.Getenv("PRICE_BASE_URL"),
		GRPCEndpoint:      os.Getenv("GRPC_ENDPOINT"),
		GRPCInsecure:      getBool("GRPC_INSECURE", false, et),
		ChainID:           os.Getenv("CHAIN_ID"),
		OracleAddress:     os.Getenv("ORACLE_ADDRESS"),
		PrivateKeyHex:     os.Getenv("PRIVATE_KEY_HEX"),
		GasAdjustment:     getFloat32("GAS_ADJUSTMENT", 1.5, et),
		DryRun:            getBool("DRY_RUN", false, et),
		HTTPTimeout:       getDuration("HTTP_TIMEOUT", 15*time.Second, et),
		Unordered:         getBool("UNORDERED", true, et),
		UnorderedTimeout:  getDuration("UNORDERED_TIMEOUT", 2*time.Minute, et),
		AccountNumber:     getUint64("ACCOUNT_NUMBER", 0, et),
		MaxPriceMoveRatio: getFloat32("MAX_PRICE_MOVE_RATIO", 10, et),
		MinTrades:         getInt("MIN_TRADES", 10, et),
		MinVolumeHASH:     getFloat64("MIN_VOLUME_HASH", 100, et),
		ForceUpdate:       getBool("FORCE_UPDATE", false, et),
	}

	if et.HasError() {
		return Config{}, et.GetError()
	}

	// In non-dry-run mode the chain settings are required.
	if !c.DryRun {
		var missing []string
		if c.GRPCEndpoint == "" {
			missing = append(missing, "GRPC_ENDPOINT")
		}
		if c.ChainID == "" {
			missing = append(missing, "CHAIN_ID")
		}
		if c.OracleAddress == "" {
			missing = append(missing, "ORACLE_ADDRESS")
		}
		if c.PrivateKeyHex == "" {
			missing = append(missing, "PRIVATE_KEY_HEX")
		}
		if len(missing) > 0 {
			return Config{}, fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
		}
		if c.Unordered && c.UnorderedTimeout > 5*time.Minute {
			return Config{}, fmt.Errorf("UNORDERED_TIMEOUT %s must be at most the chain max of 5m", c.UnorderedTimeout)
		}
	}

	return c, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// An errorTracker holds a list of errors.
type errorTracker struct {
	Errors []error
}

// Append adds an error to this errorTracker.
func (e *errorTracker) Append(err error) {
	e.Errors = append(e.Errors, err)
}

// GetError returns a single error representing all the errors in this errorTracker.
// Returns nil, if there aren't any errors in this errorTracker.
func (e *errorTracker) GetError() error {
	return errors.Join(e.Errors...)
}

// HasError returns true if this errorTracker contains an error, false if empty.
func (e *errorTracker) HasError() bool {
	return len(e.Errors) > 0
}

// getBool looks up the env var with the provided key and converts it to a bool.
// Returns def if the env var isn't set or if there's a problem parsing it.
// If there's a problem parsing it, an error is added to the provided errorTracker.
func getBool(key string, def bool, et *errorTracker) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	rv, err := strconv.ParseBool(v)
	if err != nil {
		et.Append(fmt.Errorf("invalid %s bool %q: %w", key, v, err))
		return def
	}
	return rv
}

// getFloat32 looks up the env var with the provided key and converts it to a float32.
// Returns def if the env var isn't set or if there's a problem parsing it.
// If there's a problem parsing it, an error is added to the provided errorTracker.
func getFloat32(key string, def float32, et *errorTracker) float32 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	rv, err := strconv.ParseFloat(v, 32)
	if err != nil {
		et.Append(fmt.Errorf("invalid %s float %q: %w", key, v, err))
		return def
	}
	return float32(rv)
}

// getDuration looks up the env var with the provided key and converts it to a time.Duration.
// Returns def if the env var isn't set or if there's a problem parsing it.
// If there's a problem parsing it, an error is added to the provided errorTracker.
func getDuration(key string, def time.Duration, et *errorTracker) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	rv, err := time.ParseDuration(v)
	if err != nil {
		et.Append(fmt.Errorf("invalid %s duration %q: %w", key, v, err))
		return def
	}
	return rv
}

// getUint64 looks up the env var with the provided key and converts it to a uint64.
// Returns def if the env var isn't set or if there's a problem parsing it.
// If there's a problem parsing it, an error is added to the provided errorTracker.
func getUint64(key string, def uint64, et *errorTracker) uint64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	rv, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		et.Append(fmt.Errorf("invalid %s uint64 %q: %w", key, v, err))
		return def
	}
	return rv
}

// getInt looks up the env var with the provided key and converts it to an int.
// Returns def if the env var isn't set or if there's a problem parsing it.
// If there's a problem parsing it, an error is added to the provided errorTracker.
func getInt(key string, def int, et *errorTracker) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	rv, err := strconv.Atoi(v)
	if err != nil {
		et.Append(fmt.Errorf("invalid %s int %q: %w", key, v, err))
		return def
	}
	return rv
}

// getFloat64 looks up the env var with the provided key and converts it to a float64.
// Returns def if the env var isn't set or if there's a problem parsing it.
// If there's a problem parsing it, an error is added to the provided errorTracker.
func getFloat64(key string, def float64, et *errorTracker) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	rv, err := strconv.ParseFloat(v, 64)
	if err != nil {
		et.Append(fmt.Errorf("invalid %s float %q: %w", key, v, err))
		return def
	}
	return rv
}
