use cosmwasm_std::{Binary, Deps, Order, StdResult};

use crate::msg::{LeaderboardEntry, LeaderboardResp};
use crate::state::{Role, AGENTS};

/// Maximum entries returned by a single Leaderboard query. Capping
/// prevents unbounded query costs on chains with metered execution.
const MAX_LEADERBOARD: u32 = 100;

pub fn leaderboard(
    deps: Deps,
    role: Option<String>,
    limit: Option<u32>,
) -> StdResult<LeaderboardResp> {
    let role_filter: Option<Role> = role.as_deref().and_then(Role::from_str);
    let limit = limit.unwrap_or(20).min(MAX_LEADERBOARD) as usize;

    let mut all: Vec<(Binary, crate::state::AgentRecord)> = vec![];
    for kv in AGENTS.range(deps.storage, None, None, Order::Ascending) {
        let (k, v) = kv?;
        if v.retired_at.is_some() {
            continue;
        }
        if let Some(ref want) = role_filter {
            if &v.role != want {
                continue;
            }
        }
        all.push((Binary::new(k), v));
    }
    // Sort by descending balance, tiebreak by ascending label for stable output.
    all.sort_by(|a, b| {
        b.1.balance
            .cmp(&a.1.balance)
            .then_with(|| a.1.label.cmp(&b.1.label))
    });
    all.truncate(limit);

    let entries = all
        .into_iter()
        .map(|(pubkey, a)| LeaderboardEntry {
            pubkey,
            label: a.label,
            role: role_str(&a.role),
            balance: a.balance,
            tp_count: a.tp_count,
            fp_count: a.fp_count,
        })
        .collect();
    Ok(LeaderboardResp { entries })
}

fn role_str(r: &Role) -> String {
    match r {
        Role::ProjectManager => "project-manager",
        Role::Architect => "architect",
        Role::Implementer => "implementer",
        Role::ReviewerArch => "reviewer-arch",
        Role::ReviewerSec => "reviewer-sec",
        Role::ReviewerPerf => "reviewer-perf",
        Role::Adversary => "adversary",
        Role::Classifier => "classifier",
        Role::Qa => "qa",
    }
    .to_string()
}
