#!/usr/bin/env bash
#
# End-to-end local test for the flatfees-oracle: spin up a single-node
# Provenance chain, whitelist a local oracle key, run the oracle, and show the
# conversion factor before/after.
#
# Requirements: jq, go, openssl on PATH, and a provenance source checkout.
#
# Usage:
#   PROVENANCE_REPO=/path/to/provenance ./scripts/localnet-test.sh
#   UNORDERED=true PROVENANCE_REPO=/path/to/provenance ./scripts/localnet-test.sh
#
# Optional overrides:
#   PROVENANCED      exact provenanced binary  (default: $PROVENANCE_REPO/build/provenanced)
#   HOME_DIR         node data dir (WIPED!)    (default: /tmp/pio-localnet)
#   GAS_ADJUSTMENT   gas multiplier            (default: 2.0)  # unordered txs need >1.0.
#   UNORDERED        unordered tx (true) vs sequence-based (false)  (default: false)
#
set -euo pipefail

# --- required: path to the provenance source repo -------------------------
PROVENANCE_REPO="${PROVENANCE_REPO:-}"
if [ -z "$PROVENANCE_REPO" ]; then
  echo "PROVENANCE_REPO is required, e.g.:"
  echo "  PROVENANCE_REPO=/path/to/provenance ./scripts/localnet-test.sh"
  exit 1
fi

# --- paths ----------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ORACLE_REPO="$(cd "$SCRIPT_DIR/.." && pwd)"                # this repo (scripts/ is at root)
PROVENANCED="${PROVENANCED:-$PROVENANCE_REPO/build/provenanced}"

HOME_DIR="${HOME_DIR:-/tmp/pio-localnet}"
CHAIN_ID="${CHAIN_ID:-testing-1}"
DENOM="nhash"
KEY_NAME="oracle"
KEYRING="test"
GRPC="localhost:9090"
GAS_ADJUSTMENT="${GAS_ADJUSTMENT:-2.0}"                    # unordered txs need >1.0 headroom
NODE_LOG="/tmp/pio-node.log"
# --------------------------------------------------------------------------

prov() { "$PROVENANCED" -t --home "$HOME_DIR" "$@"; }

# Kill any leftover provenanced node from a previous run (no lsof — it can hang on macOS).
kill_node() { pkill -f 'provenanced.*start' 2>/dev/null || true; }

# --- preflight ------------------------------------------------------------
for t in jq go openssl; do command -v "$t" >/dev/null || { echo "$t is required"; exit 1; }; done
[ -f "$PROVENANCE_REPO/scripts/initialize.sh" ] || { echo "initialize.sh not found in $PROVENANCE_REPO"; exit 1; }

if [ ! -x "$PROVENANCED" ]; then
  echo ">> building provenanced"
  ( cd "$PROVENANCE_REPO" && make build )
fi
[ -x "$PROVENANCED" ] || { echo "provenanced not found at $PROVENANCED"; exit 1; }
echo ">> provenanced: $PROVENANCED"
echo ">> oracle repo: $ORACLE_REPO"

echo ">> clearing any leftover node"; kill_node; sleep 1
echo ">> fresh home: $HOME_DIR"; rm -rf "$HOME_DIR"; mkdir -p "$HOME_DIR"

# --- bootstrap a working single-node chain --------------------------------
echo ">> bootstrapping chain"
PIO_HOME="$HOME_DIR" PROV_CMD="$PROVENANCED" PIO_CHAIN_ID="$CHAIN_ID" \
PIO_TESTNET=true PIO_KEYRING_BACKEND="$KEYRING" DENOM="$DENOM" SHOW_START=false \
  bash "$PROVENANCE_REPO/scripts/initialize.sh"

# --- oracle key: generate hex, import, fund, whitelist --------------------
echo ">> creating oracle key"
PRIV_HEX=$(openssl rand -hex 32)
prov keys import-hex "$KEY_NAME" "$PRIV_HEX" --keyring-backend "$KEYRING" >/dev/null 2>&1
ORACLE_ADDR=$(prov keys show "$KEY_NAME" -a --keyring-backend "$KEYRING")
echo "   oracle address: $ORACLE_ADDR"

echo ">> funding + whitelisting oracle"
prov genesis add-account "$ORACLE_ADDR" "100000000000000$DENOM"
GENESIS="$HOME_DIR/config/genesis.json"
jq --arg a "$ORACLE_ADDR" \
  '.app_state.flatfees.params.oracle_addresses =
     ((.app_state.flatfees.params.oracle_addresses // []) + [$a] | unique)' \
  "$GENESIS" > "$GENESIS.tmp" && mv "$GENESIS.tmp" "$GENESIS"

# enable tx indexing so the oracle's GetTx confirmation works
sed -i.bak 's/^indexer = .*/indexer = "kv"/' "$HOME_DIR/config/config.toml"

# --- start node -----------------------------------------------------------
echo ">> starting node (logs -> $NODE_LOG)"
prov start >"$NODE_LOG" 2>&1 &
NODE_PID=$!
trap 'kill "$NODE_PID" 2>/dev/null || true; kill_node' EXIT

echo ">> waiting for first block"
ready=""
for _ in $(seq 1 40); do
  h=$(prov status 2>/dev/null | jq -r '(.sync_info.latest_block_height // .SyncInfo.latest_block_height // "0")' 2>/dev/null || echo 0)
  [ "${h:-0}" -ge 1 ] 2>/dev/null && { ready=1; break; }
  sleep 1
done
[ -n "$ready" ] || { echo "node did not produce a block; see $NODE_LOG"; tail -n 40 "$NODE_LOG"; exit 1; }

# --- before / run / after -------------------------------------------------
echo ">> conversion factor BEFORE:"
prov query flatfees params -o json | jq '.params.conversion_factor'

echo ">> running the oracle (gas_adjustment=$GAS_ADJUSTMENT)"
set +e
( cd "$ORACLE_REPO" && \
  DRY_RUN=false ORACLE_ENV=testnet \
  GRPC_ENDPOINT="$GRPC" CHAIN_ID="$CHAIN_ID" \
  ORACLE_ADDRESS="$ORACLE_ADDR" PRIVATE_KEY_HEX="$PRIV_HEX" \
  UNORDERED=false GAS_ADJUSTMENT="$GAS_ADJUSTMENT" \
  go run ./cmd/oracle )
ORACLE_RC=$?
set -e
echo "   oracle exit code: $ORACLE_RC"

sleep 3
echo ">> conversion factor AFTER:"
prov query flatfees params -o json | jq '.params.conversion_factor'
echo ">> done (rc=$ORACLE_RC). node log: $NODE_LOG"
exit "$ORACLE_RC"