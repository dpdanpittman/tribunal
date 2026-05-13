use cosmwasm_std::{DepsMut, Env, MessageInfo, Response, Uint128};

use crate::error::ContractError;
use crate::msg::ResolutionCommit;
use crate::state::{AgentRecord, Outcome, ResolutionRecord, AGENTS, CONFIG, FINDINGS};
use crate::validate::{validate_batch_size, validate_id_field, MAX_HASH_LEN, MAX_ID_LEN};

/// `resolve_finding` settles a single finding. Returns / slashes stake +
/// updates the filing agent's TP / FP counters.
pub fn resolve_finding(
    deps: DepsMut,
    env: Env,
    _info: MessageInfo,
    r: ResolutionCommit,
) -> Result<Response, ContractError> {
    process_resolution(deps, env, r)?;
    Ok(Response::new().add_attribute("method", "resolve_finding"))
}

/// `resolve_finding_batch` settles N findings for one plan in a single
/// transaction.
pub fn resolve_finding_batch(
    mut deps: DepsMut,
    env: Env,
    _info: MessageInfo,
    plan_id: String,
    resolutions: Vec<ResolutionCommit>,
) -> Result<Response, ContractError> {
    validate_batch_size(resolutions.len())?;
    validate_id_field("plan_id", &plan_id, MAX_ID_LEN)?;
    let count = resolutions.len();
    for r in resolutions {
        if r.plan_id != plan_id {
            return Err(ContractError::BatchMixedPlanID {
                batch_plan_id: plan_id.clone(),
                found_plan_id: r.plan_id,
                finding_id: r.finding_id,
            });
        }
        process_resolution(deps.branch(), env.clone(), r)?;
    }
    Ok(Response::new()
        .add_attribute("method", "resolve_finding_batch")
        .add_attribute("plan_id", plan_id)
        .add_attribute("count", count.to_string()))
}

fn process_resolution(deps: DepsMut, env: Env, r: ResolutionCommit) -> Result<(), ContractError> {
    validate_id_field("plan_id", &r.plan_id, MAX_ID_LEN)?;
    validate_id_field("finding_id", &r.finding_id, MAX_ID_LEN)?;
    validate_id_field("evidence_hash", &r.evidence_hash, MAX_HASH_LEN)?;

    // Resolver must be a registered, active agent with a resolver role.
    let resolver: AgentRecord = AGENTS
        .may_load(deps.storage, r.resolver_pubkey.as_slice())?
        .ok_or(ContractError::AgentNotRegistered)?;
    if resolver.retired_at.is_some() {
        return Err(ContractError::AgentRetired(resolver.label.clone()));
    }
    if !resolver.role.can_resolve() {
        return Err(ContractError::UnauthorizedResolver);
    }

    let outcome = Outcome::parse(&r.outcome)
        .ok_or_else(|| ContractError::InvalidOutcome(r.outcome.clone()))?;

    // Verify resolver's signature over the canonical message.
    let canonical =
        canonical_resolution_message(&r.plan_id, &r.finding_id, &r.outcome, &r.evidence_hash);
    let verified = deps
        .api
        .ed25519_verify(
            &canonical,
            r.signature.as_slice(),
            r.resolver_pubkey.as_slice(),
        )
        .map_err(|_| ContractError::InvalidSignature)?;
    if !verified {
        return Err(ContractError::InvalidSignature);
    }

    // Load the finding being resolved.
    let key = (r.plan_id.as_str(), r.finding_id.as_str());
    let mut state = FINDINGS.may_load(deps.storage, key)?.ok_or_else(|| {
        ContractError::FindingNotCommitted {
            plan_id: r.plan_id.clone(),
            finding_id: r.finding_id.clone(),
        }
    })?;
    if state.resolution.is_some() {
        return Err(ContractError::FindingAlreadyResolved {
            plan_id: r.plan_id,
            finding_id: r.finding_id,
        });
    }

    // Apply the outcome to the filing agent.
    let mut filing_agent: AgentRecord = AGENTS
        .may_load(deps.storage, state.agent_pubkey.as_slice())?
        .ok_or(ContractError::AgentNotRegistered)?;
    let cfg = CONFIG.load(deps.storage)?;

    let mut stake_returned = Uint128::zero();
    let mut reward = Uint128::zero();
    match outcome {
        Outcome::TruePositive => {
            // Stake returned + reward = stake * multiplier.
            stake_returned = state.stake;
            reward = state.stake.saturating_mul(cfg.outcome_reward_multiplier);
            filing_agent.balance = filing_agent.balance.saturating_add(stake_returned);
            filing_agent.balance = filing_agent.balance.saturating_add(reward);
            filing_agent.tp_count = filing_agent.tp_count.saturating_add(1);
        }
        Outcome::FalsePositive => {
            // Stake stays slashed; balance already debited at commit time.
            filing_agent.fp_count = filing_agent.fp_count.saturating_add(1);
        }
        Outcome::StaleDuplicate => {
            // No reputation change beyond returning the stake (the agent
            // shouldn't be punished for surfacing something a faster agent
            // already caught).
            stake_returned = state.stake;
            filing_agent.balance = filing_agent.balance.saturating_add(stake_returned);
        }
        Outcome::Indeterminate => {
            // Stake returned, no reward.
            stake_returned = state.stake;
            filing_agent.balance = filing_agent.balance.saturating_add(stake_returned);
        }
    }

    // Persist updated agent + finding state.
    AGENTS.save(deps.storage, state.agent_pubkey.as_slice(), &filing_agent)?;

    state.resolution = Some(ResolutionRecord {
        outcome,
        resolver_pubkey: r.resolver_pubkey,
        evidence_hash: r.evidence_hash,
        resolved_at: env.block.time,
        stake_returned,
        reward,
    });
    FINDINGS.save(deps.storage, key, &state)?;

    Ok(())
}

/// `canonical_resolution_message` returns the bytes a resolver signs to
/// authorize a settlement. Mirrored exactly in the Go-side builder.
pub fn canonical_resolution_message(
    plan_id: &str,
    finding_id: &str,
    outcome: &str,
    evidence_hash: &str,
) -> Vec<u8> {
    format!(
        "TRIBUNAL_RESOLUTION|{}|{}|{}|{}",
        plan_id, finding_id, outcome, evidence_hash
    )
    .into_bytes()
}
