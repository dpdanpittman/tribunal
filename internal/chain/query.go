package chain

import (
	"context"
	"encoding/json"
	"fmt"
)

// ReputationResp mirrors src/msg.rs::ReputationResp.
type ReputationResp struct {
	Pubkey  string  `json:"pubkey"`
	Label   *string `json:"label,omitempty"`
	Balance string  `json:"balance"`
	TPCount uint32  `json:"tp_count"`
	FPCount uint32  `json:"fp_count"`
}

// AgentRecord mirrors src/state.rs::AgentRecord on the query side.
type AgentRecord struct {
	Label         string  `json:"label"`
	ModelID       string  `json:"model_id"`
	Role          string  `json:"role"`
	Balance       string  `json:"balance"`
	TPCount       uint32  `json:"tp_count"`
	FPCount       uint32  `json:"fp_count"`
	CreatedAt     uint64  `json:"created_at"`
	RetiredAt     *uint64 `json:"retired_at,omitempty"`
	SupersededBy  *string `json:"superseded_by,omitempty"`
	RotatedFrom   *string `json:"rotated_from,omitempty"`
}

// AgentResp mirrors src/msg.rs::AgentResp.
type AgentResp struct {
	Pubkey string      `json:"pubkey"`
	Agent  AgentRecord `json:"agent"`
}

// FindingState mirrors src/state.rs::FindingState (for query results).
type FindingState struct {
	PlanID      string             `json:"plan_id"`
	FindingID   string             `json:"finding_id"`
	AgentPubkey string             `json:"agent_pubkey"`
	Severity    string             `json:"severity"`
	ClaimHash   string             `json:"claim_hash"`
	Stake       string             `json:"stake"`
	CommittedAt uint64             `json:"committed_at"`
	Resolution  *ResolutionRecord  `json:"resolution,omitempty"`
}

// ResolutionRecord mirrors src/state.rs::ResolutionRecord.
type ResolutionRecord struct {
	Outcome        string `json:"outcome"`
	ResolverPubkey string `json:"resolver_pubkey"`
	EvidenceHash   string `json:"evidence_hash"`
	ResolvedAt     uint64 `json:"resolved_at"`
	StakeReturned  string `json:"stake_returned"`
	Reward         string `json:"reward"`
}

// FindingResp mirrors src/msg.rs::FindingResp.
type FindingResp struct {
	Finding *FindingState `json:"finding,omitempty"`
}

// LeaderboardEntry mirrors src/msg.rs::LeaderboardEntry.
type LeaderboardEntry struct {
	Pubkey  string `json:"pubkey"`
	Label   string `json:"label"`
	Role    string `json:"role"`
	Balance string `json:"balance"`
	TPCount uint32 `json:"tp_count"`
	FPCount uint32 `json:"fp_count"`
}

// LeaderboardResp mirrors src/msg.rs::LeaderboardResp.
type LeaderboardResp struct {
	Entries []LeaderboardEntry `json:"entries"`
}

// ConfigResp mirrors src/msg.rs::ConfigResp.
type ConfigResp struct {
	Admin                   string `json:"admin"`
	InitialBalance          string `json:"initial_balance"`
	RotationFloor           string `json:"rotation_floor"`
	OutcomeRewardMultiplier string `json:"outcome_reward_multiplier"`
}

// Reputation queries the rolling balance for an agent pubkey
// ("ed25519:<hex>" form).
func (c *Client) Reputation(ctx context.Context, pubkey string) (*ReputationResp, error) {
	wire, err := PubkeyToWire(pubkey)
	if err != nil {
		return nil, err
	}
	raw, err := c.Query(ctx, &QueryMsg{Reputation: &QueryReputation{Pubkey: wire}})
	if err != nil {
		return nil, err
	}
	var resp ReputationResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse reputation: %w", err)
	}
	return &resp, nil
}

// Agent queries the full agent record by pubkey.
func (c *Client) Agent(ctx context.Context, pubkey string) (*AgentResp, error) {
	wire, err := PubkeyToWire(pubkey)
	if err != nil {
		return nil, err
	}
	raw, err := c.Query(ctx, &QueryMsg{Agent: &QueryAgent{Pubkey: wire}})
	if err != nil {
		return nil, err
	}
	var resp AgentResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse agent: %w", err)
	}
	return &resp, nil
}

// AgentByLabel queries the agent record using its human-readable label.
func (c *Client) AgentByLabel(ctx context.Context, label string) (*AgentResp, error) {
	raw, err := c.Query(ctx, &QueryMsg{AgentByLabel: &QueryAgentByLabel{Label: label}})
	if err != nil {
		return nil, err
	}
	var resp AgentResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse agent_by_label: %w", err)
	}
	return &resp, nil
}

// Finding queries a single finding state.
func (c *Client) Finding(ctx context.Context, planID, findingID string) (*FindingResp, error) {
	raw, err := c.Query(ctx, &QueryMsg{Finding: &QueryFinding{PlanID: planID, FindingID: findingID}})
	if err != nil {
		return nil, err
	}
	var resp FindingResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse finding: %w", err)
	}
	return &resp, nil
}

// Leaderboard queries the top agents by balance, optionally filtered by role.
func (c *Client) Leaderboard(ctx context.Context, role string, limit uint32) (*LeaderboardResp, error) {
	q := &QueryLeaderboard{}
	if role != "" {
		q.Role = &role
	}
	if limit > 0 {
		q.Limit = &limit
	}
	raw, err := c.Query(ctx, &QueryMsg{Leaderboard: q})
	if err != nil {
		return nil, err
	}
	var resp LeaderboardResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse leaderboard: %w", err)
	}
	return &resp, nil
}

// ContractConfig queries the contract's stored instantiate params.
func (c *Client) ContractConfig(ctx context.Context) (*ConfigResp, error) {
	raw, err := c.Query(ctx, &QueryMsg{Config: &struct{}{}})
	if err != nil {
		return nil, err
	}
	var resp ConfigResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &resp, nil
}
