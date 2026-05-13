use cosmwasm_std::{Binary, Deps, StdError, StdResult};

use crate::msg::AgentResp;
use crate::state::{AGENTS, AGENTS_BY_LABEL};

pub fn agent(deps: Deps, pubkey: Binary) -> StdResult<AgentResp> {
    let agent = AGENTS
        .may_load(deps.storage, pubkey.as_slice())?
        .ok_or_else(|| StdError::not_found("agent"))?;
    Ok(AgentResp { agent })
}

pub fn agent_by_label(deps: Deps, label: String) -> StdResult<AgentResp> {
    let pubkey = AGENTS_BY_LABEL
        .may_load(deps.storage, label.as_str())?
        .ok_or_else(|| StdError::not_found("label"))?;
    agent(deps, pubkey)
}
