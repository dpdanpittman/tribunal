use cosmwasm_std::{Binary, DepsMut, Env, MessageInfo, Response, Uint128};

use crate::error::ContractError;
use crate::state::{AgentRecord, Role, AGENTS, AGENTS_BY_LABEL, CONFIG};
use crate::validate::{validate_id_field, MAX_LABEL_LEN, MAX_MODEL_ID_LEN};

const PUBKEY_LEN: usize = 32;

/// `register_agent` records a new agent under the given pubkey and label.
///
/// Invariants enforced:
/// - Pubkey must be 32 bytes (raw ed25519).
/// - Pubkey must not already be registered.
/// - Label must not already be taken.
/// - Role must parse to a known `Role` value.
/// - Initial balance, if provided, must be >= 1.
pub fn register_agent(
    deps: DepsMut,
    env: Env,
    _info: MessageInfo,
    pubkey: Binary,
    label: String,
    model_id: String,
    role: String,
    initial_balance: Option<Uint128>,
) -> Result<Response, ContractError> {
    if pubkey.len() != PUBKEY_LEN {
        return Err(ContractError::InvalidPubkeyLength(pubkey.len()));
    }
    validate_id_field("label", &label, MAX_LABEL_LEN)?;
    validate_id_field("model_id", &model_id, MAX_MODEL_ID_LEN)?;
    let role = Role::from_str(&role).ok_or(ContractError::InvalidRole(role.clone()))?;

    if AGENTS.has(deps.storage, pubkey.as_slice()) {
        return Err(ContractError::AgentAlreadyRegistered);
    }
    if AGENTS_BY_LABEL.has(deps.storage, label.as_str()) {
        return Err(ContractError::LabelAlreadyTaken(label.clone()));
    }

    let cfg = CONFIG.load(deps.storage)?;
    let balance = match initial_balance {
        Some(b) if b.is_zero() => return Err(ContractError::InvalidInitialBalance),
        Some(b) => b,
        None => cfg.initial_balance,
    };

    let agent = AgentRecord {
        label: label.clone(),
        model_id,
        role,
        balance,
        tp_count: 0,
        fp_count: 0,
        created_at: env.block.time,
        retired_at: None,
        superseded_by: None,
        rotated_from: None,
    };

    AGENTS.save(deps.storage, pubkey.as_slice(), &agent)?;
    AGENTS_BY_LABEL.save(deps.storage, label.as_str(), &pubkey)?;

    Ok(Response::new()
        .add_attribute("method", "register_agent")
        .add_attribute("label", agent.label)
        .add_attribute("balance", agent.balance.to_string()))
}
