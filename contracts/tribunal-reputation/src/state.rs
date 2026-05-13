use cosmwasm_std::{Binary, Timestamp, Uint128};
use cw_storage_plus::{Item, Map};
use serde::{Deserialize, Serialize};

/// `Role` mirrors the on-disk role enum in the Go side. Only agents whose
/// role is `project-manager` or `qa` may submit resolutions.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
#[serde(rename_all = "kebab-case")]
pub enum Role {
    ProjectManager,
    Architect,
    Implementer,
    ReviewerArch,
    ReviewerSec,
    ReviewerPerf,
    Adversary,
    Classifier,
    Qa,
}

impl Role {
    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "project-manager" => Some(Role::ProjectManager),
            "architect" => Some(Role::Architect),
            "implementer" => Some(Role::Implementer),
            "reviewer-arch" => Some(Role::ReviewerArch),
            "reviewer-sec" => Some(Role::ReviewerSec),
            "reviewer-perf" => Some(Role::ReviewerPerf),
            "adversary" => Some(Role::Adversary),
            "classifier" => Some(Role::Classifier),
            "qa" => Some(Role::Qa),
            _ => None,
        }
    }

    pub fn can_resolve(&self) -> bool {
        matches!(self, Role::ProjectManager | Role::Qa)
    }
}

/// `Severity` is the filed severity of a finding.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Severity {
    Critical,
    Warning,
    Suggestion,
}

impl Severity {
    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "critical" => Some(Severity::Critical),
            "warning" => Some(Severity::Warning),
            "suggestion" => Some(Severity::Suggestion),
            _ => None,
        }
    }

    /// `weight` returns the severity-weighted multiplier applied during
    /// reputation calculation. Higher severity = more reputation impact.
    pub fn weight(&self) -> u128 {
        match self {
            Severity::Critical => 4,
            Severity::Warning => 2,
            Severity::Suggestion => 1,
        }
    }

    /// `default_stake` returns the reputation amount staked when filing a
    /// finding of this severity.
    pub fn default_stake(&self) -> Uint128 {
        match self {
            Severity::Critical => Uint128::new(8),
            Severity::Warning => Uint128::new(4),
            Severity::Suggestion => Uint128::new(2),
        }
    }
}

/// `Outcome` records how a finding settled.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Outcome {
    TruePositive,
    FalsePositive,
    StaleDuplicate,
    Indeterminate,
}

impl Outcome {
    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "true_positive" => Some(Outcome::TruePositive),
            "false_positive" => Some(Outcome::FalsePositive),
            "stale_duplicate" => Some(Outcome::StaleDuplicate),
            "indeterminate" => Some(Outcome::Indeterminate),
            _ => None,
        }
    }
}

/// `AgentRecord` is the on-chain record for one Tribunal agent. Pubkey
/// is the immutable identity; the contract uses the 32-byte ed25519
/// public key (raw bytes) as the storage key.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct AgentRecord {
    /// Human-readable label (unique). E.g. "claude-adversary".
    pub label: String,
    /// Model identifier reported on registration. E.g. "claude-opus-4-7".
    pub model_id: String,
    /// Role; determines whether this agent can resolve findings.
    pub role: Role,
    /// Current reputation balance. Floored at zero by the slash path.
    pub balance: Uint128,
    /// Lifetime true-positive count.
    pub tp_count: u64,
    /// Lifetime false-positive count.
    pub fp_count: u64,
    /// Timestamp the agent was first registered.
    pub created_at: Timestamp,
    /// If retired, the timestamp of retirement. None means active.
    pub retired_at: Option<Timestamp>,
    /// If retired via rotation, the pubkey of the successor agent.
    pub superseded_by: Option<Binary>,
    /// If this agent was created by rotation, the pubkey of the predecessor.
    pub rotated_from: Option<Binary>,
}

/// `FindingState` is the on-chain state of one filed finding. The full
/// finding text is off-chain (referenced by `claim_hash`); only the hash
/// + reputation-relevant metadata lives here.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct FindingState {
    pub plan_id: String,
    pub finding_id: String,
    pub agent_pubkey: Binary,
    pub severity: Severity,
    pub claim_hash: String,
    pub stake: Uint128,
    pub committed_at: Timestamp,
    pub resolution: Option<ResolutionRecord>,
}

/// `ResolutionRecord` is the on-chain settlement of one finding. Split
/// into `stake_returned` + `reward` so off-chain consumers can distinguish
/// stake-return from outcome-reward without re-deriving from severity.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct ResolutionRecord {
    pub outcome: Outcome,
    pub resolver_pubkey: Binary,
    pub evidence_hash: String,
    pub resolved_at: Timestamp,
    /// Stake returned to the filing agent's balance.
    pub stake_returned: Uint128,
    /// Additional reward (TP only). Zero on FP / Stale / Indeterminate.
    pub reward: Uint128,
}

/// `Config` is the contract-level configuration set at instantiation.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct Config {
    /// Address allowed to migrate the contract or update config. Typically
    /// the deployer; can be transferred via a future ExecuteMsg::UpdateAdmin.
    pub admin: cosmwasm_std::Addr,
    /// Default initial reputation balance for newly registered agents.
    pub initial_balance: Uint128,
    /// Balance new (post-rotation) agents inherit; lower than initial so
    /// rotation is not a free top-up.
    pub rotation_floor: Uint128,
    /// Reward multiplier on true positives. E.g. 2 means a TP returns the
    /// stake plus 2× the stake as additional reputation.
    pub outcome_reward_multiplier: Uint128,
}

/// Storage maps + items used by the contract.
///
/// Contract configuration.
pub const CONFIG: Item<Config> = Item::new("config");

/// AgentRecord keyed by raw pubkey bytes (32-byte ed25519).
pub const AGENTS: Map<&[u8], AgentRecord> = Map::new("agents");

/// Label → pubkey mapping for human-friendly lookup. Removed on rotation
/// (the predecessor's accountability trail lives in the `AGENTS` record
/// via `retired_at` + `superseded_by`).
pub const AGENTS_BY_LABEL: Map<&str, Binary> = Map::new("agents_by_label");

/// FindingState keyed by (plan_id, finding_id).
pub const FINDINGS: Map<(&str, &str), FindingState> = Map::new("findings");
