package ledger

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
)

// Outcome is the verdict a resolver assigns to a finding when it closes
// out. The four values correspond directly to the reputation math.
type Outcome string

const (
	// OutcomeTruePositive: a fix was merged that addresses the finding.
	// Stake returned, reward = stake × OutcomeRewardMultiplier.
	OutcomeTruePositive Outcome = "true_positive"
	// OutcomeFalsePositive: a dismissal was merged. Stake slashed.
	OutcomeFalsePositive Outcome = "false_positive"
	// OutcomeStaleDuplicate: the finding repeats an existing one. No
	// stake change.
	OutcomeStaleDuplicate Outcome = "stale_duplicate"
	// OutcomeIndeterminate: N rounds elapsed without resolution. Stake
	// returned, no reward.
	OutcomeIndeterminate Outcome = "indeterminate"
)

// IsValid reports whether o is one of the four known outcomes.
func (o Outcome) IsValid() bool {
	switch o {
	case OutcomeTruePositive, OutcomeFalsePositive, OutcomeStaleDuplicate, OutcomeIndeterminate:
		return true
	}
	return false
}

// Resolution closes out a finding. Like Finding, it's signed (by a PM or
// QA agent) and persisted to the same ledger.jsonl. Must match the scope
// (plan_id or trajectory_id) of the Finding it resolves.
type Resolution struct {
	Kind      Kind   `json:"kind"`
	FindingID string `json:"finding_id"`
	// PlanID anchors the resolution to a plan when resolving a plan-scoped
	// Finding. Empty when TrajectoryID is set.
	PlanID string `json:"plan_id"`
	// TrajectoryID (v0.5.6+) is set when resolving a trajectory-scoped
	// Finding. Exactly one of PlanID and TrajectoryID is non-empty;
	// SigningPayload enforces this.
	TrajectoryID   string    `json:"trajectory_id,omitempty"`
	Outcome        Outcome   `json:"outcome"`
	ResolverPubkey string    `json:"resolver_pubkey"`
	ResolverLabel  string    `json:"resolver_label"`
	EvidenceHash   string    `json:"evidence_hash"`
	EvidenceURI    string    `json:"evidence_uri"`
	Reward         int       `json:"reward"`
	Timestamp      time.Time `json:"timestamp"`
	Signature      string    `json:"signature"`
}

// NewResolution constructs a plan-scoped Resolution. Reward is left at
// zero; the caller is expected to set it (or use the default reward
// calculator). Sign must be called before persisting. For resolutions
// of trajectory-scoped Findings, use NewTrajectoryResolution.
func NewResolution(findingID, planID string, outcome Outcome, kp *agent.Keypair, resolverLabel string, evidenceHash, evidenceURI string) *Resolution {
	return &Resolution{
		Kind:           KindResolution,
		FindingID:      findingID,
		PlanID:         planID,
		Outcome:        outcome,
		ResolverPubkey: kp.PublicKeyString(),
		ResolverLabel:  resolverLabel,
		EvidenceHash:   evidenceHash,
		EvidenceURI:    evidenceURI,
		Timestamp:      time.Now().UTC(),
	}
}

// NewTrajectoryResolution (v0.5.6+) constructs a Resolution for a
// trajectory-scoped Finding. PlanID stays empty; chain sync skips
// trajectory resolutions.
func NewTrajectoryResolution(findingID, trajectoryID string, outcome Outcome, kp *agent.Keypair, resolverLabel string, evidenceHash, evidenceURI string) *Resolution {
	return &Resolution{
		Kind:           KindResolution,
		FindingID:      findingID,
		TrajectoryID:   trajectoryID,
		Outcome:        outcome,
		ResolverPubkey: kp.PublicKeyString(),
		ResolverLabel:  resolverLabel,
		EvidenceHash:   evidenceHash,
		EvidenceURI:    evidenceURI,
		Timestamp:      time.Now().UTC(),
	}
}

// SigningPayload mirrors Finding.SigningPayload — canonical JSON with the
// Signature field zeroed. Validation enforces exactly-one-of(PlanID,
// TrajectoryID).
func (r *Resolution) SigningPayload() ([]byte, error) {
	if !r.Outcome.IsValid() {
		return nil, fmt.Errorf("resolution: invalid outcome %q", r.Outcome)
	}
	hasPlan := r.PlanID != ""
	hasTrajectory := r.TrajectoryID != ""
	if hasPlan == hasTrajectory {
		return nil, fmt.Errorf("resolution: exactly one of plan_id or trajectory_id must be set (got plan_id=%q, trajectory_id=%q)", r.PlanID, r.TrajectoryID)
	}
	cp := *r
	cp.Signature = ""
	cp.Kind = KindResolution
	return json.Marshal(&cp)
}

// Sign computes and assigns the resolver's signature.
func (r *Resolution) Sign(kp *agent.Keypair) error {
	if kp.PublicKeyString() != r.ResolverPubkey {
		return fmt.Errorf("resolution: keypair pubkey %q does not match resolver_pubkey %q", kp.PublicKeyString(), r.ResolverPubkey)
	}
	payload, err := r.SigningPayload()
	if err != nil {
		return err
	}
	r.Signature = kp.Sign(payload)
	return nil
}

// Verify checks the embedded signature against the canonical payload.
func (r *Resolution) Verify() error {
	if r.Signature == "" {
		return fmt.Errorf("resolution %s: missing signature", r.FindingID)
	}
	payload, err := r.SigningPayload()
	if err != nil {
		return err
	}
	return agent.Verify(r.ResolverPubkey, r.Signature, payload)
}

// OutcomeRewardMultiplier is the reward (in stake-units) per stake-unit
// returned to an agent whose finding is resolved true_positive.
const OutcomeRewardMultiplier = 2

// DefaultReward returns the reward an agent receives for the given outcome
// and stake amount, before family-diversity bonuses.
func DefaultReward(outcome Outcome, stake int) int {
	if outcome == OutcomeTruePositive {
		return stake * OutcomeRewardMultiplier
	}
	return 0
}
