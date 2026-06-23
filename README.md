# flatfees-oracle

Cronjob that updates the conversion factor in the Provenance `x/flatfees` module
to match the current price of HASH.

It runs once per invocation: fetch the HASH price, compute the conversion factor,
and — unless `DRY_RUN` — submit a `MsgUpdateConversionFactorRequest` if the factor
changed. Designed to run as a Kubernetes CronJob (daily), one instance per network
(testnet and mainnet) sharing the same image with different config/secrets.

## How it works

1. **Price** — `internal/price` fetches HASH-USD trades from the internal Figure
   Markets API over a trailing 7-day window (ending midnight Eastern) and computes
   a volume-weighted average price (VWAP).
2. **Convert** — `internal/convert` maps the price to the module's
   `ConversionFactor`. `converted_amount` (nhash) is a price-tiered scale and
   `definition_amount` (musd) is computed so the factor tracks the live rate:

   | HASH price (USD/HASH)      | `converted_amount` (nhash) | `definition_amount` (musd) |
   |----------------------------|----------------------------|----------------------------|
   | ≥ $0.01                    | 1e9                        | 1000 × P                   |
   | ≥ $0.00001 and < $0.01     | 1e12                       | 1,000,000 × P              |
   | < $0.00001                 | 1e15                       | 1,000,000,000 × P          |

   Scaling: `1 musd = $0.001`, `1 HASH = 1e9 nhash`. `definition_amount` is
   rounded to the nearest integer musd.
3. **Chain** — `internal/chain` reads current params (`Params` query) for the
   skip-if-unchanged check and oracle authorization, maps the factor to
   `x/flatfees/types`, estimates gas/fees (`CalculateTxFees`), and builds the
   update message. The signer must be a registered oracle address.

## Layout

```
cmd/oracle/         entrypoint (run once, exit 0/non-zero)
internal/price/     Figure Markets fetch + VWAP
internal/convert/   tiered conversion-factor math (pure, fully tested)
internal/chain/     x/flatfees query + msg construction (real SDK types)
internal/config/    env-based configuration
internal/logging/   structured JSON logging (slog)
```

## Configuration (environment)

| Var | Required | Default | Notes |
|-----|----------|---------|-------|
| `ORACLE_ENV` | no | `unknown` | label for logs, e.g. `testnet`/`mainnet` |
| `LOG_LEVEL` | no | `info` | `debug|info|warn|error` |
| `GRPC_ENDPOINT` | yes¹ | – | Provenance node gRPC `host:port` |
| `CHAIN_ID` | yes¹ | – | target chain id |
| `ORACLE_ADDRESS` | yes¹ | – | bech32 signer; must be in `oracle_addresses` |
| `GAS_ADJUSTMENT` | no | `1.2` | multiplier on simulated gas |
| `PRICE_BASE_URL` | no | Figure Markets URL | override price endpoint |
| `HTTP_TIMEOUT` | no | `15s` | price request timeout |
| `DRY_RUN` | no | `false` | compute + log, never broadcast |

¹ Required only when `DRY_RUN` is false. The signing key itself is mounted as a
secret and consumed by the broadcast wiring (see below) — never bake it into the
image.

## Develop

```
make test     # go test ./... -race
make vet
make build
make run      # local dry run (DRY_RUN=true)
make docker   # build the image
```

## Build notes / dependencies

This module imports `github.com/provenance-io/provenance` for the `x/flatfees`
types. Provenance pins cosmos-sdk to a fork via `replace`, so **go.mod mirrors
provenance's `replace` directives** — these must stay in sync with the pinned
provenance version. After changing any dependency, run `go mod tidy` and commit
`go.sum`.

## Status / TODO

- **Broadcast** (`cmd/oracle`): the tx **sign + broadcast + confirm** step is
  marked `TODO` — it needs the team's standard cosmos-sdk `client.Context` +
  keyring setup. Read/compute/estimate paths and message construction are done.
