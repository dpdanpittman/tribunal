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
use tribunal_reputation::state::AgentRecord;

const ADMIN: &str = "admin";
const REWARD_MULT: u128 = 2; // matches default setup
const INITIAL_BALANCE: u128 = 1_000;
const ROTATION_FLOOR: u128 = 10; // matches default setup

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
    agent_record(app, contract, pubkey).balance.u128()
}

fn agent_record(app: &App, contract: &Addr, pubkey: &Binary) -> AgentRecord {
    let resp: AgentResp = app
        .wrap()
        .query_wasm_smart(
            contract,
            &QueryMsg::Agent {
                pubkey: pubkey.clone(),
            },
        )
        .unwrap();
    resp.agent
}

fn rotate(
    app: &mut App,
    contract: &Addr,
    old_pubkey: &Binary,
    new_kp: &Keypair,
    new_label: &str,
    new_model_id: &str,
) {
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::RotateAgent {
            old_pubkey: old_pubkey.clone(),
            new_pubkey: new_kp.pubkey.clone(),
            new_label: new_label.into(),
            new_model_id: new_model_id.into(),
            reason: "proptest-rotation".into(),
        },
        &[],
    )
    .unwrap();
}

fn commit_batch(app: &mut App, contract: &Addr, plan_id: &str, findings: Vec<FindingCommit>) {
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::CommitFindingBatch {
            plan_id: plan_id.into(),
            findings,
        },
        &[],
    )
    .unwrap();
}

fn resolve_batch(
    app: &mut App,
    contract: &Addr,
    plan_id: &str,
    resolutions: Vec<ResolutionCommit>,
) {
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::ResolveFindingBatch {
            plan_id: plan_id.into(),
            resolutions,
        },
        &[],
    )
    .unwrap();
}

fn build_resolution_commit(
    resolver: &Keypair,
    plan_id: &str,
    finding_id: &str,
    outcome: &str,
) -> ResolutionCommit {
    let evidence_hash = "evd";
    let sig = resolver.sign(&canonical_resolution(
        plan_id,
        finding_id,
        outcome,
        evidence_hash,
    ));
    ResolutionCommit {
        plan_id: plan_id.into(),
        finding_id: finding_id.into(),
        outcome: outcome.into(),
        resolver_pubkey: resolver.pubkey.clone(),
        evidence_hash: evidence_hash.into(),
        signature: sig,
    }
}

fn build_finding_commit(
    filer: &Keypair,
    plan_id: &str,
    finding_id: &str,
    severity: &str,
    stake: u128,
) -> FindingCommit {
    let claim_hash = "h";
    let sig = filer.sign(&canonical_finding(
        plan_id, finding_id, severity, claim_hash, stake,
    ));
    FindingCommit {
        plan_id: plan_id.into(),
        finding_id: finding_id.into(),
        agent_pubkey: filer.pubkey.clone(),
        severity: severity.into(),
        claim_hash: claim_hash.into(),
        stake: stake.into(),
        signature: sig,
    }
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
    let sig = resolver.sign(&canonical_resolution(
        plan_id,
        finding_id,
        outcome,
        evidence_hash,
    ));
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

    /// PROPERTY D — rotation preserves the accountability trail (v0.5.4).
    /// After rotate(A → A'), the contract should preserve A's mutation
    /// surface even though A is retired: resolutions of findings A filed
    /// before retirement still credit/debit A's record (not A's
    /// successor). A' starts with rotation_floor balance + inherits A's
    /// tp_count + fp_count. The two records evolve independently from
    /// rotation forward.
    ///
    /// Operation sequence:
    ///   1. A registers (balance = INITIAL_BALANCE)
    ///   2. A commits F1, F2 (balance debited 2×stake)
    ///   3. PM resolves F1 (TP or FP per pre_outcome) — A's record updated
    ///   4. Rotate A → A' (A retired; A' gets rotation_floor + A's counts)
    ///   5. A' commits F3 (balance debited stake_a_prime)
    ///   6. PM resolves F2 (TP or FP per post_retire_outcome) — still
    ///      mutates A's record because F2 was filed by A
    ///   7. PM resolves F3 — mutates A''s record
    #[test]
    fn rotation_preserves_accountability_trail(
        stake_a in 1u128..=32,
        // A' starts at ROTATION_FLOOR (10), so its stake must fit in that
        // budget. Anything larger and the contract correctly rejects with
        // "insufficient stake balance" — not the property we're trying
        // to exercise here.
        stake_a_prime in 1u128..=(ROTATION_FLOOR - 1),
        pre_outcome in prop_oneof![Just("true_positive"), Just("false_positive")],
        post_retire_outcome in prop_oneof![Just("true_positive"), Just("false_positive")],
        a_prime_outcome in prop_oneof![Just("true_positive"), Just("false_positive")],
    ) {
        let (mut app, contract) = setup_app();
        let a = register(&mut app, &contract, "a", "adversary", 0x80);
        let pm = register(&mut app, &contract, "pm", "project-manager", 0x81);

        // A commits F1, F2.
        commit(&mut app, &contract, &a, "P-prop-D", "F1", "warning", stake_a);
        commit(&mut app, &contract, &a, "P-prop-D", "F2", "warning", stake_a);

        // Resolve F1 (pre-retirement).
        resolve(&mut app, &contract, &pm, "P-prop-D", "F1", pre_outcome);

        // Snapshot A's record before rotation.
        let a_pre_rotate = agent_record(&app, &contract, &a.pubkey);
        let a_balance_pre_rotate = a_pre_rotate.balance.u128();
        let a_tp_pre = a_pre_rotate.tp_count;
        let a_fp_pre = a_pre_rotate.fp_count;

        // Rotate A → A'. Use a new label (different from "a") to avoid
        // any label-collision edge cases.
        let a_prime_kp = Keypair::from_seed(0x82);
        rotate(&mut app, &contract, &a.pubkey, &a_prime_kp, "a-v2", "model-y");

        // A is retired, balance + counts preserved.
        let a_post_rotate = agent_record(&app, &contract, &a.pubkey);
        prop_assert!(a_post_rotate.retired_at.is_some(), "A should be retired");
        prop_assert_eq!(a_post_rotate.balance.u128(), a_balance_pre_rotate,
            "A's balance should not change at rotation moment");
        prop_assert_eq!(a_post_rotate.tp_count, a_tp_pre,
            "A's tp_count should not change at rotation moment");

        // A' has rotation_floor + inherited counts.
        let a_prime_record = agent_record(&app, &contract, &a_prime_kp.pubkey);
        prop_assert_eq!(a_prime_record.balance.u128(), ROTATION_FLOOR,
            "A' should start with rotation_floor");
        prop_assert_eq!(a_prime_record.tp_count, a_tp_pre,
            "A' should inherit A's tp_count");
        prop_assert_eq!(a_prime_record.fp_count, a_fp_pre,
            "A' should inherit A's fp_count");
        prop_assert!(a_prime_record.rotated_from.is_some(),
            "A' should record its rotated_from");

        // A' commits F3.
        commit(&mut app, &contract, &a_prime_kp, "P-prop-D", "F3", "warning",
               stake_a_prime);

        // Resolve F2 — this was filed by A. Mutates A's record.
        resolve(&mut app, &contract, &pm, "P-prop-D", "F2", post_retire_outcome);

        // Resolve F3 — filed by A'. Mutates A''s record.
        resolve(&mut app, &contract, &pm, "P-prop-D", "F3", a_prime_outcome);

        let a_final = agent_record(&app, &contract, &a.pubkey);
        let a_prime_final = agent_record(&app, &contract, &a_prime_kp.pubkey);

        // Invariant 1: A's counts changed by F2's outcome (not F3).
        let a_expected_tp_delta = (pre_outcome == "true_positive") as u64
            + (post_retire_outcome == "true_positive") as u64;
        let a_expected_fp_delta = (pre_outcome == "false_positive") as u64
            + (post_retire_outcome == "false_positive") as u64;
        prop_assert_eq!(a_final.tp_count, a_expected_tp_delta,
            "A's tp_count should reflect F1+F2 outcomes only");
        prop_assert_eq!(a_final.fp_count, a_expected_fp_delta,
            "A's fp_count should reflect F1+F2 outcomes only");

        // Invariant 2: A' counts changed by F3 outcome only (on top of
        // inherited A counts).
        let a_prime_expected_tp = a_tp_pre + (a_prime_outcome == "true_positive") as u64;
        let a_prime_expected_fp = a_fp_pre + (a_prime_outcome == "false_positive") as u64;
        prop_assert_eq!(a_prime_final.tp_count, a_prime_expected_tp,
            "A's tp_count should reflect inherited + F3 only");
        prop_assert_eq!(a_prime_final.fp_count, a_prime_expected_fp,
            "A's fp_count should reflect inherited + F3 only");

        // Invariant 3: A stays retired forever.
        prop_assert!(a_final.retired_at.is_some(), "A should still be retired");
    }

    /// PROPERTY E — batch commits are equivalent to N independent commits (v0.5.4).
    /// For any random batch of N findings on the same plan, the final
    /// per-agent state (balance, tp/fp counts) matches what you'd get by
    /// applying those N findings as N independent CommitFinding txs
    /// against a fresh App. Pins the invariant that batch processing
    /// doesn't have any hidden state-coupling or order-dependence beyond
    /// the individual operations.
    ///
    /// The batch test uses 1..=6 findings (small to keep proptest cheap)
    /// across 1..=3 filers (so we can hit "two findings from same agent"
    /// + "one finding from a different agent" interactions in the batch).
    #[test]
    fn batch_commit_equivalent_to_n_independent_commits(
        // Vector of (filer_idx 0..3, stake 1..=32). 1..=6 entries total.
        entries in prop::collection::vec((0u8..3, 1u128..=32), 1..=6),
    ) {
        // Run the batch path.
        let (mut app_batch, contract_batch) = setup_app();
        let filers_batch: Vec<Keypair> = (0..3)
            .map(|i| register(&mut app_batch, &contract_batch, &format!("f{}", i), "adversary", 0x90 + i))
            .collect();

        let commits: Vec<FindingCommit> = entries
            .iter()
            .enumerate()
            .map(|(i, (filer_idx, stake))| {
                build_finding_commit(
                    &filers_batch[*filer_idx as usize],
                    "P-prop-E",
                    &format!("F-{}", i),
                    "warning",
                    *stake,
                )
            })
            .collect();

        commit_batch(&mut app_batch, &contract_batch, "P-prop-E", commits.clone());

        let batch_balances: Vec<u128> = (0..3)
            .map(|i| agent_balance(&app_batch, &contract_batch, &filers_batch[i].pubkey))
            .collect();

        // Run the equivalent independent-commits path.
        let (mut app_solo, contract_solo) = setup_app();
        let filers_solo: Vec<Keypair> = (0..3)
            .map(|i| register(&mut app_solo, &contract_solo, &format!("f{}", i), "adversary", 0x90 + i as u8))
            .collect();

        for (i, (filer_idx, stake)) in entries.iter().enumerate() {
            commit(
                &mut app_solo,
                &contract_solo,
                &filers_solo[*filer_idx as usize],
                "P-prop-E",
                &format!("F-{}", i),
                "warning",
                *stake,
            );
        }

        let solo_balances: Vec<u128> = (0..3)
            .map(|i| agent_balance(&app_solo, &contract_solo, &filers_solo[i].pubkey))
            .collect();

        // Invariant: per-agent balances match.
        prop_assert_eq!(
            batch_balances, solo_balances,
            "batch and solo paths should produce identical per-agent balances"
        );
    }

    /// PROPERTY F — commit → resolve(StaleDuplicate | Indeterminate)
    /// roundtrip (v0.5.7).
    /// Both outcomes return the staked amount without paying reward and
    /// without changing tp/fp counts. Net delta from pre-commit baseline:
    /// zero. They differ semantically (stale = duplicate of prior;
    /// indeterminate = N rounds elapsed without resolution) but the
    /// reputation math is identical. This property pins both branches.
    #[test]
    fn commit_then_resolve_stale_or_indeterminate_is_a_noop_on_balance(
        stake in 1u128..=64,
        sev in severity_strategy(),
        outcome in prop_oneof![Just("stale_duplicate"), Just("indeterminate")],
    ) {
        let (mut app, contract) = setup_app();
        let adv = register(&mut app, &contract, "adv", "adversary", 0xA0);
        let pm = register(&mut app, &contract, "pm", "project-manager", 0xA1);

        let pre = agent_balance(&app, &contract, &adv.pubkey);
        let pre_record = agent_record(&app, &contract, &adv.pubkey);

        commit(&mut app, &contract, &adv, "P-prop-F", "F-prop-F", sev, stake);
        let mid = agent_balance(&app, &contract, &adv.pubkey);
        prop_assert_eq!(mid, pre - stake, "commit should debit stake");

        resolve(&mut app, &contract, &pm, "P-prop-F", "F-prop-F", outcome);

        let post = agent_balance(&app, &contract, &adv.pubkey);
        prop_assert_eq!(post, pre,
            "stale/indeterminate roundtrip should net to zero (stake returned, no reward)");

        // Pin tp/fp counts unchanged. Stale and Indeterminate must not
        // increment either counter — those are reserved for TP/FP.
        let post_record = agent_record(&app, &contract, &adv.pubkey);
        prop_assert_eq!(post_record.tp_count, pre_record.tp_count,
            "stale/indeterminate must not increment tp_count");
        prop_assert_eq!(post_record.fp_count, pre_record.fp_count,
            "stale/indeterminate must not increment fp_count");
    }

    /// PROPERTY G — resolve-batch equivalent to N independent resolves
    /// (v0.5.7). Mirror of Property E for the resolution path. For any
    /// random batch of N findings already committed to the same plan,
    /// the resulting per-agent state from ResolveFindingBatch matches
    /// applying those N as independent ResolveFinding txs. Pins the
    /// invariant that resolution batch processing has no hidden state
    /// coupling beyond individual ops.
    ///
    /// Strategy: pre-commit all N findings (so they exist on-chain to
    /// resolve), then split the resolutions between the batch and solo
    /// paths and compare final per-agent balances.
    #[test]
    fn resolve_batch_equivalent_to_n_independent_resolves(
        entries in prop::collection::vec(
            (0u8..3, 1u128..=32, prop_oneof![Just("true_positive"), Just("false_positive")]),
            1..=6,
        ),
    ) {
        // Run the batch path.
        let (mut app_batch, contract_batch) = setup_app();
        let filers_batch: Vec<Keypair> = (0..3)
            .map(|i| register(&mut app_batch, &contract_batch, &format!("f{}", i), "adversary", 0xB0 + i))
            .collect();
        let pm_batch = register(&mut app_batch, &contract_batch, "pm", "project-manager", 0xBA);

        // Pre-commit all findings on the batch app.
        for (i, (filer_idx, stake, _)) in entries.iter().enumerate() {
            commit(
                &mut app_batch,
                &contract_batch,
                &filers_batch[*filer_idx as usize],
                "P-prop-G",
                &format!("F-{}", i),
                "warning",
                *stake,
            );
        }
        // Resolve as one batch.
        let resolutions: Vec<ResolutionCommit> = entries
            .iter()
            .enumerate()
            .map(|(i, (_, _, outcome))| {
                build_resolution_commit(&pm_batch, "P-prop-G", &format!("F-{}", i), outcome)
            })
            .collect();
        resolve_batch(&mut app_batch, &contract_batch, "P-prop-G", resolutions);

        let batch_balances: Vec<u128> = (0..3)
            .map(|i| agent_balance(&app_batch, &contract_batch, &filers_batch[i].pubkey))
            .collect();

        // Run the equivalent solo-resolves path.
        let (mut app_solo, contract_solo) = setup_app();
        let filers_solo: Vec<Keypair> = (0..3)
            .map(|i| register(&mut app_solo, &contract_solo, &format!("f{}", i), "adversary", 0xB0 + i as u8))
            .collect();
        let pm_solo = register(&mut app_solo, &contract_solo, "pm", "project-manager", 0xBA);

        for (i, (filer_idx, stake, _)) in entries.iter().enumerate() {
            commit(
                &mut app_solo,
                &contract_solo,
                &filers_solo[*filer_idx as usize],
                "P-prop-G",
                &format!("F-{}", i),
                "warning",
                *stake,
            );
        }
        for (i, (_, _, outcome)) in entries.iter().enumerate() {
            resolve(
                &mut app_solo,
                &contract_solo,
                &pm_solo,
                "P-prop-G",
                &format!("F-{}", i),
                outcome,
            );
        }

        let solo_balances: Vec<u128> = (0..3)
            .map(|i| agent_balance(&app_solo, &contract_solo, &filers_solo[i].pubkey))
            .collect();

        prop_assert_eq!(
            batch_balances, solo_balances,
            "resolve-batch and solo-resolve paths should produce identical per-agent balances"
        );
    }
}
