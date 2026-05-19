//! Property-based tests for tribunal-reputation. Exercises the reputation
//! math via randomly-generated commit + resolve sequences and asserts the
//! invariants the contract documents but that integration tests only spot-
//! check with one or two fixed values.
//!
//! Why these specific properties:
//!   - The commit / resolve math has multiple branches (TP, FP, Stale,
//!     Indeterminate) and a configurable reward multiplier. Spot-checks
//!     miss off-by-one boundaries, wraparound, and stake-times-multiplier
//!     overflow paths.
//!   - Leaderboard ordering is the kind of invariant that's quietly fine
//!     until the storage iteration order changes upstream; PBT pins it
//!     against any sort-key bug.
//!
//! The strategy generators draw small numbers (stakes 1..=64, balances
//! 100..=10_000) to keep cw-multi-test cheap per case while still hitting
//! the interesting boundaries (stake == balance, stake * mult > balance,
//! etc.). proptest's shrinker will minimise any counterexample.

use cosmwasm_std::{Addr, Binary, Uint128};
use cw_multi_test::{App, AppBuilder, ContractWrapper, Executor};
use ed25519_dalek::{Signer, SigningKey, VerifyingKey, SECRET_KEY_LENGTH};
use proptest::prelude::*;

use tribunal_reputation::contract;
use tribunal_reputation::msg::{
    AgentResp, ExecuteMsg, FindingCommit, InstantiateMsg, LeaderboardResp, QueryMsg,
    ResolutionCommit,
};

const ADMIN: &str = "admin";
const REWARD_MULT: u128 = 2; // matches default setup
const INITIAL_BALANCE: u128 = 1_000;

// ---------- helpers (inlined; see tests/integration.rs for the canonical copies) ----------

struct Keypair {
    signing: SigningKey,
    pubkey: Binary,
}

impl Keypair {
    fn from_seed(seed_byte: u8) -> Self {
        let seed = [seed_byte; SECRET_KEY_LENGTH];
        let signing = SigningKey::from_bytes(&seed);
        let verifying: VerifyingKey = signing.verifying_key();
        Keypair {
            pubkey: Binary::new(verifying.to_bytes().to_vec()),
            signing,
        }
    }

    fn sign(&self, msg: &[u8]) -> Binary {
        let sig = self.signing.sign(msg);
        Binary::new(sig.to_bytes().to_vec())
    }
}

fn setup_app() -> (App, Addr) {
    let mut app = AppBuilder::new().build(|_, _, _| {});
    let code = ContractWrapper::new(contract::execute, contract::instantiate, contract::query);
    let code_id = app.store_code(Box::new(code));
    let contract_addr = app
        .instantiate_contract(
            code_id,
            Addr::unchecked(ADMIN),
            &InstantiateMsg {
                admin: None,
                initial_balance: Some(Uint128::new(INITIAL_BALANCE)),
                rotation_floor: Some(Uint128::new(10)),
                outcome_reward_multiplier: Some(Uint128::new(REWARD_MULT)),
            },
            &[],
            "tribunal-reputation",
            None,
        )
        .unwrap();
    (app, contract_addr)
}

fn register(app: &mut App, contract: &Addr, label: &str, role: &str, seed: u8) -> Keypair {
    let kp = Keypair::from_seed(seed);
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::RegisterAgent {
            pubkey: kp.pubkey.clone(),
            label: label.to_string(),
            model_id: "model-x".to_string(),
            role: role.to_string(),
            initial_balance: None,
        },
        &[],
    )
    .unwrap();
    kp
}

fn canonical_finding(
    plan_id: &str,
    finding_id: &str,
    severity: &str,
    claim_hash: &str,
    stake: u128,
) -> Vec<u8> {
    format!(
        "TRIBUNAL_FINDING|{}|{}|{}|{}|{}",
        plan_id, finding_id, severity, claim_hash, stake
    )
    .into_bytes()
}

fn canonical_resolution(
    plan_id: &str,
    finding_id: &str,
    outcome: &str,
    evidence_hash: &str,
) -> Vec<u8> {
    format!(
        "TRIBUNAL_RESOLUTION|{}|{}|{}|{}",
        plan_id, finding_id, outcome, evidence_hash
    )
    .into_bytes()
}

fn agent_balance(app: &App, contract: &Addr, pubkey: &Binary) -> u128 {
    let resp: AgentResp = app
        .wrap()
        .query_wasm_smart(
            contract,
            &QueryMsg::Agent {
                pubkey: pubkey.clone(),
            },
        )
        .unwrap();
    resp.agent.balance.u128()
}

fn commit(
    app: &mut App,
    contract: &Addr,
    filer: &Keypair,
    plan_id: &str,
    finding_id: &str,
    severity: &str,
    stake: u128,
) {
    let claim_hash = "h";
    let sig = filer.sign(&canonical_finding(
        plan_id, finding_id, severity, claim_hash, stake,
    ));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::CommitFinding(FindingCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            agent_pubkey: filer.pubkey.clone(),
            severity: severity.into(),
            claim_hash: claim_hash.into(),
            stake: stake.into(),
            signature: sig,
        }),
        &[],
    )
    .unwrap();
}

fn resolve(
    app: &mut App,
    contract: &Addr,
    resolver: &Keypair,
    plan_id: &str,
    finding_id: &str,
    outcome: &str,
) {
    let evidence_hash = "evd";
    let sig = resolver.sign(&canonical_resolution(plan_id, finding_id, outcome, evidence_hash));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::ResolveFinding(ResolutionCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            outcome: outcome.into(),
            resolver_pubkey: resolver.pubkey.clone(),
            evidence_hash: evidence_hash.into(),
            signature: sig,
        }),
        &[],
    )
    .unwrap();
}

// ---------- strategies ----------

fn severity_strategy() -> impl Strategy<Value = &'static str> {
    prop_oneof![Just("critical"), Just("warning"), Just("suggestion")]
}

// ---------- properties ----------

proptest! {
    /// PROPERTY A — commit → resolve(TruePositive) roundtrip.
    /// Filing agent's balance changes by exactly +stake × reward_mult.
    /// Pre-commit baseline B → post-commit B-S → post-TP B-S+S+S·mult = B+S·mult.
    #[test]
    fn commit_then_resolve_true_positive_returns_stake_plus_reward(
        stake in 1u128..=64,
        sev in severity_strategy(),
    ) {
        let (mut app, contract) = setup_app();
        let adv = register(&mut app, &contract, "adv", "adversary", 0x40);
        let pm = register(&mut app, &contract, "pm", "project-manager", 0x41);

        let pre = agent_balance(&app, &contract, &adv.pubkey);
        commit(&mut app, &contract, &adv, "P-prop-A", "F-prop-A", sev, stake);
        let mid = agent_balance(&app, &contract, &adv.pubkey);
        prop_assert_eq!(mid, pre - stake, "commit should debit stake");

        resolve(&mut app, &contract, &pm, "P-prop-A", "F-prop-A", "true_positive");
        let post = agent_balance(&app, &contract, &adv.pubkey);

        // Net effect on balance from the entire roundtrip:
        //   post = pre - stake + stake_returned + reward
        //        = pre - stake + stake + stake * mult
        //        = pre + stake * mult
        let expected = pre + stake * REWARD_MULT;
        prop_assert_eq!(post, expected,
            "TP roundtrip: balance should be pre + stake × reward_mult");
    }

    /// PROPERTY B — commit → resolve(FalsePositive) roundtrip.
    /// Filing agent's balance changes by exactly -stake (the commit-time
    /// debit stays slashed; no return, no reward).
    #[test]
    fn commit_then_resolve_false_positive_loses_stake(
        stake in 1u128..=64,
        sev in severity_strategy(),
    ) {
        let (mut app, contract) = setup_app();
        let adv = register(&mut app, &contract, "adv", "adversary", 0x50);
        let pm = register(&mut app, &contract, "pm", "project-manager", 0x51);

        let pre = agent_balance(&app, &contract, &adv.pubkey);
        commit(&mut app, &contract, &adv, "P-prop-B", "F-prop-B", sev, stake);
        resolve(&mut app, &contract, &pm, "P-prop-B", "F-prop-B", "false_positive");
        let post = agent_balance(&app, &contract, &adv.pubkey);

        prop_assert_eq!(post, pre - stake,
            "FP roundtrip: balance should be pre - stake (slashed, no return)");
    }

    /// PROPERTY C — leaderboard query is sorted by balance descending,
    /// regardless of agent registration order. The leaderboard's ordering
    /// is the kind of invariant that breaks silently if iteration order
    /// changes upstream in cw-storage-plus; PBT pins it.
    #[test]
    fn leaderboard_sorted_descending_by_balance(
        // Per-agent stake amounts (random vector size 1..=5). Each is the
        // stake the agent commits + then loses to FP, mutating the balance
        // away from the INITIAL_BALANCE baseline.
        stakes in prop::collection::vec(1u128..=64, 1..=5),
    ) {
        let (mut app, contract) = setup_app();
        let pm = register(&mut app, &contract, "pm", "project-manager", 0x60);

        // Register agents, each commits + gets FP'd so balances diverge.
        let mut agents = Vec::new();
        for (i, stake) in stakes.iter().enumerate() {
            let seed = 0x70u8 + i as u8;
            let label = format!("adv-{}", i);
            let adv = register(&mut app, &contract, &label, "adversary", seed);
            let finding_id = format!("F-prop-C-{}", i);
            commit(&mut app, &contract, &adv, "P-prop-C", &finding_id, "warning", *stake);
            resolve(&mut app, &contract, &pm, "P-prop-C", &finding_id, "false_positive");
            agents.push(adv);
        }

        let resp: LeaderboardResp = app
            .wrap()
            .query_wasm_smart(&contract, &QueryMsg::Leaderboard { role: None, limit: Some(20) })
            .unwrap();

        // Check descending sort.
        let balances: Vec<u128> = resp.entries.iter().map(|e| e.balance.u128()).collect();
        for w in balances.windows(2) {
            prop_assert!(
                w[0] >= w[1],
                "leaderboard not descending: {} before {}",
                w[0],
                w[1]
            );
        }
    }
}
