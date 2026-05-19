// Package ledger implements Tribunal's append-only signed ledger of
// findings and resolutions, plus the rolling reputation calculation built
// on top of it.
package ledger

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
)

// Severity is the reviewer-assigned importance of a finding. The three
// values are deliberately coarse to discourage hair-splitting; severity
// gates and reputation math both lean on this enum.
type Severity string

const (
	SeverityCritical   Severity = "critical"
	SeverityWarning    Severity = "warning"
	SeveritySuggestion Severity = "suggestion"
)

// Weight returns the multiplier used in reputation calculation. Higher
// severity = more reputation impact (in either direction).
func (s Severity) Weight() float64 {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityWarning:
		return 2
	case SeveritySuggestion:
		return 1
	default:
		return 0
	}
}

// DefaultStake returns the reputation amount staked when filing a finding
// of this severity.
func (s Severity) DefaultStake() int {
	switch s {
	case SeverityCritical:
		return 8
	case SeverityWarning:
		return 4
	case SeveritySuggestion:
		return 2
	default:
		return 0
	}
}

// IsValid reports whether s is one of the three known severities.
func (s Severity) IsValid() bool {
	return s == SeverityCritical || s == SeverityWarning || s == SeveritySuggestion
}

// Category names the kind of bug-class a finding describes. The list is
// open — agents are free to coin new categories, but the well-known set
// here is what the adversary, classifier, and gates reason about.
type Category string

const (
	CategoryUnderSpecification    Category = "under_specification"
	CategoryOverSpecification     Category = "over_specification"
	CategoryTriviality            Category = "triviality"
	CategoryAmbiguity             Category = "ambiguity"
	CategoryCoverageGap           Category = "coverage_gap"
	CategoryContradiction         Category = "contradiction"
	CategoryEdgeCase              Category = "edge_case"
	CategoryCompositionFailure    Category = "composition_failure"
	CategoryRefinementMismatch    Category = "refinement_mismatch"
	CategoryTemporalStateMismatch Category = "temporal_state_mismatch"
	CategorySharedBlindSpot       Category = "shared_blind_spot"
	CategoryHiddenAssumption      Category = "hidden_assumption"
	CategoryAdversarialInput      Category = "adversarial_input"
)

// Kind discriminates ledger entries. Finding, Resolution, and TriageEvent
// all live in the same JSONL file; readers switch on Kind.
type Kind string

const (
	KindFinding    Kind = "finding"
	KindResolution Kind = "resolution"
	// KindTriage is appended whenever a PM/QA/automation transitions a
	// finding's triage state (open → in-progress → fixed, etc.). Multiple
	// triage events per finding are expected; the ledger stays append-only.
	KindTriage Kind = "triage"
)

// Finding is the load-bearing signed event filed by a reviewer or adversary
// when they identify a problem. Stored as a JSONL line; the signature is
// computed over the canonical JSON encoding of the struct with the
// Signature field zeroed.
//
// ClawpatchID is set when the finding originated from a clawpatch
// subprocess (Phase 2 absorption). It's the stable clawpatch finding ID
// used by `tribunal fix` / `tribunal revalidate` to round-trip state back
// to clawpatch's own store. Empty on findings filed by skill-trio or
// adversary agents; readers must tolerate its absence on legacy entries.
type Finding struct {
	Kind      Kind   `json:"kind"`
	FindingID string `json:"finding_id"`
	// PlanID anchors the finding to a single plan under .tribunal/plans/.
	// Empty when TrajectoryID is set (v0.5.6+ cross-plan findings).
	PlanID string `json:"plan_id"`
	// TrajectoryID (v0.5.6+) is set when the finding spans many plans —
	// e.g., a temporal-lens finding about portrait drift across the last
	// N audit cycles. Exactly one of PlanID and TrajectoryID is non-empty;
	// the SigningPayload + Verify enforce this. Chain sync skips
	// trajectory findings (settlement is plan-scoped on-chain only;
	// trajectory findings stay local until the contract grows trajectory
	// support).
	TrajectoryID string    `json:"trajectory_id,omitempty"`
	Round        int       `json:"round"`
	AgentPubkey  string    `json:"agent_pubkey"`
	AgentLabel   string    `json:"agent_label"`
	Severity     Severity  `json:"severity"`
	Category     Category  `json:"category"`
	ClaimHash    string    `json:"claim_hash"`
	ClaimURI     string    `json:"claim_uri"`
	Stake        int       `json:"stake"`
	Timestamp    time.Time `json:"timestamp"`
	Signature    string    `json:"signature"`
	ClawpatchID  string    `json:"clawpatch_id,omitempty"`
}

// NewFinding constructs a plan-scoped Finding with sensible defaults:
// severity-based stake, current UTC timestamp, and Kind set to KindFinding.
// The caller must still call Sign before persisting. For findings that
// span many plans (temporal-lens trajectory observations), use
// NewTrajectoryFinding instead.
func NewFinding(findingID, planID string, round int, kp *agent.Keypair, agentLabel string, sev Severity, cat Category, claimHash, claimURI string) *Finding {
	return &Finding{
		Kind:        KindFinding,
		FindingID:   findingID,
		PlanID:      planID,
		Round:       round,
		AgentPubkey: kp.PublicKeyString(),
		AgentLabel:  agentLabel,
		Severity:    sev,
		Category:    cat,
		ClaimHash:   claimHash,
		ClaimURI:    claimURI,
		Stake:       sev.DefaultStake(),
		Timestamp:   time.Now().UTC(),
	}
}

// NewTrajectoryFinding (v0.5.6+) constructs a Finding scoped to a
// trajectory (a logical sequence of plans the temporal lens audits as a
// whole). PlanID stays empty. Round=0 by convention since trajectory
// findings don't belong to any single round. Chain sync skips trajectory
// findings; they live local-only until the contract grows trajectory-
// scoped settlement.
func NewTrajectoryFinding(findingID, trajectoryID string, kp *agent.Keypair, agentLabel string, sev Severity, cat Category, claimHash, claimURI string) *Finding {
	return &Finding{
		Kind:         KindFinding,
		FindingID:    findingID,
		TrajectoryID: trajectoryID,
		Round:        0,
		AgentPubkey:  kp.PublicKeyString(),
		AgentLabel:   agentLabel,
		Severity:     sev,
		Category:     cat,
		ClaimHash:    claimHash,
		ClaimURI:     claimURI,
		Stake:        sev.DefaultStake(),
		Timestamp:    time.Now().UTC(),
	}
}

// SigningPayload returns the canonical bytes signed by the agent.
// Determinism: Go's encoding/json marshals struct fields in declaration
// order, and Finding contains no maps. Two callers with the same Finding
// data always produce byte-identical payloads. Validation enforces
// exactly-one-of(PlanID, TrajectoryID).
func (f *Finding) SigningPayload() ([]byte, error) {
	if !f.Severity.IsValid() {
		return nil, fmt.Errorf("finding: invalid severity %q", f.Severity)
	}
	hasPlan := f.PlanID != ""
	hasTrajectory := f.TrajectoryID != ""
	if hasPlan == hasTrajectory {
		return nil, fmt.Errorf("finding: exactly one of plan_id or trajectory_id must be set (got plan_id=%q, trajectory_id=%q)", f.PlanID, f.TrajectoryID)
	}
	cp := *f
	cp.Signature = ""
	cp.Kind = KindFinding
	return json.Marshal(&cp)
}

// Sign computes and assigns the agent's signature over the canonical
// payload. The keypair's pubkey must match f.AgentPubkey.
func (f *Finding) Sign(kp *agent.Keypair) error {
	if kp.PublicKeyString() != f.AgentPubkey {
		return fmt.Errorf("finding: keypair pubkey %q does not match finding agent_pubkey %q", kp.PublicKeyString(), f.AgentPubkey)
	}
	payload, err := f.SigningPayload()
	if err != nil {
		return err
	}
	f.Signature = kp.Sign(payload)
	return nil
}

// Verify checks the embedded signature against the canonical payload.
func (f *Finding) Verify() error {
	if f.Signature == "" {
		return fmt.Errorf("finding %s: missing signature", f.FindingID)
	}
	payload, err := f.SigningPayload()
	if err != nil {
		return err
	}
	return agent.Verify(f.AgentPubkey, f.Signature, payload)
}
