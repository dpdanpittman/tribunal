# Installation

Tribunal ships as a single Go binary plus a set of markdown skills and agent definitions. Installation has three layers:

1. **The CLI** (`tribunal`) — installed via `go install`.
2. **The skills and agents** — installed into your editor / harness host via `tribunal init`.
3. **Per-project state** — created in `.tribunal/` in the project's working directory.

## Prerequisites

| Component                  | Required         | Notes                                                    |
| -------------------------- | ---------------- | -------------------------------------------------------- |
| Go 1.23+                   | yes              | For installing the CLI.                                  |
| Git                        | yes              | Tribunal records review reports as commits.              |
| A supported harness        | yes              | One of: Claude Code, OpenCode, or Cursor.                |
| Anthropic API key          | optional (v0.2+) | Needed for multi-model adversarial review via Anthropic. |
| OpenAI / Google API keys   | optional (v0.2+) | Needed for multi-model dispatch beyond Claude.           |
| LM Studio                  | optional (v0.2+) | Local-model adversary floor.                             |
| Burnt XION testnet account | optional (v0.3+) | On-chain settlement.                                     |

## Install the CLI

```bash
go install github.com/dpdanpittman/tribunal/cmd/tribunal@latest
```

Verify:

```bash
tribunal --version
```

## Initialize a host

Pick your harness:

### Claude Code

```bash
tribunal init --target claude-code
```

This copies:

- All `skills/tribunal-*` to `~/.claude/skills/`
- All `agents/tribunal-*.md` to `~/.claude/agents/`

After init, the skills are discoverable in any Claude Code session.

### OpenCode

```bash
tribunal init --target opencode
```

This generates a plugin entry in your `opencode.json` plugin list and copies the skills/agents into the resolved plugin directory.

### Cursor

```bash
tribunal init --target cursor
```

Tribunal will install at `~/.cursor/plugins/local/tribunal/` and add the appropriate rules. Reload the Cursor window after install.

### Auto-detect

If you don't specify `--target`, Tribunal will detect from the current environment:

- If `CLAUDE_PROJECT_DIR` is set or `~/.claude/` exists → Claude Code
- Else if `~/.opencode/` or an `opencode.json` is nearby → OpenCode
- Else if `~/.cursor/` exists → Cursor

## Register your agents

Each Tribunal agent (each model + role combination) needs an ed25519 keypair.

```bash
tribunal agents add claude-adversary --model claude-opus-4-7 --role adversary
tribunal agents add claude-reviewer-arch --model claude-opus-4-7 --role reviewer-arch
tribunal agents add claude-reviewer-sec --model claude-opus-4-7 --role reviewer-sec
tribunal agents add claude-reviewer-perf --model claude-opus-4-7 --role reviewer-perf
tribunal agents add claude-pm --model claude-opus-4-7 --role project-manager
tribunal agents add claude-implementer --model claude-opus-4-7 --role implementer
tribunal agents add claude-classifier --model claude-opus-4-7 --role classifier
tribunal agents add claude-qa --model claude-opus-4-7 --role qa
```

This generates `~/.tribunal/agents/<name>.key` (private, 0600) and `~/.tribunal/agents/<name>.pub` (public + metadata) for each.

For multi-model dispatch later (v0.2+), add agents from other model families:

```bash
tribunal agents add gpt-adversary --model gpt-5 --role adversary
tribunal agents add gemini-adversary --model gemini-2.5-pro --role adversary
tribunal agents add local-qwen-adversary --model qwen-3-32b --role adversary
```

## Per-project setup

In any project where you want Tribunal to operate:

```bash
cd <your-project>
git init   # if not already
echo ".tribunal/" >> .gitignore  # actually, keep tracked — see below
```

Tribunal creates `.tribunal/` containing:

- `status.json` — plan registry, current state machine state, residual findings.
- `ledger.jsonl` — append-only signed findings/resolutions.
- `findings/F-*.md` — full text of each finding.
- `resolutions/F-*.md` — full text of each resolution.
- `reports/<plan-id>/` — QC reports per plan, per reviewer.
- `plans/<plan-id>/` — intent doc, plan, tasks, status per plan.

**Decision: track `.tribunal/` in git, or not?**

Recommended: **yes, track it.** The review history is valuable context for future reviewers and a public audit trail. The local ledger is signed and append-only, so collaborators can't quietly rewrite it. The chain layer (v0.3+) ultimately settles disputes anyway.

If you have specific reasons to keep it untracked (private bug content, etc.), add `.tribunal/` to `.gitignore` and use `tribunal export` (planned for v0.4) to generate a shareable summary.

## Verify install

```bash
cd <your-project>
tribunal agents list
tribunal ledger summary
```

If `tribunal agents list` shows your registered agents and `ledger summary` reports a fresh empty state, you're ready.

## Optional: API keys for multi-model dispatch (v0.2+)

For multi-model adversarial review, set the API keys for each provider you want to use:

```bash
export ANTHROPIC_API_KEY=...
export OPENAI_API_KEY=...
export GOOGLE_API_KEY=...
export LM_STUDIO_URL=http://localhost:1234    # default; only set if different
```

Add them to your shell rc file or use a secret manager appropriate to your environment.

## Optional: Burnt XION chain config (v0.3+)

For on-chain settlement, configure `~/.tribunal/chain.toml`:

```toml
[chain]
rpc = "https://rpc.xion-testnet-2.burnt.com:443"
chain_id = "xion-testnet-2"
contract_address = "xion1..."

[operator]
mnemonic_path = "~/.tribunal/operator.mnemonic"  # 0600
gas_price = "0.025uxion"
```

The operator account is the local human-or-org account that signs the on-chain transactions. It's separate from the per-agent ed25519 keys (those sign findings; the operator pays gas).

## Troubleshooting

**`tribunal init` says it can't detect a host.** Pass `--target` explicitly.

**`tribunal agents add` fails with `label already taken`.** Each agent label must be unique in your local registry. Pick a different label.

**`tribunal ledger summary` shows nothing after several reviews.** Verify the agent signatures are valid: `tribunal ledger verify`. If verification fails, the ledger is corrupted or an agent's key was replaced without rotation — see the agent rotation guide.

**Multi-model dispatch is silently slow.** Verify all required API keys are exported and reachable: `tribunal dispatch test --providers claude,openai,google,local`.
