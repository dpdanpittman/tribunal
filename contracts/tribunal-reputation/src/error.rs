use cosmwasm_std::StdError;
use thiserror::Error;

/// `ContractError` enumerates every domain-specific failure the contract
/// can return. The variants are precise enough that downstream clients
/// (the Go `tribunal chain` CLI) can route on them without parsing
/// messages.
#[derive(Error, Debug, PartialEq)]
pub enum ContractError {
    #[error("{0}")]
    Std(#[from] StdError),

    #[error("agent already registered (pubkey)")]
    AgentAlreadyRegistered,

    #[error("agent label {0} already taken")]
    LabelAlreadyTaken(String),

    #[error("agent not registered (pubkey)")]
    AgentNotRegistered,

    #[error("agent {0} is retired")]
    AgentRetired(String),

    #[error("finding {plan_id}/{finding_id} already committed")]
    FindingAlreadyCommitted { plan_id: String, finding_id: String },

    #[error("finding {plan_id}/{finding_id} not committed")]
    FindingNotCommitted { plan_id: String, finding_id: String },

    #[error("finding {plan_id}/{finding_id} already resolved")]
    FindingAlreadyResolved { plan_id: String, finding_id: String },

    #[error("unauthorized resolver: only project-manager or qa agents may resolve findings")]
    UnauthorizedResolver,

    #[error("invalid signature: failed to verify with the agent's registered pubkey")]
    InvalidSignature,

    #[error("invalid severity {0}; must be one of: critical, warning, suggestion")]
    InvalidSeverity(String),

    #[error("invalid outcome {0}; must be one of: true_positive, false_positive, stale_duplicate, indeterminate")]
    InvalidOutcome(String),

    #[error("invalid role {0}; must be one of: project-manager, architect, implementer, reviewer-arch, reviewer-sec, reviewer-perf, adversary, classifier, qa")]
    InvalidRole(String),

    #[error("insufficient stake balance: agent has {balance}, finding requires {requested}")]
    InsufficientStake { balance: u128, requested: u128 },

    #[error("zero or negative initial balance not allowed")]
    InvalidInitialBalance,

    #[error("pubkey must decode to 32 bytes (got {0})")]
    InvalidPubkeyLength(usize),

    #[error("rotation requires source agent to be registered and not yet retired")]
    InvalidRotationSource,

    #[error("rotation target pubkey is already registered")]
    InvalidRotationTarget,

    #[error("batch contains zero items")]
    EmptyBatch,
}
