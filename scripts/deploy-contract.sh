#!/usr/bin/env bash
# Build, upload, and instantiate the tribunal-reputation CosmWasm contract.
#
# Usage:
#   scripts/deploy-contract.sh [--label tribunal-reputation-v1] [--admin <addr>]
#
# Required env (override on the command line if you prefer):
#   CHAIN_ID         e.g. xion-devnet-1 or xion-testnet-2
#   NODE             RPC endpoint, e.g. tcp://localhost:26657
#   KEY              xiond keyring entry name (must hold enough uxion for fees)
#   KEYRING_BACKEND  test|os|file (default: test)
#   XIOND            xiond binary or 'docker exec devnet-xion-1 xiond' style prefix
#                    (default: xiond)
#   GAS_PRICES       fee unit (default: 0.025uxion)
#   GAS_ADJUSTMENT   safety multiplier (default: 1.4)
#
# What it does:
#   1. cosmwasm/optimizer Docker pass (default). Produces a stripped,
#      production-sized wasm in contracts/tribunal-reputation/artifacts/.
#      Pass --skip-optimize to fall back to a raw `cargo build` instead —
#      note that the raw build embeds wasm bulk-memory ops that wasmd
#      v0.54+ rejects, so this only works against chains that allow them.
#   2. (or --skip-build, which uses whatever wasm is already on disk.)
#   3. xiond tx wasm store ... | extract code_id
#   4. xiond tx wasm instantiate ... | extract contract_address
#   5. echo a chain.yaml snippet to stdout so the operator can copy it into
#      ~/.tribunal/chain.yaml or pipe it through `tribunal chain init`.

set -euo pipefail

LABEL="tribunal-reputation-v1"
ADMIN=""
OPTIMIZE=1
SKIP_BUILD=0
INITIAL_BALANCE=100
ROTATION_FLOOR=10
REWARD_MULTIPLIER=2
OPTIMIZER_IMAGE="${OPTIMIZER_IMAGE:-cosmwasm/optimizer:0.17.0}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --label) LABEL="$2"; shift 2 ;;
    --admin) ADMIN="$2"; shift 2 ;;
    --optimize) OPTIMIZE=1; shift ;;
    --skip-optimize) OPTIMIZE=0; shift ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    --initial-balance) INITIAL_BALANCE="$2"; shift 2 ;;
    --rotation-floor) ROTATION_FLOOR="$2"; shift 2 ;;
    --reward-multiplier) REWARD_MULTIPLIER="$2"; shift 2 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

: "${CHAIN_ID:?CHAIN_ID is required (e.g. xion-devnet-1)}"
: "${NODE:?NODE is required (e.g. tcp://localhost:26657)}"
: "${KEY:?KEY is required (xiond keyring entry)}"
KEYRING_BACKEND="${KEYRING_BACKEND:-test}"
XIOND="${XIOND:-xiond}"
GAS_PRICES="${GAS_PRICES:-0.025uxion}"
GAS_ADJUSTMENT="${GAS_ADJUSTMENT:-1.4}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CONTRACT_DIR="$REPO_ROOT/contracts/tribunal-reputation"
WASM_OUT="$CONTRACT_DIR/target/wasm32-unknown-unknown/release/tribunal_reputation.wasm"
OPT_OUT="$CONTRACT_DIR/artifacts/tribunal_reputation.wasm"

cd "$CONTRACT_DIR"

if [[ "$OPTIMIZE" -eq 1 ]]; then
  if [[ "$SKIP_BUILD" -ne 1 ]]; then
    echo "==> $OPTIMIZER_IMAGE pass"
    mkdir -p "$CONTRACT_DIR/artifacts"
    docker run --rm -v "$CONTRACT_DIR":/code \
      --mount type=volume,source=tribunal_reputation_cache,target=/code/target \
      --mount type=volume,source=registry_cache,target=/usr/local/cargo/registry \
      "$OPTIMIZER_IMAGE"
  else
    echo "==> --skip-build: using existing $OPT_OUT"
  fi
  UPLOAD_PATH="$OPT_OUT"
else
  if [[ "$SKIP_BUILD" -ne 1 ]]; then
    echo "==> cargo build --release --target wasm32-unknown-unknown (raw, --skip-optimize)"
    cargo build --release --target wasm32-unknown-unknown
  else
    echo "==> --skip-build: using existing $WASM_OUT"
  fi
  UPLOAD_PATH="$WASM_OUT"
fi

if [[ ! -f "$UPLOAD_PATH" ]]; then
  echo "wasm artifact not found at $UPLOAD_PATH" >&2
  exit 1
fi

echo "==> uploading $UPLOAD_PATH"
STORE_OUT="$($XIOND tx wasm store "$UPLOAD_PATH" \
  --from "$KEY" \
  --chain-id "$CHAIN_ID" \
  --node "$NODE" \
  --keyring-backend "$KEYRING_BACKEND" \
  --gas-prices "$GAS_PRICES" \
  --gas-adjustment "$GAS_ADJUSTMENT" \
  --gas auto \
  --broadcast-mode sync \
  --output json \
  --yes)"
STORE_TX="$(echo "$STORE_OUT" | sed -n 's/.*"txhash":"\([^"]*\)".*/\1/p')"
if [[ -z "$STORE_TX" ]]; then
  echo "could not parse store txhash from: $STORE_OUT" >&2
  exit 1
fi
echo "store txhash: $STORE_TX"

# Wait for the tx to land + grab the code_id from events.
sleep 6
CODE_ID="$($XIOND query tx "$STORE_TX" --node "$NODE" --output json \
  | sed -n 's/.*"key":"code_id","value":"\([0-9]*\)".*/\1/p' | head -n1)"
if [[ -z "$CODE_ID" ]]; then
  echo "could not parse code_id for tx $STORE_TX" >&2
  exit 1
fi
echo "code_id: $CODE_ID"

INIT_MSG=$(cat <<JSON
{
  "admin": $( [[ -n "$ADMIN" ]] && echo "\"$ADMIN\"" || echo "null"),
  "initial_balance": "$INITIAL_BALANCE",
  "rotation_floor": "$ROTATION_FLOOR",
  "outcome_reward_multiplier": "$REWARD_MULTIPLIER"
}
JSON
)

echo "==> instantiating code $CODE_ID with label '$LABEL'"
INIT_OUT="$($XIOND tx wasm instantiate "$CODE_ID" "$INIT_MSG" \
  --label "$LABEL" \
  --no-admin \
  --from "$KEY" \
  --chain-id "$CHAIN_ID" \
  --node "$NODE" \
  --keyring-backend "$KEYRING_BACKEND" \
  --gas-prices "$GAS_PRICES" \
  --gas-adjustment "$GAS_ADJUSTMENT" \
  --gas auto \
  --broadcast-mode sync \
  --output json \
  --yes)"
INIT_TX="$(echo "$INIT_OUT" | sed -n 's/.*"txhash":"\([^"]*\)".*/\1/p')"
echo "instantiate txhash: $INIT_TX"

sleep 6
CONTRACT_ADDR="$($XIOND query wasm list-contract-by-code "$CODE_ID" \
  --node "$NODE" --output json \
  | sed -n 's/.*"contracts":\["\([^"]*\)".*/\1/p' | head -n1)"
if [[ -z "$CONTRACT_ADDR" ]]; then
  echo "could not list contracts for code_id $CODE_ID" >&2
  exit 1
fi

echo
echo "==> deployed"
echo "contract_address: $CONTRACT_ADDR"
echo
echo "Paste this into ~/.tribunal/chain.yaml (or pipe through 'tribunal chain init'):"
echo "---"
cat <<YAML
chain_id: "$CHAIN_ID"
node_rpc: "$NODE"
node_rest: "$(echo "$NODE" | sed 's|^tcp://|http://|; s|:26657$|:1317|')"
contract_address: "$CONTRACT_ADDR"
operator_key_name: "$KEY"
keyring_backend: "$KEYRING_BACKEND"
gas_prices: "$GAS_PRICES"
gas_adjustment: "$GAS_ADJUSTMENT"
xiond_binary: "$XIOND"
outcome_reward_multiplier: $REWARD_MULTIPLIER
YAML
