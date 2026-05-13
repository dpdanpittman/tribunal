use cosmwasm_std::{Binary, Deps, StdResult, Uint128};

use crate::msg::ReputationResp;
use crate::state::AGENTS;

pub fn reputation(deps: Deps, pubkey: Binary) -> StdResult<ReputationResp> {
    let agent = AGENTS.may_load(deps.storage, pubkey.as_slice())?;
    Ok(match agent {
        Some(a) => ReputationResp {
            pubkey,
            label: Some(a.label),
            balance: a.balance,
            tp_count: a.tp_count,
            fp_count: a.fp_count,
            retired: a.retired_at.is_some(),
        },
        None => ReputationResp {
            pubkey,
            label: None,
            balance: Uint128::zero(),
            tp_count: 0,
            fp_count: 0,
            retired: false,
        },
    })
}
