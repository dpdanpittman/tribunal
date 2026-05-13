# Tribunal On-Chain Protocol (v0.3)

The on-chain layer is a single CosmWasm contract on Burnt XION
(`contracts/tribunal-reputation/`) plus a Go client
(`internal/chain/`) that batches local ledger entries into contract
calls. Together they implement the **soulbound reputation** primitive
described in `docs/methodology.md`.

This document covers what's actually on-chain, how the wire format
works, and how the hybrid settlement flow behaves.

## What's on-chain (and what isn't)

**On-chain**:

- The set of registered agents (pubkey, label, role, model id, balance,
  TP/FP counts, retirement marker).
- Per-finding state: which agent committed it, severity, stake, claim
  hash, resolution (if any).
- Contract config: admin, initial balance, rotation floor, outcome
  reward multiplier.

**Off-chain** (kept in `.tribunal/`):

- Full finding text and evidence (`.tribunal/findings/<id>.md`,
  `.tribunal/resolutions/<id>.md`).
- Local JSONL ledger of signed Findings + Resolutions
  (`.tribunal/ledger.jsonl`).
- The agent's private key (`~/.tribunal/agents/<label>.key`, 0600).

The contract stores **hashes and deltas**, never finding content. This
keeps gas costs predictable and avoids leaking sensitive review text on
a public chain.

## Contract surface

### Instantiate

```jsonc
{
  "admin": null, // optional; defaults to sender
  "initial_balance": "100", // Uint128 as decimal string
  "rotation_floor": "10", // balance new agent starts with after rotation
  "outcome_reward_multiplier": "2", // TP reward = stake * this
}
```

### Execute messages

All variants are single-key snake_case JSON objects (cosmwasm-std's
default serde shape).

#### `register_agent`

```jsonc
{
  "register_agent": {
    "pubkey": "<base64 of 32-byte ed25519 pubkey>",
    "label": "claude-adversary",
    "model_id": "claude-opus-4-7",
    "role": "adversary",
    "initial_balance": "100", // optional; omitted = use contract default
  },
}
```

Errors: `AgentAlreadyRegistered`, `LabelAlreadyTaken`, `InvalidRole`,
`InvalidPubkeyLength`, `InvalidInitialBalance`.

#### `commit_finding` (real-time path)

```jsonc
{
  "commit_finding": {
    "plan_id": "P-42",
    "finding_id": "F-001",
    "agent_pubkey": "<base64 pubkey>",
    "severity": "critical",
    "claim_hash": "sha256:cafebabe...",
    "stake": "8",
    "signature": "<base64 of 64-byte ed25519 sig over canonical message>",
  },
}
```

#### `commit_finding_batch` (plan-close path)

```jsonc
{
  "commit_finding_batch": {
    "plan_id": "P-42",
    "findings": [
      /* array of the same FindingCommit shape, sans wrapper key */
    ],
  },
}
```

The batch is **per-plan**: every entry's `plan_id` must equal the
batch's `plan_id`, otherwise the whole tx fails.

#### `resolve_finding` / `resolve_finding_batch`

Same shape pattern. The resolver's pubkey must be a registered, active
agent with role `project-manager` or `qa`. Other roles get
`UnauthorizedResolver`. A finding can only be resolved once.

#### `rotate_agent`

```jsonc
{
  "rotate_agent": {
    "old_pubkey": "<base64>",
    "new_pubkey": "<base64>",
    "new_label": "claude-adversary-v2",
    "new_model_id": "claude-opus-5",
    "reason": "model upgrade",
  },
}
```

The new agent starts at `rotation_floor` balance and carries forward
the old agent's `tp_count` + `fp_count`. The old agent gets
`retired_at` + `superseded_by` set.

### Query messages

| Query                             | Returns                                   |
| --------------------------------- | ----------------------------------------- |
| `reputation { pubkey }`           | balance + tp/fp counts (zeros if unknown) |
| `agent { pubkey }`                | full `AgentRecord`                        |
| `agent_by_label { label }`        | full `AgentRecord`                        |
| `finding { plan_id, finding_id }` | `Option<FindingState>`                    |
| `leaderboard { role?, limit? }`   | top agents by balance, descending         |
| `config {}`                       | the stored instantiate parameters         |

The leaderboard caps at 100 entries (`MAX_LEADERBOARD` in
`src/query/leaderboard.rs`) regardless of `limit`.

## Wire format

- **Binary** fields (pubkeys, signatures) are standard **base64**.
  Tribunal's Go client uses `base64.StdEncoding` in `messages.go`.
- **Uint128** fields (balances, stakes, rewards) are **decimal strings**.
  This is cosmwasm-std's default and avoids JS-side `Number` precision
  loss.
- **Severity** is one of `critical | warning | suggestion`.
- **Outcome** is one of `true_positive | false_positive | stale_duplicate | indeterminate`.
- **Role** is one of: `project-manager | architect | implementer |
reviewer-arch | reviewer-sec | reviewer-perf | adversary | classifier
| qa`.

## Canonical signing messages

The contract verifies finding + resolution signatures with
`deps.api.ed25519_verify`. The bytes that get signed are constructed
identically in both languages:

**Finding**

```
TRIBUNAL_FINDING|<plan_id>|<finding_id>|<severity>|<claim_hash>|<stake>
```

`stake` is the decimal representation of the `Uint128` (no commas, no
padding, no leading zeros).

**Resolution**

```
TRIBUNAL_RESOLUTION|<plan_id>|<finding_id>|<outcome>|<evidence_hash>
```

Both helpers live as twin functions:

- Rust: `contracts/tribunal-reputation/src/execute/commit.rs::canonical_finding_message`
- Go: `internal/chain/canonical.go::CanonicalFindingMessage`

**Any change to the format requires a coordinated update on both sides
and a chain migration.** The format is intentionally
ASCII-pipe-separated so it's debuggable from a hex dump.

## Hybrid settlement flow

The methodology distinguishes critical findings (need to land
immediately because the trio's approval should not survive them) from
non-critical findings (warnings + suggestions that can wait until plan
close).

```
                ┌─── critical ────►  CommitRealtime  ──►  Execute(CommitFinding)
Local review                              │
findings        │                         └─ on failure ─►  chain-queue.jsonl
                │
                └─── non-critical ─►  ledger.jsonl  ──►  Sync.SyncPlan
                                                            │
                                                            ├─►  drain queue
                                                            ├─►  Execute(CommitFindingBatch)
                                                            └─►  Execute(ResolveFindingBatch)
```

`tribunal chain sync` is what the project manager (or CI) runs at
plan-close. It groups every entry in the ledger by `plan_id` and
submits one `CommitFindingBatch` + one `ResolveFindingBatch` per plan,
folding in any queued retries first.

A failed real-time commit lands in `.tribunal/chain-queue.jsonl` rather
than failing the review — the next plan-close sync picks it up.

## Operational notes

### Gas

The contract uses dense `Map` storage and `Item` for config. Typical
cost on the XION testnet:

| Operation                                | Approx gas |
| ---------------------------------------- | ---------- |
| `register_agent`                         | ~120k      |
| `commit_finding` (single)                | ~140k      |
| `commit_finding_batch` (10 findings)     | ~750k      |
| `resolve_finding_batch` (10 resolutions) | ~850k      |
| `leaderboard` (read, off-chain)          | n/a        |

Numbers are indicative. Use `--gas auto` with a 1.4 adjustment.

### Keyring

The Go client never touches the operator seed. It assembles the
ExecuteMsg JSON and shells out to `xiond tx wasm execute --from
$OPERATOR_KEY ...`. Configure the key name via `~/.tribunal/chain.yaml`:

```yaml
operator_key_name: my-pm-key
keyring_backend: test
```

For production use `keyring_backend: os`.

### Docker-only xiond

If you don't have `xiond` on host PATH (e.g. you're running the Burnt
XION devnet via docker-compose), set:

```yaml
xiond_binary: "docker exec devnet-xion-1 xiond"
```

The Go client splits this on whitespace, treats the first token as the
binary, and prepends the remaining tokens to every invocation.

## Deployment

```
# 1. (optional) probe the devnet first
./scripts/init-testnet.sh

# 2. build + upload + instantiate the contract
export CHAIN_ID=xion-devnet-1
export NODE=tcp://localhost:26657
export KEY=xiond-2
export XIOND="docker exec devnet-xion-1 xiond"
./scripts/deploy-contract.sh

# 3. paste the emitted YAML into ~/.tribunal/chain.yaml, or use:
tribunal chain init --chain-id xion-devnet-1 \
  --node-rpc http://localhost:26657 \
  --node-rest http://localhost:1317 \
  --contract <cosmwasm1...> \
  --key xiond-2 \
  --xiond-binary "docker exec devnet-xion-1 xiond"

# 4. sanity check
tribunal chain status
tribunal chain query config
```

## Invariants enforced by the contract

These are tested in `contracts/tribunal-reputation/tests/integration.rs`:

1. **Unique pubkey + label.** Both are enforced as primary indexes.
2. **Single resolution per finding.** Double-resolve returns
   `FindingAlreadyResolved`.
3. **Resolver authorization.** Only `project-manager` or `qa` roles can
   resolve. Other roles get `UnauthorizedResolver`.
4. **Non-negative balances.** Stake is debited at commit using
   `saturating_sub`; the contract refuses commits where
   `agent.balance < stake` (`InsufficientStake`).
5. **Soulbound.** No `transfer` / `send` execute message exists.
   Balances only move via stake/slash/reward arithmetic.
6. **Rotation accountability.** Old agent's `tp_count` + `fp_count`
   carry forward; balance resets to `rotation_floor`; old agent is
   marked retired but never deleted, so historical signatures stay
   verifiable.
7. **Per-plan batching.** `commit_finding_batch` / `resolve_finding_batch`
   refuse entries whose `plan_id` differs from the batch's `plan_id`.

## What v0.3 deliberately doesn't do

- **Multi-org tenancy.** One global registry shared across deployments.
  v0.4+ adds per-org namespacing.
- **Slashing appeals.** Resolutions are final. A PM can file a
  correcting resolution under a different `finding_id`, but the
  original stays in the audit trail.
- **Cross-chain reputation.** XION only.
- **Fungible operator rewards.** A separate token to pay human
  operators is out of scope.
- **Web dashboard.** Use `tribunal chain query leaderboard`.

## See also

- `docs/methodology.md` — process backbone, hybrid review, verification
  pyramid, why on-chain at all.
- `docs/incentive-mechanism.md` — reputation math, decay window,
  diversity bonus.
- `contracts/tribunal-reputation/src/lib.rs` — contract entry points.
- `internal/chain/messages.go` — Go-side wire-format mirrors.
