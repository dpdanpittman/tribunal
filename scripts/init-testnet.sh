#!/usr/bin/env bash
# Convenience wrapper that points at a locally-running XION devnet and
# verifies it's healthy. Use this before deploy-contract.sh.
#
# The Burnt XION devnet ships as a docker-compose stack; this script
# does NOT manage it. It just probes the well-known endpoints.

set -euo pipefail

RPC="${RPC:-http://localhost:26657}"
REST="${REST:-http://localhost:1317}"

echo "==> probing RPC at $RPC"
HEIGHT="$(curl -sf "$RPC/status" | sed -n 's/.*"latest_block_height":"\([0-9]*\)".*/\1/p' | head -n1)"
if [[ -z "$HEIGHT" ]]; then
  echo "RPC not responding at $RPC — start the devnet first" >&2
  exit 1
fi
echo "RPC ok, height=$HEIGHT"

echo "==> probing LCD at $REST"
NETWORK="$(curl -sf "$REST/cosmos/base/tendermint/v1beta1/node_info" | sed -n 's/.*"network":"\([^"]*\)".*/\1/p' | head -n1)"
if [[ -z "$NETWORK" ]]; then
  echo "LCD not responding at $REST" >&2
  exit 1
fi
echo "LCD ok, network=$NETWORK"

echo
echo "==> exports for the deploy script"
echo "export CHAIN_ID=\"$NETWORK\""
echo "export NODE=\"$(echo "$RPC" | sed 's|^http://|tcp://|')\""
echo "export KEY=\"<xiond keyring entry name>\""
echo "# If xiond is only inside containers (no host binary), set:"
echo "# export XIOND=\"docker exec devnet-xion-1 xiond\""
