use cosmwasm_std::{DepsMut, Env, MessageInfo, Response, Uint128};

use crate::error::ContractError;
use crate::msg::FindingCommit;
use crate::state::{AgentRecord, FindingState, Severity, AGENTS, FINDINGS};
use crate::validate::{validate_batch_size, validate_id_field, MAX_HASH_LEN, MAX_ID_LEN};

/// `commit_finding` lands a single signed finding on-chain. Used for the
/// real-time path (typically severity=critical) where waiting for the
/// plan-close batch would be too slow.
pub fn commit_finding(
    deps: DepsMut,
    env: Env,
    _info: MessageInfo,
    f: FindingCommit,
) -> Result<Response, ContractError> {
    process_finding(deps, env, f)?;
    Ok(Response::new().add_attribute("method", "commit_finding"))
}

/// `commit_finding_batch` lands N findings for one plan in a single
/// transaction. Typical use: plan-close settlement of warnings / suggestions.
pub fn commit_finding_batch(
    mut deps: DepsMut,
    env: Env,
    _info: MessageInfo,
    plan_id: String,
    findings: Vec<FindingCommit>,
) -> Result<Response, ContractError> {
    validate_batch_size(findings.len())?;
    validate_id_field("plan_id", &plan_id, MAX_ID_LEN)?;
    let count = findings.len();
    for f in findings {
        if f.plan_id != plan_id {
            return Err(ContractError::BatchMixedPlanID {
                batch_plan_id: plan_id.clone(),
                found_plan_id: f.plan_id,
                finding_id: f.finding_id,
            });
        }
        process_finding(deps.branch(), env.clone(), f)?;
    }
    Ok(Response::new()
        .add_attribute("method", "commit_finding_batch")
        .add_attribute("plan_id", plan_id)
        .add_attribute("count", count.to_string()))
}

fn process_finding(deps: DepsMut, env: Env, f: FindingCommit) -> Result<(), ContractError> {
    validate_id_field("plan_id", &f.plan_id, MAX_ID_LEN)?;
    validate_id_field("finding_id", &f.finding_id, MAX_ID_LEN)?;
    validate_id_field("claim_hash", &f.claim_hash, MAX_HASH_LEN)?;

    // Verify finding hasn't already been committed.
    if FINDINGS.has(deps.storage, (f.plan_id.as_str(), f.finding_id.as_str())) {
        return Err(ContractError::FindingAlreadyCommitted {
            plan_id: f.plan_id,
            finding_id: f.finding_id,
        });
    }

    // Resolve agent + verify signature.
    let mut agent: AgentRecord = AGENTS
        .may_load(deps.storage, f.agent_pubkey.as_slice())?
        .ok_or(ContractError::AgentNotRegistered)?;
    if agent.retired_at.is_some() {
        return Err(ContractError::AgentRetired(agent.label.clone()));
    }

    let severity = Severity::parse(&f.severity)
        .ok_or_else(|| ContractError::InvalidSeverity(f.severity.clone()))?;

    let canonical = canonical_finding_message(
        &f.plan_id,
        &f.finding_id,
        &f.severity,
        &f.claim_hash,
        f.stake,
    );

    let verified = deps
        .api
        .ed25519_verify(
            &canonical,
            f.signature.as_slice(),
            f.agent_pubkey.as_slice(),
        )
        .map_err(|_| ContractError::InvalidSignature)?;
    if !verified {
        return Err(ContractError::InvalidSignature);
    }

    // Reserve the stake.
    if agent.balance < f.stake {
        return Err(ContractError::InsufficientStake {
            balance: agent.balance.to_string(),
            requested: f.stake.to_string(),
        });
    }
    agent.balance =
        agent
            .balance
            .checked_sub(f.stake)
            .map_err(|_| ContractError::InsufficientStake {
                balance: agent.balance.to_string(),
                requested: f.stake.to_string(),
            })?;
    AGENTS.save(deps.storage, f.agent_pubkey.as_slice(), &agent)?;

    let state = FindingState {
        plan_id: f.plan_id.clone(),
        finding_id: f.finding_id.clone(),
        agent_pubkey: f.agent_pubkey,
        severity,
        claim_hash: f.claim_hash,
        stake: f.stake,
        committed_at: env.block.time,
        resolution: None,
    };
    FINDINGS.save(
        deps.storage,
        (f.plan_id.as_str(), f.finding_id.as_str()),
        &state,
    )?;
    Ok(())
}

/// `canonical_finding_message` returns the bytes that an agent signs to
/// authorize a commit. The encoding is deliberately simple: ASCII-only,
/// pipe-separated, with the stake serialized as decimal (via `Uint128`'s
/// `Display`). Mirrored exactly in the Go `internal/chain/canonical.go`
/// builder.
pub fn canonical_finding_message(
    plan_id: &str,
    finding_id: &str,
    severity: &str,
    claim_hash: &str,
    stake: Uint128,
) -> Vec<u8> {
    format!(
        "TRIBUNAL_FINDING|{}|{}|{}|{}|{}",
        plan_id, finding_id, severity, claim_hash, stake
    )
    .into_bytes()
}
