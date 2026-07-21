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

| Var                    | Required | Default            | Notes                                                                                                |
|------------------------|----------|--------------------|------------------------------------------------------------------------------------------------------|
| `ORACLE_ENV`           | no       | `unknown`          | label for logs, e.g. `testnet`/`mainnet`                                                             |
| `LOG_LEVEL`            | no       | `info`             | debug, info, warn, or error                                                                          |
| `GRPC_ENDPOINT`        | yes¹     | –                  | Provenance node gRPC `host:port`                                                                     |
| `GRPC_INSECURE`        | no       | `false`            | plaintext gRPC transport; only for in-cluster / localhost endpoints on a trusted network             |
| `CHAIN_ID`             | yes¹     | –                  | target chain id                                                                                      |
| `ORACLE_ADDRESS`       | yes¹     | –                  | bech32 signer; must be in x/flatfees params `oracle_addresses` list                                  |
| `PRIVATE_KEY_HEX`      | yes¹     | –                  | hex-encoded secp256k1 private key for signing; must derive to `ORACLE_ADDRESS`                       |
| `GAS_ADJUSTMENT`       | no       | `1.5`              | multiplier on simulated gas                                                                          |
| `UNORDERED`            | no       | `true`             | submit as an unordered tx (timeout-based replay protection) rather than by account sequence          |
| `UNORDERED_TIMEOUT`    | no       | `2m`               | timeout for unordered txs; must be ≤ chain max of `5m`. Ignored when `UNORDERED=false`               |
| `ACCOUNT_NUMBER`       | no       | `0`                | when non-zero, used when signing unordered txs instead of querying the chain. `0` means "look it up" |
| `PRICE_BASE_URL`       | no       | Figure Markets URL | override price endpoint                                                                              |
| `HTTP_TIMEOUT`         | no       | `15s`              | price request timeout                                                                                |
| `MAX_PRICE_MOVE_RATIO` | no       | `10`               | refuse to submit if new price > N× or < 1/N× the on-chain price. `≤ 1` disables the check            |
| `MIN_TRADES`           | no       | `10`               | refuse to submit unless the window has at least this many trades                                     |
| `MIN_VOLUME_HASH`      | no       | `100`              | refuse to submit unless total HASH volume in the window meets this floor. `0` disables the check     |
| `FORCE_UPDATE`         | no       | `false`            | operator escape hatch — bypasses movement and liquidity guards for one run                           |
| `DRY_RUN`              | no       | `false`            | compute + log, never broadcast                                                                       |

¹ Required only when `DRY_RUN` is false. `PRIVATE_KEY_HEX` must be mounted as a
Kubernetes secret — never bake it into the image.

## Deployment

Kubernetes manifests (CronJob schedule, `concurrencyPolicy`,
`activeDeadlineSeconds`, resource limits, secret mount for `PRIVATE_KEY_HEX`)
live in the Argo repo:
[provenance-io/argo-manifests / apps/flatfees-oracle](https://github.com/provenance-io/argo-manifests/tree/main/apps/flatfees-oracle).

Testnet and mainnet share this image; per-environment values live alongside
the manifest in that repo.

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
