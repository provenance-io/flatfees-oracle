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
	// KeyringDir / KeyName / mnemonic source are intentionally left to the
	// broadcast wiring; see internal/chain. The signing secret must be mounted,
	// never baked into the image.

	// GasAdjustment multiplies the simulated gas from CalculateTxFees.
	GasAdjustment float32

	// DryRun, when true, computes and logs the factor but never broadcasts.
	DryRun bool

	// HTTPTimeout bounds outbound price requests.
	HTTPTimeout time.Duration
}

// Load reads configuration from environment variables, applying defaults and
// validating required fields.
func Load() (Config, error) {
	c := Config{
		Env:           getEnv("ORACLE_ENV", "unknown"),
		LogLevel:      strings.ToLower(getEnv("LOG_LEVEL", "info")),
		PriceBaseURL:  os.Getenv("PRICE_BASE_URL"),
		GRPCEndpoint:  os.Getenv("GRPC_ENDPOINT"),
		ChainID:       os.Getenv("CHAIN_ID"),
		OracleAddress: os.Getenv("ORACLE_ADDRESS"),
		GasAdjustment: 1.5,
		DryRun:        getBool("DRY_RUN", false),
		HTTPTimeout:   15 * time.Second,
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
		if len(missing) > 0 {
			return Config{}, fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
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
