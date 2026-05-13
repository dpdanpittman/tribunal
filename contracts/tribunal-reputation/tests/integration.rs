//! Integration tests for tribunal-reputation. Exercises every execute /
//! query path end-to-end against a cw-multi-test app.

use cosmwasm_std::{Addr, Binary, Uint128};
use cw_multi_test::{App, AppBuilder, ContractWrapper, Executor};

use ed25519_dalek::{Signer, SigningKey, VerifyingKey, SECRET_KEY_LENGTH};

use tribunal_reputation::contract;
use tribunal_reputation::msg::{
    AgentResp, ConfigResp, ExecuteMsg, FindingCommit, FindingResp, InstantiateMsg,
    LeaderboardResp, QueryMsg, ReputationResp, ResolutionCommit,
};

const ADMIN: &str = "admin";

/// Helper bundling a SigningKey and its public bytes.
struct Keypair {
    signing: SigningKey,
    pubkey: Binary,
}

impl Keypair {
    fn from_seed(seed_byte: u8) -> Self {
        let mut seed = [0u8; SECRET_KEY_LENGTH];
        for b in &mut seed {
            *b = seed_byte;
        }
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

/// Build a fresh App + instantiate the contract; return (app, contract_addr).
fn setup() -> (App, Addr) {
    let mut app = AppBuilder::new().build(|_, _, _| {});

    let code = ContractWrapper::new(contract::execute, contract::instantiate, contract::query);
    let code_id = app.store_code(Box::new(code));

    let admin = Addr::unchecked(ADMIN);
    let contract_addr = app
        .instantiate_contract(
            code_id,
            admin.clone(),
            &InstantiateMsg {
                admin: None,
                initial_balance: Some(Uint128::new(100)),
                rotation_floor: Some(Uint128::new(10)),
                outcome_reward_multiplier: Some(Uint128::new(2)),
            },
            &[],
            "tribunal-reputation",
            None,
        )
        .unwrap();
    (app, contract_addr)
}

/// Convenience: register an agent and return its keypair.
fn register(app: &mut App, contract: &Addr, label: &str, role: &str, seed: u8) -> Keypair {
    let kp = Keypair::from_seed(seed);
    let msg = ExecuteMsg::RegisterAgent {
        pubkey: kp.pubkey.clone(),
        label: label.to_string(),
        model_id: "model-x".to_string(),
        role: role.to_string(),
        initial_balance: None,
    };
    app.execute_contract(Addr::unchecked(ADMIN), contract.clone(), &msg, &[])
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

fn canonical_resolution(plan_id: &str, finding_id: &str, outcome: &str, evidence_hash: &str) -> Vec<u8> {
    format!(
        "TRIBUNAL_RESOLUTION|{}|{}|{}|{}",
        plan_id, finding_id, outcome, evidence_hash
    )
    .into_bytes()
}

#[test]
fn instantiate_records_config() {
    let (app, contract) = setup();
    let cfg: ConfigResp = app
        .wrap()
        .query_wasm_smart(&contract, &QueryMsg::Config {})
        .unwrap();
    assert_eq!(cfg.initial_balance, Uint128::new(100));
    assert_eq!(cfg.rotation_floor, Uint128::new(10));
    assert_eq!(cfg.outcome_reward_multiplier, Uint128::new(2));
}

#[test]
fn register_agent_happy_path() {
    let (mut app, contract) = setup();
    let kp = register(&mut app, &contract, "alice", "adversary", 0x01);
    let resp: AgentResp = app
        .wrap()
        .query_wasm_smart(&contract, &QueryMsg::Agent { pubkey: kp.pubkey })
        .unwrap();
    assert_eq!(resp.agent.label, "alice");
    assert_eq!(resp.agent.balance, Uint128::new(100));
    assert!(resp.agent.retired_at.is_none());
}

#[test]
fn register_agent_rejects_duplicate_pubkey() {
    let (mut app, contract) = setup();
    let kp = Keypair::from_seed(0x02);
    let msg1 = ExecuteMsg::RegisterAgent {
        pubkey: kp.pubkey.clone(),
        label: "first".into(),
        model_id: "m".into(),
        role: "adversary".into(),
        initial_balance: None,
    };
    app.execute_contract(Addr::unchecked(ADMIN), contract.clone(), &msg1, &[])
        .unwrap();
    let msg2 = ExecuteMsg::RegisterAgent {
        pubkey: kp.pubkey,
        label: "second".into(),
        model_id: "m".into(),
        role: "adversary".into(),
        initial_balance: None,
    };
    let err = app
        .execute_contract(Addr::unchecked(ADMIN), contract, &msg2, &[])
        .unwrap_err();
    let msg = err.root_cause().to_string();
    assert!(msg.contains("already registered"), "got: {}", msg);
}

#[test]
fn register_agent_rejects_duplicate_label() {
    let (mut app, contract) = setup();
    let _ = register(&mut app, &contract, "shared", "adversary", 0x03);
    let kp2 = Keypair::from_seed(0x04);
    let msg = ExecuteMsg::RegisterAgent {
        pubkey: kp2.pubkey,
        label: "shared".into(),
        model_id: "m".into(),
        role: "adversary".into(),
        initial_balance: None,
    };
    let err = app
        .execute_contract(Addr::unchecked(ADMIN), contract, &msg, &[])
        .unwrap_err();
    let msg = err.root_cause().to_string();
    assert!(msg.contains("label"), "got: {}", msg);
}

#[test]
fn register_agent_rejects_invalid_role() {
    let (mut app, contract) = setup();
    let kp = Keypair::from_seed(0x05);
    let msg = ExecuteMsg::RegisterAgent {
        pubkey: kp.pubkey,
        label: "x".into(),
        model_id: "m".into(),
        role: "not-a-role".into(),
        initial_balance: None,
    };
    let err = app
        .execute_contract(Addr::unchecked(ADMIN), contract, &msg, &[])
        .unwrap_err();
    let msg = err.root_cause().to_string();
    assert!(msg.to_lowercase().contains("role"), "got: {}", msg);
}

#[test]
fn commit_finding_happy_path_deducts_stake() {
    let (mut app, contract) = setup();
    let kp = register(&mut app, &contract, "adv", "adversary", 0x10);

    let plan_id = "P-42";
    let finding_id = "F-001";
    let severity = "critical";
    let claim_hash = "sha256:abc";
    let stake: u128 = 8;
    let sig = kp.sign(&canonical_finding(plan_id, finding_id, severity, claim_hash, stake));

    let msg = ExecuteMsg::CommitFinding(FindingCommit {
        plan_id: plan_id.into(),
        finding_id: finding_id.into(),
        agent_pubkey: kp.pubkey.clone(),
        severity: severity.into(),
        claim_hash: claim_hash.into(),
        stake: stake.into(),
        signature: sig,
    });
    app.execute_contract(Addr::unchecked(ADMIN), contract.clone(), &msg, &[])
        .unwrap();

    let rep: ReputationResp = app
        .wrap()
        .query_wasm_smart(&contract, &QueryMsg::Reputation { pubkey: kp.pubkey })
        .unwrap();
    assert_eq!(rep.balance, Uint128::new(100 - 8));
}

#[test]
fn commit_finding_rejects_bad_signature() {
    let (mut app, contract) = setup();
    let kp = register(&mut app, &contract, "adv", "adversary", 0x11);
    let imposter = Keypair::from_seed(0x99);

    // Imposter signs a message claiming to be the registered agent.
    let plan_id = "P-x";
    let finding_id = "F-x";
    let severity = "critical";
    let claim_hash = "h";
    let stake: u128 = 8;
    let bad_sig = imposter.sign(&canonical_finding(plan_id, finding_id, severity, claim_hash, stake));

    let msg = ExecuteMsg::CommitFinding(FindingCommit {
        plan_id: plan_id.into(),
        finding_id: finding_id.into(),
        agent_pubkey: kp.pubkey.clone(),
        severity: severity.into(),
        claim_hash: claim_hash.into(),
        stake: stake.into(),
        signature: bad_sig,
    });
    let err = app
        .execute_contract(Addr::unchecked(ADMIN), contract, &msg, &[])
        .unwrap_err();
    let msg = err.root_cause().to_string();
    assert!(msg.to_lowercase().contains("signature"), "got: {}", msg);
}

#[test]
fn commit_finding_rejects_duplicate() {
    let (mut app, contract) = setup();
    let kp = register(&mut app, &contract, "adv", "adversary", 0x12);
    let plan_id = "P-1";
    let finding_id = "F-1";
    let severity = "warning";
    let claim_hash = "h";
    let stake: u128 = 4;
    let sig = kp.sign(&canonical_finding(plan_id, finding_id, severity, claim_hash, stake));
    let f = FindingCommit {
        plan_id: plan_id.into(),
        finding_id: finding_id.into(),
        agent_pubkey: kp.pubkey.clone(),
        severity: severity.into(),
        claim_hash: claim_hash.into(),
        stake: stake.into(),
        signature: sig,
    };
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::CommitFinding(f.clone()),
        &[],
    )
    .unwrap();
    let err = app
        .execute_contract(
            Addr::unchecked(ADMIN),
            contract,
            &ExecuteMsg::CommitFinding(f),
            &[],
        )
        .unwrap_err();
    let msg = err.root_cause().to_string();
    assert!(msg.to_lowercase().contains("already committed") || msg.to_lowercase().contains("already_committed"), "got: {}", msg);
}

#[test]
fn resolve_true_positive_returns_stake_plus_reward() {
    let (mut app, contract) = setup();
    let adv = register(&mut app, &contract, "adv", "adversary", 0x20);
    let pm = register(&mut app, &contract, "pm", "project-manager", 0x21);

    let plan_id = "P-9";
    let finding_id = "F-9";
    let severity = "critical";
    let claim_hash = "h";
    let stake: u128 = 8;
    let sig = adv.sign(&canonical_finding(plan_id, finding_id, severity, claim_hash, stake));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::CommitFinding(FindingCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            agent_pubkey: adv.pubkey.clone(),
            severity: severity.into(),
            claim_hash: claim_hash.into(),
            stake: stake.into(),
            signature: sig,
        }),
        &[],
    )
    .unwrap();

    // PM resolves as TP.
    let outcome = "true_positive";
    let evidence_hash = "evd";
    let pm_sig = pm.sign(&canonical_resolution(plan_id, finding_id, outcome, evidence_hash));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::ResolveFinding(ResolutionCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            outcome: outcome.into(),
            resolver_pubkey: pm.pubkey.clone(),
            evidence_hash: evidence_hash.into(),
            signature: pm_sig,
        }),
        &[],
    )
    .unwrap();

    // Balance: 100 - 8 (commit) + 8 (return) + 16 (2x reward) = 116.
    let rep: ReputationResp = app
        .wrap()
        .query_wasm_smart(&contract, &QueryMsg::Reputation { pubkey: adv.pubkey })
        .unwrap();
    assert_eq!(rep.balance, Uint128::new(116));
    assert_eq!(rep.tp_count, 1);
    assert_eq!(rep.fp_count, 0);
}

#[test]
fn resolve_false_positive_slashes_stake() {
    let (mut app, contract) = setup();
    let adv = register(&mut app, &contract, "adv", "adversary", 0x30);
    let pm = register(&mut app, &contract, "pm", "project-manager", 0x31);

    let plan_id = "P-5";
    let finding_id = "F-5";
    let severity = "warning";
    let claim_hash = "h";
    let stake: u128 = 4;
    let sig = adv.sign(&canonical_finding(plan_id, finding_id, severity, claim_hash, stake));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::CommitFinding(FindingCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            agent_pubkey: adv.pubkey.clone(),
            severity: severity.into(),
            claim_hash: claim_hash.into(),
            stake: stake.into(),
            signature: sig,
        }),
        &[],
    )
    .unwrap();

    let outcome = "false_positive";
    let evidence_hash = "ev";
    let pm_sig = pm.sign(&canonical_resolution(plan_id, finding_id, outcome, evidence_hash));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::ResolveFinding(ResolutionCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            outcome: outcome.into(),
            resolver_pubkey: pm.pubkey.clone(),
            evidence_hash: evidence_hash.into(),
            signature: pm_sig,
        }),
        &[],
    )
    .unwrap();

    // Stake stays slashed; balance ends at 100 - 4 = 96.
    let rep: ReputationResp = app
        .wrap()
        .query_wasm_smart(&contract, &QueryMsg::Reputation { pubkey: adv.pubkey })
        .unwrap();
    assert_eq!(rep.balance, Uint128::new(96));
    assert_eq!(rep.fp_count, 1);
}

#[test]
fn unauthorized_resolver_rejected() {
    let (mut app, contract) = setup();
    let adv = register(&mut app, &contract, "adv", "adversary", 0x40);
    let imposter = register(&mut app, &contract, "imposter", "implementer", 0x41);

    let plan_id = "P-7";
    let finding_id = "F-7";
    let severity = "critical";
    let claim_hash = "h";
    let stake: u128 = 8;
    let sig = adv.sign(&canonical_finding(plan_id, finding_id, severity, claim_hash, stake));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::CommitFinding(FindingCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            agent_pubkey: adv.pubkey.clone(),
            severity: severity.into(),
            claim_hash: claim_hash.into(),
            stake: stake.into(),
            signature: sig,
        }),
        &[],
    )
    .unwrap();

    let outcome = "true_positive";
    let evidence_hash = "ev";
    let bad_sig = imposter.sign(&canonical_resolution(plan_id, finding_id, outcome, evidence_hash));
    let err = app
        .execute_contract(
            Addr::unchecked(ADMIN),
            contract,
            &ExecuteMsg::ResolveFinding(ResolutionCommit {
                plan_id: plan_id.into(),
                finding_id: finding_id.into(),
                outcome: outcome.into(),
                resolver_pubkey: imposter.pubkey,
                evidence_hash: evidence_hash.into(),
                signature: bad_sig,
            }),
            &[],
        )
        .unwrap_err();
    let msg = err.root_cause().to_string();
    assert!(msg.to_lowercase().contains("unauthorized") || msg.to_lowercase().contains("resolver"), "got: {}", msg);
}

#[test]
fn batch_commit_and_resolve_per_plan() {
    let (mut app, contract) = setup();
    let adv = register(&mut app, &contract, "adv", "adversary", 0x50);
    let pm = register(&mut app, &contract, "pm", "project-manager", 0x51);

    let plan_id = "P-batch";
    let mut findings = vec![];
    for i in 0..3 {
        let fid = format!("F-{i}");
        let severity = "warning";
        let claim_hash = "h";
        let stake: u128 = 4;
        let sig = adv.sign(&canonical_finding(plan_id, &fid, severity, claim_hash, stake));
        findings.push(FindingCommit {
            plan_id: plan_id.into(),
            finding_id: fid,
            agent_pubkey: adv.pubkey.clone(),
            severity: severity.into(),
            claim_hash: claim_hash.into(),
            stake: stake.into(),
            signature: sig,
        });
    }
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::CommitFindingBatch {
            plan_id: plan_id.into(),
            findings: findings.clone(),
        },
        &[],
    )
    .unwrap();

    // 3 stakes of 4 deducted → 100 - 12 = 88.
    let rep: ReputationResp = app
        .wrap()
        .query_wasm_smart(&contract, &QueryMsg::Reputation { pubkey: adv.pubkey.clone() })
        .unwrap();
    assert_eq!(rep.balance, Uint128::new(88));

    // Resolve all three as TP.
    let outcome = "true_positive";
    let mut resolutions = vec![];
    for f in &findings {
        let sig = pm.sign(&canonical_resolution(&f.plan_id, &f.finding_id, outcome, "ev"));
        resolutions.push(ResolutionCommit {
            plan_id: f.plan_id.clone(),
            finding_id: f.finding_id.clone(),
            outcome: outcome.into(),
            resolver_pubkey: pm.pubkey.clone(),
            evidence_hash: "ev".into(),
            signature: sig,
        });
    }
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

    // 3 TPs at stake=4, reward multiplier=2: each returns 4 + 8 = 12; total +36.
    // Balance: 88 + 36 = 124.
    let rep: ReputationResp = app
        .wrap()
        .query_wasm_smart(&contract, &QueryMsg::Reputation { pubkey: adv.pubkey })
        .unwrap();
    assert_eq!(rep.balance, Uint128::new(124));
    assert_eq!(rep.tp_count, 3);
}

#[test]
fn rotate_preserves_history_and_resets_balance() {
    let (mut app, contract) = setup();
    let old_kp = register(&mut app, &contract, "v1", "adversary", 0x60);
    // Give it some history first.
    let pm = register(&mut app, &contract, "pm", "project-manager", 0x61);
    let plan_id = "P";
    let finding_id = "F";
    let severity = "critical";
    let sig = old_kp.sign(&canonical_finding(plan_id, finding_id, severity, "h", 8));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::CommitFinding(FindingCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            agent_pubkey: old_kp.pubkey.clone(),
            severity: severity.into(),
            claim_hash: "h".into(),
            stake: Uint128::new(8),
            signature: sig,
        }),
        &[],
    )
    .unwrap();
    let pm_sig = pm.sign(&canonical_resolution(plan_id, finding_id, "true_positive", "ev"));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::ResolveFinding(ResolutionCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            outcome: "true_positive".into(),
            resolver_pubkey: pm.pubkey.clone(),
            evidence_hash: "ev".into(),
            signature: pm_sig,
        }),
        &[],
    )
    .unwrap();

    // Now rotate v1 → v2.
    let new_kp = Keypair::from_seed(0x62);
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::RotateAgent {
            old_pubkey: old_kp.pubkey.clone(),
            new_pubkey: new_kp.pubkey.clone(),
            new_label: "v2".into(),
            new_model_id: "claude-opus-5".into(),
            reason: "model upgrade".into(),
        },
        &[],
    )
    .unwrap();

    let old_resp: AgentResp = app
        .wrap()
        .query_wasm_smart(&contract, &QueryMsg::Agent { pubkey: old_kp.pubkey })
        .unwrap();
    assert!(old_resp.agent.retired_at.is_some(), "old agent should be retired");
    assert!(old_resp.agent.superseded_by.is_some());

    let new_resp: AgentResp = app
        .wrap()
        .query_wasm_smart(&contract, &QueryMsg::Agent { pubkey: new_kp.pubkey })
        .unwrap();
    // New balance is rotation_floor (10). TP history carried over (1).
    assert_eq!(new_resp.agent.balance, Uint128::new(10));
    assert_eq!(new_resp.agent.tp_count, 1);
    assert!(new_resp.agent.rotated_from.is_some());
}

#[test]
fn leaderboard_orders_by_balance_descending() {
    let (mut app, contract) = setup();
    let high = register(&mut app, &contract, "high", "adversary", 0x70);
    let mid = register(&mut app, &contract, "mid", "adversary", 0x71);
    let low = register(&mut app, &contract, "low", "adversary", 0x72);
    let pm = register(&mut app, &contract, "pm", "project-manager", 0x73);

    // Award `high` a TP (net +16), leave `mid` at 100, slash `low` by 4.
    let mut commit_and_resolve = |kp: &Keypair, plan_id: &str, finding_id: &str, severity: &str, stake: u128, outcome: &str| {
        let sig = kp.sign(&canonical_finding(plan_id, finding_id, severity, "h", stake));
        app.execute_contract(
            Addr::unchecked(ADMIN),
            contract.clone(),
            &ExecuteMsg::CommitFinding(FindingCommit {
                plan_id: plan_id.into(),
                finding_id: finding_id.into(),
                agent_pubkey: kp.pubkey.clone(),
                severity: severity.into(),
                claim_hash: "h".into(),
                stake: stake.into(),
                signature: sig,
            }),
            &[],
        )
        .unwrap();
        let pm_sig = pm.sign(&canonical_resolution(plan_id, finding_id, outcome, "ev"));
        app.execute_contract(
            Addr::unchecked(ADMIN),
            contract.clone(),
            &ExecuteMsg::ResolveFinding(ResolutionCommit {
                plan_id: plan_id.into(),
                finding_id: finding_id.into(),
                outcome: outcome.into(),
                resolver_pubkey: pm.pubkey.clone(),
                evidence_hash: "ev".into(),
                signature: pm_sig,
            }),
            &[],
        )
        .unwrap();
    };
    commit_and_resolve(&high, "P-1", "F-1", "critical", 8, "true_positive");
    commit_and_resolve(&low, "P-2", "F-2", "warning", 4, "false_positive");
    // mid: untouched.
    let _ = mid;

    let lb: LeaderboardResp = app
        .wrap()
        .query_wasm_smart(
            &contract,
            &QueryMsg::Leaderboard {
                role: Some("adversary".into()),
                limit: Some(10),
            },
        )
        .unwrap();
    // high: 100 - 8 + 8 + 16 = 116
    // mid:  100
    // low:  100 - 4 = 96
    assert_eq!(lb.entries.len(), 3);
    assert_eq!(lb.entries[0].label, "high");
    assert_eq!(lb.entries[0].balance, Uint128::new(116));
    assert_eq!(lb.entries[1].label, "mid");
    assert_eq!(lb.entries[1].balance, Uint128::new(100));
    assert_eq!(lb.entries[2].label, "low");
    assert_eq!(lb.entries[2].balance, Uint128::new(96));
}

#[test]
fn finding_query_returns_committed_state() {
    let (mut app, contract) = setup();
    let kp = register(&mut app, &contract, "adv", "adversary", 0x80);
    let plan_id = "P-find";
    let finding_id = "F-find";
    let severity = "warning";
    let stake: u128 = 4;
    let sig = kp.sign(&canonical_finding(plan_id, finding_id, severity, "h", stake));
    app.execute_contract(
        Addr::unchecked(ADMIN),
        contract.clone(),
        &ExecuteMsg::CommitFinding(FindingCommit {
            plan_id: plan_id.into(),
            finding_id: finding_id.into(),
            agent_pubkey: kp.pubkey,
            severity: severity.into(),
            claim_hash: "h".into(),
            stake: stake.into(),
            signature: sig,
        }),
        &[],
    )
    .unwrap();

    let resp: FindingResp = app
        .wrap()
        .query_wasm_smart(
            &contract,
            &QueryMsg::Finding {
                plan_id: plan_id.into(),
                finding_id: finding_id.into(),
            },
        )
        .unwrap();
    assert!(resp.finding.is_some());
    let f = resp.finding.unwrap();
    assert_eq!(f.stake, Uint128::new(stake));
    assert!(f.resolution.is_none());
}
