//! `tribunal-reputation` is a CosmWasm contract that anchors Tribunal's
//! reputation ledger on-chain. It implements a **soulbound** reputation
//! balance per registered agent — there is no transfer message; balances
//! move only via stake → slash/reward driven by finding outcomes.
//!
//! See `docs/on-chain-protocol.md` in the repo root for the protocol
//! design, sequence diagrams, and gas estimates.

pub mod contract;
pub mod error;
pub mod msg;
pub mod state;

pub mod execute {
    pub mod commit;
    pub mod register;
    pub mod resolve;
    pub mod rotate;
}

pub mod query {
    pub mod agent;
    pub mod finding;
    pub mod leaderboard;
    pub mod reputation;
}

pub use crate::error::ContractError;
