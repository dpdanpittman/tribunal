use cosmwasm_std::{Binary, DepsMut, Env, MessageInfo, Response};

use crate::error::ContractError;
use crate::state::{AgentRecord, AGENTS, AGENTS_BY_LABEL, CONFIG};
use crate::validate::{
    validate_id_field, validate_optional_text, MAX_LABEL_LEN, MAX_MODEL_ID_LEN, MAX_REASON_LEN,
};

/// `rotate_agent` retires `old_pubkey` and creates a fresh agent at
/// `new_pubkey`. The new agent inherits the historical TP/FP counts of the
/// old agent (preserving the accountability trail) but receives the
/// contract's configured `rotation_floor` as starting balance — rotation
/// is not a free top-up.
///
/// The old agent's label binding is removed from `AGENTS_BY_LABEL` so
/// label lookups no longer resolve to a retired record. The retired
/// `AgentRecord` itself (keyed by pubkey) is preserved with `retired_at`
/// + `superseded_by` set, so the accountability trail survives.
#[allow(clippy::too_many_arguments)]
pub fn rotate_agent(
    deps: DepsMut,
    env: Env,
    _info: MessageInfo,
    old_pubkey: Binary,
    new_pubkey: Binary,
    new_label: String,
    new_model_id: String,
    reason: String,
) -> Result<Response, ContractError> {
    if old_pubkey == new_pubkey {
        return Err(ContractError::InvalidRotationTarget);
    }
    if new_pubkey.len() != 32 {
        return Err(ContractError::InvalidPubkeyLength(new_pubkey.len()));
    }
    validate_id_field("new_label", &new_label, MAX_LABEL_LEN)?;
    validate_id_field("new_model_id", &new_model_id, MAX_MODEL_ID_LEN)?;
    validate_optional_text("reason", &reason, MAX_REASON_LEN)?;

    if AGENTS.has(deps.storage, new_pubkey.as_slice()) {
        return Err(ContractError::InvalidRotationTarget);
    }

    let mut old: AgentRecord = AGENTS
        .may_load(deps.storage, old_pubkey.as_slice())?
        .ok_or(ContractError::InvalidRotationSource)?;
    if old.retired_at.is_some() {
        return Err(ContractError::InvalidRotationSource);
    }

    // The new label is allowed to equal the old one (the new agent can
    // keep using the predecessor's name). Free the old binding first so
    // we don't trip the `LabelAlreadyTaken` check below.
    AGENTS_BY_LABEL.remove(deps.storage, old.label.as_str());

    if AGENTS_BY_LABEL.has(deps.storage, new_label.as_str()) {
        return Err(ContractError::LabelAlreadyTaken(new_label));
    }

    let cfg = CONFIG.load(deps.storage)?;

    let new_agent = AgentRecord {
        label: new_label.clone(),
        model_id: new_model_id,
        role: old.role.clone(),
        balance: cfg.rotation_floor,
        tp_count: old.tp_count,
        fp_count: old.fp_count,
        created_at: env.block.time,
        retired_at: None,
        superseded_by: None,
        rotated_from: Some(old_pubkey.clone()),
    };

    // Mark old as retired (preserved in AGENTS for the accountability trail).
    let retired_label = old.label.clone();
    old.retired_at = Some(env.block.time);
    old.superseded_by = Some(new_pubkey.clone());

    AGENTS.save(deps.storage, old_pubkey.as_slice(), &old)?;
    AGENTS.save(deps.storage, new_pubkey.as_slice(), &new_agent)?;
    AGENTS_BY_LABEL.save(deps.storage, new_label.as_str(), &new_pubkey)?;

    Ok(Response::new()
        .add_attribute("method", "rotate_agent")
        .add_attribute("retired_label", retired_label)
        .add_attribute("new_label", new_agent.label))
}
