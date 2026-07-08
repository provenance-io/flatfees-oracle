// Package config loads runtime configuration from the environment. All
// environment-specific values (chain, endpoints, oracle key/address) come from
// here so the same image runs unchanged on testnet and mainnet.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the oracle's runtime settings.
type Config struct {
	// Env is a label for logs/metrics, e.g. "testnet" or "mainnet".
	Env string
	// LogLevel is one of debug|info|warn|error.
	LogLevel string

	// PriceBaseURL overrides the Figure Markets trades endpoint (optional).
	PriceBaseURL string

	// GRPCEndpoint is the Provenance node gRPC address (host:port).
	GRPCEndpoint string
	// ChainID is the target chain id.
	ChainID string
	// OracleAddress is the bech32 address of the signing oracle key; it must be
	// registered in the module's oracle_addresses.
	OracleAddress string

	// Hex-encoded secp256k1 private key for signing update transactions; must derive to OracleAddress.
	PrivateKeyHex string

	// GasAdjustment multiplies the simulated gas from CalculateTxFees.
	GasAdjustment float32

	// DryRun, when true, computes and logs the factor but never broadcasts.
	DryRun bool

	// AccountNumber, if non-zero, is used when signing unordered txs instead of
	// querying the chain each run. The account number is immutable, and a real
	// oracle account is never number 0, so zero means "not set — look it up".
	AccountNumber uint64

	// HTTPTimeout bounds outbound price requests.
	HTTPTimeout time.Duration

	// Unordered submits updates as unordered transactions without using account sequence numbers.
	Unordered bool

	// UnorderedTimeout sets the timeout for unordered transactions and must be less than 5 minutes.
	UnorderedTimeout time.Duration
}

// Load reads configuration from environment variables, applying defaults and
// validating required fields.
func Load() (Config, error) {
	c := Config{
		Env:              getEnv("ORACLE_ENV", "unknown"),
		LogLevel:         strings.ToLower(getEnv("LOG_LEVEL", "info")),
		PriceBaseURL:     os.Getenv("PRICE_BASE_URL"),
		GRPCEndpoint:     os.Getenv("GRPC_ENDPOINT"),
		ChainID:          os.Getenv("CHAIN_ID"),
		OracleAddress:    os.Getenv("ORACLE_ADDRESS"),
		PrivateKeyHex:    os.Getenv("PRIVATE_KEY_HEX"),
		GasAdjustment:    1.5,
		DryRun:           getBool("DRY_RUN", false),
		HTTPTimeout:      15 * time.Second,
		Unordered:        getBool("UNORDERED", true),
		UnorderedTimeout: 2 * time.Minute,
	}

	if v := os.Getenv("GAS_ADJUSTMENT"); v != "" {
		f, err := strconv.ParseFloat(v, 32)
		if err != nil {
			return Config{}, fmt.Errorf("invalid GAS_ADJUSTMENT %q: %w", v, err)
		}
		c.GasAdjustment = float32(f)
	}
	if v := os.Getenv("HTTP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid HTTP_TIMEOUT %q: %w", v, err)
		}
		c.HTTPTimeout = d
	}
	if v := os.Getenv("ACCOUNT_NUMBER"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("invalid ACCOUNT_NUMBER %q: %w", v, err)
		}
		c.AccountNumber = n
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
		if c.Unordered && c.UnorderedTimeout >= 5*time.Minute {
			return Config{}, fmt.Errorf("UNORDERED_TIMEOUT %s must be under the chain max of 5m", c.UnorderedTimeout)
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

func getBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
