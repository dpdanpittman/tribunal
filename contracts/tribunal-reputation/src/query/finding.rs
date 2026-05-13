use cosmwasm_std::{Deps, StdResult};

use crate::msg::FindingResp;
use crate::state::FINDINGS;

pub fn finding(deps: Deps, plan_id: String, finding_id: String) -> StdResult<FindingResp> {
    let finding = FINDINGS.may_load(deps.storage, (plan_id.as_str(), finding_id.as_str()))?;
    Ok(FindingResp { finding })
}
