use cosmwasm_std::{
    entry_point, to_json_binary, Binary, Deps, DepsMut, Env, MessageInfo, Response, StdResult,
    Uint128,
};

use crate::error::ContractError;
use crate::execute;
use crate::msg::{ConfigResp, ExecuteMsg, InstantiateMsg, QueryMsg};
use crate::query;
use crate::state::{Config, CONFIG};

#[entry_point]
pub fn instantiate(
    deps: DepsMut,
    _env: Env,
    info: MessageInfo,
    msg: InstantiateMsg,
) -> Result<Response, ContractError> {
    let admin = match msg.admin {
        Some(a) => deps.api.addr_validate(&a)?,
        None => info.sender,
    };
    let cfg = Config {
        admin: admin.clone(),
        initial_balance: msg.initial_balance.unwrap_or_else(|| Uint128::new(100)),
        rotation_floor: msg.rotation_floor.unwrap_or_else(|| Uint128::new(10)),
        outcome_reward_multiplier: msg
            .outcome_reward_multiplier
            .unwrap_or_else(|| Uint128::new(2)),
    };
    if cfg.initial_balance.is_zero() {
        return Err(ContractError::InvalidInitialBalance);
    }
    CONFIG.save(deps.storage, &cfg)?;
    Ok(Response::new()
        .add_attribute("method", "instantiate")
        .add_attribute("admin", admin.to_string()))
}

#[entry_point]
pub fn execute(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    msg: ExecuteMsg,
) -> Result<Response, ContractError> {
    match msg {
        ExecuteMsg::RegisterAgent {
            pubkey,
            label,
            model_id,
            role,
            initial_balance,
        } => execute::register::register_agent(
            deps,
            env,
            info,
            pubkey,
            label,
            model_id,
            role,
            initial_balance,
        ),
        ExecuteMsg::CommitFinding(f) => execute::commit::commit_finding(deps, env, info, f),
        ExecuteMsg::CommitFindingBatch { plan_id, findings } => {
            execute::commit::commit_finding_batch(deps, env, info, plan_id, findings)
        }
        ExecuteMsg::ResolveFinding(r) => execute::resolve::resolve_finding(deps, env, info, r),
        ExecuteMsg::ResolveFindingBatch {
            plan_id,
            resolutions,
        } => execute::resolve::resolve_finding_batch(deps, env, info, plan_id, resolutions),
        ExecuteMsg::RotateAgent {
            old_pubkey,
            new_pubkey,
            new_label,
            new_model_id,
            reason,
        } => execute::rotate::rotate_agent(
            deps,
            env,
            info,
            old_pubkey,
            new_pubkey,
            new_label,
            new_model_id,
            reason,
        ),
    }
}

#[entry_point]
pub fn query(deps: Deps, _env: Env, msg: QueryMsg) -> StdResult<Binary> {
    match msg {
        QueryMsg::Reputation { pubkey } => {
            to_json_binary(&query::reputation::reputation(deps, pubkey)?)
        }
        QueryMsg::Agent { pubkey } => to_json_binary(&query::agent::agent(deps, pubkey)?),
        QueryMsg::AgentByLabel { label } => {
            to_json_binary(&query::agent::agent_by_label(deps, label)?)
        }
        QueryMsg::Finding {
            plan_id,
            finding_id,
        } => to_json_binary(&query::finding::finding(deps, plan_id, finding_id)?),
        QueryMsg::Leaderboard { role, limit } => {
            to_json_binary(&query::leaderboard::leaderboard(deps, role, limit)?)
        }
        QueryMsg::Config {} => {
            let cfg = CONFIG.load(deps.storage)?;
            to_json_binary(&ConfigResp {
                admin: cfg.admin.to_string(),
                initial_balance: cfg.initial_balance,
                rotation_floor: cfg.rotation_floor,
                outcome_reward_multiplier: cfg.outcome_reward_multiplier,
            })
        }
    }
}
