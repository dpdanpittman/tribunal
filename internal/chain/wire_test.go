package chain

import (
	"encoding/json"
	"testing"
)

// These tests anchor the JSON wire format against hand-crafted fixtures
// that mimic what the deployed contract emits. They exist precisely
// because cw-multi-test bypasses the JSON boundary — the v0.3.0 audit
// found that wire-format mismatches between Rust and Go were invisible
// to the contract test suite. If you change a response shape on the
// Rust side, update these fixtures and verify they still unmarshal.

func TestWire_ReputationResp_RetiredAgent(t *testing.T) {
	fixture := []byte(`{
		"pubkey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"label": "claude-adversary",
		"balance": "116",
		"tp_count": 5,
		"fp_count": 1,
		"retired": false
	}`)
	var resp ReputationResp
	if err := json.Unmarshal(fixture, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Balance != "116" {
		t.Errorf("balance: got %q, want %q", resp.Balance, "116")
	}
	if resp.TPCount != 5 || resp.FPCount != 1 {
		t.Errorf("counts: %d/%d, want 5/1", resp.TPCount, resp.FPCount)
	}
	if resp.Label == nil || *resp.Label != "claude-adversary" {
		t.Errorf("label: %+v", resp.Label)
	}
	if resp.Retired {
		t.Error("retired should be false")
	}
}

func TestWire_AgentResp_WithRotationHistory(t *testing.T) {
	fixture := []byte(`{
		"pubkey": "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA=",
		"agent": {
			"label": "claude-adversary-v2",
			"model_id": "claude-opus-5",
			"role": "adversary",
			"balance": "10",
			"tp_count": 5,
			"fp_count": 1,
			"created_at": "1715000000000000000",
			"rotated_from": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		}
	}`)
	var resp AgentResp
	if err := json.Unmarshal(fixture, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Agent.Balance != "10" {
		t.Errorf("balance: got %q, want %q", resp.Agent.Balance, "10")
	}
	if resp.Agent.CreatedAt != "1715000000000000000" {
		t.Errorf("created_at: got %q (expected nanos-as-string)", resp.Agent.CreatedAt)
	}
	if resp.Agent.RotatedFrom == nil {
		t.Error("rotated_from should be set")
	}
	if resp.Agent.RetiredAt != nil {
		t.Error("retired_at should be nil for active agent")
	}
}

func TestWire_FindingResp_ResolvedTruePositive(t *testing.T) {
	// This is the path that v0.3.0 broke — the contract returned a single
	// `reward_applied` field but the Go side declared `stake_returned` +
	// `reward`. With Uint128 + the split fields, the wire matches.
	fixture := []byte(`{
		"finding": {
			"plan_id": "P-42",
			"finding_id": "F-001",
			"agent_pubkey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			"severity": "critical",
			"claim_hash": "sha256:cafebabe",
			"stake": "8",
			"committed_at": "1715000000000000000",
			"resolution": {
				"outcome": "true_positive",
				"resolver_pubkey": "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCA=",
				"evidence_hash": "sha256:evidence",
				"resolved_at": "1715000003000000000",
				"stake_returned": "8",
				"reward": "16"
			}
		}
	}`)
	var resp FindingResp
	if err := json.Unmarshal(fixture, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Finding == nil {
		t.Fatal("finding should be set")
	}
	if resp.Finding.Stake != "8" {
		t.Errorf("stake: got %q, want %q", resp.Finding.Stake, "8")
	}
	r := resp.Finding.Resolution
	if r == nil {
		t.Fatal("resolution should be set")
	}
	if r.StakeReturned != "8" || r.Reward != "16" {
		t.Errorf("stake_returned/reward: got %q/%q, want 8/16", r.StakeReturned, r.Reward)
	}
}

func TestWire_FindingResp_Empty(t *testing.T) {
	fixture := []byte(`{"finding": null}`)
	var resp FindingResp
	if err := json.Unmarshal(fixture, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Finding != nil {
		t.Errorf("finding should be nil, got %+v", resp.Finding)
	}
}

func TestWire_LeaderboardResp(t *testing.T) {
	fixture := []byte(`{
		"entries": [
			{"pubkey":"AAA=","label":"a","role":"adversary","balance":"116","tp_count":3,"fp_count":0},
			{"pubkey":"BBB=","label":"b","role":"adversary","balance":"100","tp_count":1,"fp_count":1}
		]
	}`)
	var resp LeaderboardResp
	if err := json.Unmarshal(fixture, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(resp.Entries))
	}
	if resp.Entries[0].Balance != "116" || resp.Entries[1].Balance != "100" {
		t.Errorf("balance ordering: %+v", resp.Entries)
	}
}

func TestWire_ConfigResp(t *testing.T) {
	fixture := []byte(`{
		"admin": "xion1abc",
		"initial_balance": "100",
		"rotation_floor": "10",
		"outcome_reward_multiplier": "2"
	}`)
	var resp ConfigResp
	if err := json.Unmarshal(fixture, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.InitialBalance != "100" || resp.RotationFloor != "10" || resp.OutcomeRewardMultiplier != "2" {
		t.Errorf("config: %+v", resp)
	}
}
