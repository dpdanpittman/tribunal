use cosmwasm_std::Binary;
use serde::{Deserialize, Serialize};

use crate::state::{AgentRecord, FindingState, Severity, Outcome};

/// `InstantiateMsg` is the one-time configuration applied when the contract
/// is instantiated.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct InstantiateMsg {
    /// Admin address. If `None`, the message sender is used.
    pub admin: Option<String>,
    /// Default initial reputation balance for newly registered agents.
    /// Defaults to 100 when `None`.
    pub initial_balance: Option<u128>,
    /// Balance new (post-rotation) agents inherit. Defaults to 10 when `None`.
    pub rotation_floor: Option<u128>,
    /// Outcome reward multiplier (1× = break-even; 2× = methodology default).
    pub outcome_reward_multiplier: Option<u128>,
}

/// `FindingCommit` is one entry in a `CommitFindingBatch`. Critical
/// findings should use `CommitFinding` (the real-time path); routine
/// non-critical findings batch at plan close via `CommitFindingBatch`.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct FindingCommit {
    pub plan_id: String,
    pub finding_id: String,
    pub agent_pubkey: Binary,
    /// "critical" / "warning" / "suggestion"
    pub severity: String,
    pub claim_hash: String,
    pub stake: u128,
    /// ed25519 signature by the filing agent over the canonical bytes
    /// `plan_id || finding_id || severity || claim_hash || stake`.
    pub signature: Binary,
}

/// `ResolutionCommit` is one entry in a `ResolveFindingBatch`.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct ResolutionCommit {
    pub plan_id: String,
    pub finding_id: String,
    /// "true_positive" / "false_positive" / "stale_duplicate" / "indeterminate"
    pub outcome: String,
    pub resolver_pubkey: Binary,
    pub evidence_hash: String,
    /// ed25519 signature by the resolver over the canonical bytes
    /// `plan_id || finding_id || outcome || evidence_hash`.
    pub signature: Binary,
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum ExecuteMsg {
    /// Register a fresh agent. The pubkey must not already be registered;
    /// the label must not already be taken.
    RegisterAgent {
        pubkey: Binary,
        label: String,
        model_id: String,
        /// "project-manager" | "adversary" | "reviewer-arch" | ...
        role: String,
        /// Optional override of the contract default. Must be >= 1.
        initial_balance: Option<u128>,
    },

    /// Commit a single finding immediately. Used for the *real-time* path
    /// (severity=critical) and for ad-hoc single-finding flows.
    CommitFinding(FindingCommit),

    /// Commit a batch of findings for one plan. Used for plan-close
    /// settlement of non-critical findings.
    CommitFindingBatch {
        plan_id: String,
        findings: Vec<FindingCommit>,
    },

    /// Resolve a single finding. Useful for ad-hoc resolution; most
    /// settlements should use the batch.
    ResolveFinding(ResolutionCommit),

    /// Resolve all findings for one plan in a single transaction.
    ResolveFindingBatch {
        plan_id: String,
        resolutions: Vec<ResolutionCommit>,
    },

    /// Rotate an agent's identity to a new pubkey. The old agent is
    /// retired (history preserved). The new agent inherits the
    /// `rotation_floor` balance plus the historical TP/FP counts.
    RotateAgent {
        old_pubkey: Binary,
        new_pubkey: Binary,
        new_label: String,
        new_model_id: String,
        reason: String,
    },
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum QueryMsg {
    /// Returns the current reputation balance + TP/FP counts for a pubkey.
    Reputation { pubkey: Binary },

    /// Returns the full agent record for the pubkey.
    Agent { pubkey: Binary },

    /// Returns the full agent record by label (convenience).
    AgentByLabel { label: String },

    /// Returns the finding state. None if not committed.
    Finding {
        plan_id: String,
        finding_id: String,
    },

    /// Returns the top-N agents by current reputation balance.
    Leaderboard {
        /// Optional role filter. None = all roles.
        role: Option<String>,
        /// Maximum entries to return. Capped at 100.
        limit: Option<u32>,
    },

    /// Returns the contract's instantiation config.
    Config {},
}

// Response shapes.

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct ReputationResp {
    pub pubkey: Binary,
    pub label: Option<String>,
    pub balance: u128,
    pub tp_count: u64,
    pub fp_count: u64,
    pub retired: bool,
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct AgentResp {
    pub agent: AgentRecord,
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct FindingResp {
    pub finding: Option<FindingState>,
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct LeaderboardEntry {
    pub pubkey: Binary,
    pub label: String,
    pub role: String,
    pub balance: u128,
    pub tp_count: u64,
    pub fp_count: u64,
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct LeaderboardResp {
    pub entries: Vec<LeaderboardEntry>,
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct ConfigResp {
    pub admin: String,
    pub initial_balance: u128,
    pub rotation_floor: u128,
    pub outcome_reward_multiplier: u128,
}

// Re-export some types for tests.
pub use crate::state::Severity as SeverityResp;
pub use crate::state::Outcome as OutcomeResp;
// Keep aliases stable in case test code wants them.
pub type _Severity = Severity;
pub type _Outcome = Outcome;
