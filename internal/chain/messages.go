package chain

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// Wire-format note: cosmwasm-std `Binary` deserializes from a base64
// string. Public keys and signatures are serialized accordingly. This
// matches what cosmwasm-schema generates from `pub struct FindingCommit
// { agent_pubkey: Binary, ... }`.

// ExecuteMsg is the discriminated union the contract's `execute` entry point
// accepts. Each variant matches a named enum case in src/msg.rs. Marshal
// with encoding/json — Go's struct-field JSON tags reproduce the
// serde(rename_all = "snake_case") shape exactly.
type ExecuteMsg struct {
	RegisterAgent       *RegisterAgentMsg `json:"register_agent,omitempty"`
	CommitFinding       *FindingCommit    `json:"commit_finding,omitempty"`
	CommitFindingBatch  *CommitBatchMsg   `json:"commit_finding_batch,omitempty"`
	ResolveFinding      *ResolutionCommit `json:"resolve_finding,omitempty"`
	ResolveFindingBatch *ResolveBatchMsg  `json:"resolve_finding_batch,omitempty"`
	RotateAgent         *RotateAgentMsg   `json:"rotate_agent,omitempty"`
}

// RegisterAgentMsg matches src/msg.rs::ExecuteMsg::RegisterAgent.
type RegisterAgentMsg struct {
	Pubkey         string  `json:"pubkey"`
	Label          string  `json:"label"`
	ModelID        string  `json:"model_id"`
	Role           string  `json:"role"`
	InitialBalance *string `json:"initial_balance,omitempty"`
}

// FindingCommit matches src/msg.rs::FindingCommit. Stake is a Uint128 on
// the contract side, so it serializes as a decimal string.
type FindingCommit struct {
	PlanID      string `json:"plan_id"`
	FindingID   string `json:"finding_id"`
	AgentPubkey string `json:"agent_pubkey"`
	Severity    string `json:"severity"`
	ClaimHash   string `json:"claim_hash"`
	Stake       string `json:"stake"`
	Signature   string `json:"signature"`
}

// CommitBatchMsg matches src/msg.rs::ExecuteMsg::CommitFindingBatch.
type CommitBatchMsg struct {
	PlanID   string          `json:"plan_id"`
	Findings []FindingCommit `json:"findings"`
}

// ResolutionCommit matches src/msg.rs::ResolutionCommit.
type ResolutionCommit struct {
	PlanID         string `json:"plan_id"`
	FindingID      string `json:"finding_id"`
	Outcome        string `json:"outcome"`
	ResolverPubkey string `json:"resolver_pubkey"`
	EvidenceHash   string `json:"evidence_hash"`
	Signature      string `json:"signature"`
}

// ResolveBatchMsg matches src/msg.rs::ExecuteMsg::ResolveFindingBatch.
type ResolveBatchMsg struct {
	PlanID      string             `json:"plan_id"`
	Resolutions []ResolutionCommit `json:"resolutions"`
}

// RotateAgentMsg matches src/msg.rs::ExecuteMsg::RotateAgent.
type RotateAgentMsg struct {
	OldPubkey  string `json:"old_pubkey"`
	NewPubkey  string `json:"new_pubkey"`
	NewLabel   string `json:"new_label"`
	NewModelID string `json:"new_model_id"`
	Reason     string `json:"reason"`
}

// BuildRegisterAgent assembles a RegisterAgent execute message from a
// local keypair + role + label. `initialBalance == 0` omits the field so
// the contract applies its default.
func BuildRegisterAgent(kp *agent.Keypair, label, modelID string, role agent.Role, initialBalance uint64) (*ExecuteMsg, error) {
	if kp == nil {
		return nil, fmt.Errorf("nil keypair")
	}
	if err := validateIDField("label", label, MaxLabelLen); err != nil {
		return nil, err
	}
	if err := validateIDField("model_id", modelID, MaxHashLen); err != nil {
		return nil, err
	}
	msg := &RegisterAgentMsg{
		Pubkey:  encodePubkey(kp.Public),
		Label:   label,
		ModelID: modelID,
		Role:    string(role),
	}
	if initialBalance > 0 {
		s := strconv.FormatUint(initialBalance, 10)
		msg.InitialBalance = &s
	}
	return &ExecuteMsg{RegisterAgent: msg}, nil
}

// BuildFindingCommit translates a local ledger.Finding into the signed
// on-chain commit. The on-chain signature is independent of the
// ledger-JSONL signature: the contract verifies the canonical bytes, the
// ledger verifies the JSON payload. Both bind to the same agent pubkey.
//
// Identifier fields (plan_id, finding_id, claim_hash) are validated to
// match the contract's `src/validate.rs` constraints so a malformed input
// fails before we even build a signature.
func BuildFindingCommit(f *ledger.Finding, kp *agent.Keypair) (*FindingCommit, error) {
	if f == nil {
		return nil, fmt.Errorf("nil finding")
	}
	if kp == nil {
		return nil, fmt.Errorf("nil keypair")
	}
	if err := validateIDField("plan_id", f.PlanID, MaxIDLen); err != nil {
		return nil, err
	}
	if err := validateIDField("finding_id", f.FindingID, MaxIDLen); err != nil {
		return nil, err
	}
	if err := validateIDField("claim_hash", f.ClaimHash, MaxHashLen); err != nil {
		return nil, err
	}
	expectedPub := agent.FormatPubkey(kp.Public)
	if expectedPub != f.AgentPubkey {
		return nil, fmt.Errorf("commit: keypair pubkey %q does not match finding agent_pubkey %q", expectedPub, f.AgentPubkey)
	}
	// Stake is rendered as decimal so it matches `Uint128` wire form
	// without going through a fixed-width integer cast.
	stake := strconv.FormatUint(uint64(f.Stake), 10)
	msgBytes := CanonicalFindingMessage(f.PlanID, f.FindingID, string(f.Severity), f.ClaimHash, stake)
	sig := ed25519.Sign(kp.Private, msgBytes)
	return &FindingCommit{
		PlanID:      f.PlanID,
		FindingID:   f.FindingID,
		AgentPubkey: encodeBinary(kp.Public),
		Severity:    string(f.Severity),
		ClaimHash:   f.ClaimHash,
		Stake:       stake,
		Signature:   encodeBinary(sig),
	}, nil
}

// BuildResolutionCommit translates a local ledger.Resolution into the
// signed on-chain commit.
func BuildResolutionCommit(r *ledger.Resolution, kp *agent.Keypair) (*ResolutionCommit, error) {
	if r == nil {
		return nil, fmt.Errorf("nil resolution")
	}
	if kp == nil {
		return nil, fmt.Errorf("nil keypair")
	}
	if err := validateIDField("plan_id", r.PlanID, MaxIDLen); err != nil {
		return nil, err
	}
	if err := validateIDField("finding_id", r.FindingID, MaxIDLen); err != nil {
		return nil, err
	}
	if err := validateIDField("evidence_hash", r.EvidenceHash, MaxHashLen); err != nil {
		return nil, err
	}
	expectedPub := agent.FormatPubkey(kp.Public)
	if expectedPub != r.ResolverPubkey {
		return nil, fmt.Errorf("commit: keypair pubkey %q does not match resolution resolver_pubkey %q", expectedPub, r.ResolverPubkey)
	}
	msgBytes := CanonicalResolutionMessage(r.PlanID, r.FindingID, string(r.Outcome), r.EvidenceHash)
	sig := ed25519.Sign(kp.Private, msgBytes)
	return &ResolutionCommit{
		PlanID:         r.PlanID,
		FindingID:      r.FindingID,
		Outcome:        string(r.Outcome),
		ResolverPubkey: encodeBinary(kp.Public),
		EvidenceHash:   r.EvidenceHash,
		Signature:      encodeBinary(sig),
	}, nil
}

// QueryMsg is the contract's query union. Same wire convention as ExecuteMsg.
type QueryMsg struct {
	Reputation   *QueryReputation   `json:"reputation,omitempty"`
	Agent        *QueryAgent        `json:"agent,omitempty"`
	AgentByLabel *QueryAgentByLabel `json:"agent_by_label,omitempty"`
	Finding      *QueryFinding      `json:"finding,omitempty"`
	Leaderboard  *QueryLeaderboard  `json:"leaderboard,omitempty"`
	Config       *struct{}          `json:"config,omitempty"`
}

type QueryReputation struct {
	Pubkey string `json:"pubkey"`
}
type QueryAgent struct {
	Pubkey string `json:"pubkey"`
}
type QueryAgentByLabel struct {
	Label string `json:"label"`
}
type QueryFinding struct {
	PlanID    string `json:"plan_id"`
	FindingID string `json:"finding_id"`
}
type QueryLeaderboard struct {
	Role  *string `json:"role,omitempty"`
	Limit *uint32 `json:"limit,omitempty"`
}

// encodeBinary returns a cosmwasm-std `Binary` wire value: standard base64.
func encodeBinary(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// encodePubkey accepts either a raw [32]byte pubkey or an existing "ed25519:..."
// string and returns the contract's wire form (base64 of raw 32 bytes).
func encodePubkey(pub ed25519.PublicKey) string {
	return encodeBinary([]byte(pub))
}

// DecodePubkeyBinary decodes a contract-wire pubkey back to canonical
// "ed25519:<hex>". Used when reading query responses.
func DecodePubkeyBinary(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("pubkey b64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return "", fmt.Errorf("pubkey length: want %d, got %d", ed25519.PublicKeySize, len(raw))
	}
	return agent.PubkeyPrefix + hex.EncodeToString(raw), nil
}

// PubkeyToWire converts a canonical "ed25519:<hex>" pubkey to the contract
// wire form (base64).
func PubkeyToWire(canonical string) (string, error) {
	pub, err := agent.ParsePubkey(canonical)
	if err != nil {
		return "", err
	}
	return encodeBinary([]byte(pub)), nil
}

// trimLeading0x is a small helper used by clients that may receive hex
// strings with an optional 0x prefix. Kept here so all wire-format helpers
// live in one file.
func trimLeading0x(s string) string {
	return strings.TrimPrefix(s, "0x")
}

var _ = trimLeading0x // reserved for future hex inputs
