package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadEnvVars is the full set of environment variables Load reads. Each test
// clears them before applying its own overrides so the ambient process
// environment cannot leak into the result.
var loadEnvVars = []string{
	"ORACLE_ENV",
	"LOG_LEVEL",
	"PRICE_BASE_URL",
	"GRPC_ENDPOINT",
	"GRPC_INSECURE",
	"CHAIN_ID",
	"ORACLE_ADDRESS",
	"PRIVATE_KEY_HEX",
	"GAS_ADJUSTMENT",
	"DRY_RUN",
	"HTTP_TIMEOUT",
	"UNORDERED",
	"UNORDERED_TIMEOUT",
	"ACCOUNT_NUMBER",
	"MAX_PRICE_MOVE_RATIO",
	"MIN_TRADES",
	"MIN_VOLUME_HASH",
	"FORCE_UPDATE",
}

// setLoadEnv clears every env var Load reads, then applies the given overrides.
// Uses t.Setenv so values are restored when the test ends.
func setLoadEnv(t *testing.T, envs map[string]string) {
	t.Helper()
	for _, k := range loadEnvVars {
		t.Setenv(k, "")
	}
	for k, v := range envs {
		t.Setenv(k, v)
	}
}

func TestLoad(t *testing.T) {
	// chainEnvs returns a base set of env vars that satisfy the non-dry-run
	// required-config check, with any extras merged on top.
	chainEnvs := func(extra map[string]string) map[string]string {
		envs := map[string]string{
			"GRPC_ENDPOINT":   "grpc.example:9090",
			"CHAIN_ID":        "pio-mainnet-1",
			"ORACLE_ADDRESS":  "pb1oracle",
			"PRIVATE_KEY_HEX": "deadbeef",
		}
		for k, v := range extra {
			envs[k] = v
		}
		return envs
	}

	cases := []struct {
		name     string
		envs     map[string]string
		want     Config
		wantErrs []string // if non-empty, each substring must appear in the returned error
	}{
		{
			name: "defaults in dry run",
			envs: map[string]string{"DRY_RUN": "true"},
			want: Config{
				Env:               "unknown",
				LogLevel:          "info",
				GasAdjustment:     1.5,
				DryRun:            true,
				HTTPTimeout:       15 * time.Second,
				Unordered:         true,
				UnorderedTimeout:  2 * time.Minute,
				MaxPriceMoveRatio: 10,
				MinTrades:         10,
				MinVolumeHASH:     100,
			},
		},
		{
			name: "all fields set in dry run",
			envs: map[string]string{
				"ORACLE_ENV":           "testnet",
				"LOG_LEVEL":            "DEBUG",
				"PRICE_BASE_URL":       "https://prices.example/trades",
				"GRPC_ENDPOINT":        "grpc.example:9090",
				"GRPC_INSECURE":        "true",
				"CHAIN_ID":             "pio-testnet-1",
				"ORACLE_ADDRESS":       "pb1oracle",
				"PRIVATE_KEY_HEX":      "deadbeef",
				"GAS_ADJUSTMENT":       "2.25",
				"DRY_RUN":              "true",
				"HTTP_TIMEOUT":         "30s",
				"UNORDERED":            "false",
				"UNORDERED_TIMEOUT":    "1m30s",
				"ACCOUNT_NUMBER":       "42",
				"MAX_PRICE_MOVE_RATIO": "5",
				"MIN_TRADES":           "25",
				"MIN_VOLUME_HASH":      "1000",
				"FORCE_UPDATE":         "true",
			},
			want: Config{
				Env:               "testnet",
				LogLevel:          "debug",
				PriceBaseURL:      "https://prices.example/trades",
				GRPCEndpoint:      "grpc.example:9090",
				GRPCInsecure:      true,
				ChainID:           "pio-testnet-1",
				OracleAddress:     "pb1oracle",
				PrivateKeyHex:     "deadbeef",
				GasAdjustment:     2.25,
				DryRun:            true,
				HTTPTimeout:       30 * time.Second,
				Unordered:         false,
				UnorderedTimeout:  90 * time.Second,
				AccountNumber:     42,
				MaxPriceMoveRatio: 5,
				MinTrades:         25,
				MinVolumeHASH:     1000,
				ForceUpdate:       true,
			},
		},
		{
			name: "required fields present non-dry-run",
			envs: chainEnvs(map[string]string{
				"UNORDERED":         "true",
				"UNORDERED_TIMEOUT": "4m",
			}),
			want: Config{
				Env:               "unknown",
				LogLevel:          "info",
				GRPCEndpoint:      "grpc.example:9090",
				ChainID:           "pio-mainnet-1",
				OracleAddress:     "pb1oracle",
				PrivateKeyHex:     "deadbeef",
				GasAdjustment:     1.5,
				HTTPTimeout:       15 * time.Second,
				Unordered:         true,
				UnorderedTimeout:  4 * time.Minute,
				MaxPriceMoveRatio: 10,
				MinTrades:         10,
				MinVolumeHASH:     100,
			},
		},
		{
			name:     "missing all required fields non-dry-run",
			envs:     map[string]string{},
			wantErrs: []string{"missing required config: GRPC_ENDPOINT, CHAIN_ID, ORACLE_ADDRESS, PRIVATE_KEY_HEX"},
		},
		{
			name: "missing GRPC_ENDPOINT non-dry-run",
			envs: map[string]string{
				"CHAIN_ID":        "pio-mainnet-1",
				"ORACLE_ADDRESS":  "pb1oracle",
				"PRIVATE_KEY_HEX": "deadbeef",
			},
			wantErrs: []string{"missing required config: GRPC_ENDPOINT"},
		},
		{
			name: "missing CHAIN_ID non-dry-run",
			envs: map[string]string{
				"GRPC_ENDPOINT":   "grpc.example:9090",
				"ORACLE_ADDRESS":  "pb1oracle",
				"PRIVATE_KEY_HEX": "deadbeef",
			},
			wantErrs: []string{"missing required config: CHAIN_ID"},
		},
		{
			name: "missing ORACLE_ADDRESS non-dry-run",
			envs: map[string]string{
				"GRPC_ENDPOINT":   "grpc.example:9090",
				"CHAIN_ID":        "pio-mainnet-1",
				"PRIVATE_KEY_HEX": "deadbeef",
			},
			wantErrs: []string{"missing required config: ORACLE_ADDRESS"},
		},
		{
			name: "missing PRIVATE_KEY_HEX non-dry-run",
			envs: map[string]string{
				"GRPC_ENDPOINT":  "grpc.example:9090",
				"CHAIN_ID":       "pio-mainnet-1",
				"ORACLE_ADDRESS": "pb1oracle",
			},
			wantErrs: []string{"missing required config: PRIVATE_KEY_HEX"},
		},
		{
			name: "unordered timeout at 5m1s rejected",
			envs: chainEnvs(map[string]string{
				"UNORDERED":         "true",
				"UNORDERED_TIMEOUT": "5m1s",
			}),
			wantErrs: []string{"UNORDERED_TIMEOUT 5m1s must be at most the chain max of 5m"},
		},
		{
			name: "unordered timeout over 5m rejected",
			envs: chainEnvs(map[string]string{
				"UNORDERED":         "true",
				"UNORDERED_TIMEOUT": "10m",
			}),
			wantErrs: []string{"UNORDERED_TIMEOUT 10m0s must be at most the chain max of 5m"},
		},
		{
			name: "unordered timeout exactly 5m accepted",
			envs: chainEnvs(map[string]string{
				"UNORDERED":         "true",
				"UNORDERED_TIMEOUT": "5m",
			}),
			want: Config{
				Env:               "unknown",
				LogLevel:          "info",
				GRPCEndpoint:      "grpc.example:9090",
				ChainID:           "pio-mainnet-1",
				OracleAddress:     "pb1oracle",
				PrivateKeyHex:     "deadbeef",
				GasAdjustment:     1.5,
				HTTPTimeout:       15 * time.Second,
				Unordered:         true,
				UnorderedTimeout:  5 * time.Minute,
				MaxPriceMoveRatio: 10,
				MinTrades:         10,
				MinVolumeHASH:     100,
			},
		},
		{
			name: "unordered timeout just under 5m accepted",
			envs: chainEnvs(map[string]string{
				"UNORDERED":         "true",
				"UNORDERED_TIMEOUT": "4m59s",
			}),
			want: Config{
				Env:               "unknown",
				LogLevel:          "info",
				GRPCEndpoint:      "grpc.example:9090",
				ChainID:           "pio-mainnet-1",
				OracleAddress:     "pb1oracle",
				PrivateKeyHex:     "deadbeef",
				GasAdjustment:     1.5,
				HTTPTimeout:       15 * time.Second,
				Unordered:         true,
				UnorderedTimeout:  4*time.Minute + 59*time.Second,
				MaxPriceMoveRatio: 10,
				MinTrades:         10,
				MinVolumeHASH:     100,
			},
		},
		{
			name: "large unordered timeout allowed when unordered false",
			envs: chainEnvs(map[string]string{
				"UNORDERED":         "false",
				"UNORDERED_TIMEOUT": "10m",
			}),
			want: Config{
				Env:               "unknown",
				LogLevel:          "info",
				GRPCEndpoint:      "grpc.example:9090",
				ChainID:           "pio-mainnet-1",
				OracleAddress:     "pb1oracle",
				PrivateKeyHex:     "deadbeef",
				GasAdjustment:     1.5,
				HTTPTimeout:       15 * time.Second,
				Unordered:         false,
				UnorderedTimeout:  10 * time.Minute,
				MaxPriceMoveRatio: 10,
				MinTrades:         10,
				MinVolumeHASH:     100,
			},
		},
		{
			name: "large unordered timeout allowed in dry run",
			envs: map[string]string{
				"DRY_RUN":           "true",
				"UNORDERED":         "true",
				"UNORDERED_TIMEOUT": "10m",
			},
			want: Config{
				Env:               "unknown",
				LogLevel:          "info",
				GasAdjustment:     1.5,
				DryRun:            true,
				HTTPTimeout:       15 * time.Second,
				Unordered:         true,
				UnorderedTimeout:  10 * time.Minute,
				MaxPriceMoveRatio: 10,
				MinTrades:         10,
				MinVolumeHASH:     100,
			},
		},
		{
			name:     "invalid GAS_ADJUSTMENT",
			envs:     map[string]string{"GAS_ADJUSTMENT": "abc"},
			wantErrs: []string{`invalid GAS_ADJUSTMENT float "abc"`},
		},
		{
			name:     "invalid DRY_RUN",
			envs:     map[string]string{"DRY_RUN": "notabool"},
			wantErrs: []string{`invalid DRY_RUN bool "notabool"`},
		},
		{
			name:     "invalid GRPC_INSECURE",
			envs:     map[string]string{"GRPC_INSECURE": "notabool"},
			wantErrs: []string{`invalid GRPC_INSECURE bool "notabool"`},
		},
		{
			name:     "invalid HTTP_TIMEOUT",
			envs:     map[string]string{"HTTP_TIMEOUT": "notaduration"},
			wantErrs: []string{`invalid HTTP_TIMEOUT duration "notaduration"`},
		},
		{
			name:     "invalid UNORDERED",
			envs:     map[string]string{"UNORDERED": "notabool"},
			wantErrs: []string{`invalid UNORDERED bool "notabool"`},
		},
		{
			name:     "invalid UNORDERED_TIMEOUT",
			envs:     map[string]string{"UNORDERED_TIMEOUT": "notaduration"},
			wantErrs: []string{`invalid UNORDERED_TIMEOUT duration "notaduration"`},
		},
		{
			name:     "invalid ACCOUNT_NUMBER",
			envs:     map[string]string{"ACCOUNT_NUMBER": "-1"},
			wantErrs: []string{`invalid ACCOUNT_NUMBER uint64 "-1"`},
		},
		{
			name:     "invalid MAX_PRICE_MOVE_RATIO",
			envs:     map[string]string{"MAX_PRICE_MOVE_RATIO": "abc"},
			wantErrs: []string{`invalid MAX_PRICE_MOVE_RATIO float "abc"`},
		},
		{
			name:     "invalid MIN_TRADES",
			envs:     map[string]string{"MIN_TRADES": "many"},
			wantErrs: []string{`invalid MIN_TRADES int "many"`},
		},
		{
			name:     "invalid MIN_VOLUME_HASH",
			envs:     map[string]string{"MIN_VOLUME_HASH": "lots"},
			wantErrs: []string{`invalid MIN_VOLUME_HASH float "lots"`},
		},
		{
			name:     "invalid FORCE_UPDATE",
			envs:     map[string]string{"FORCE_UPDATE": "notabool"},
			wantErrs: []string{`invalid FORCE_UPDATE bool "notabool"`},
		},
		{
			name: "multiple parse errors joined",
			envs: map[string]string{
				"GAS_ADJUSTMENT": "abc",
				"HTTP_TIMEOUT":   "xyz",
				"ACCOUNT_NUMBER": "-1",
			},
			wantErrs: []string{
				`invalid GAS_ADJUSTMENT float "abc"`,
				`invalid HTTP_TIMEOUT duration "xyz"`,
				`invalid ACCOUNT_NUMBER uint64 "-1"`,
			},
		},
		{
			name: "parse error takes precedence over missing required",
			envs: map[string]string{"GAS_ADJUSTMENT": "abc"},
			wantErrs: []string{`invalid GAS_ADJUSTMENT float "abc"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setLoadEnv(t, tc.envs)
			got, err := Load()
			if len(tc.wantErrs) > 0 {
				require.Error(t, err, "Load()")
				errStr := err.Error();
				for _, sub := range tc.wantErrs {
					assert.Contains(t, errStr, sub, "Load() error")
				}
				assert.Equal(t, Config{}, got, "Load() config on error")
			} else {
				require.NoError(t, err, "Load() error")
				assert.Equal(t, tc.want, got, "Load() config")
			}
		})
	}
}
