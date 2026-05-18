package ledger

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
)

// TriageStatus is the current disposition of a finding as recorded by a
// triager (PM, QA, or automation). The set is deliberately aligned with
// clawpatch's status enum so the bidirectional sync in Phase 2b is a
// straight value-passthrough.
type TriageStatus string

const (
	// TriageStatusOpen — newly filed; not yet acted on. The implicit
	// status of every Finding without a triage event.
	TriageStatusOpen TriageStatus = "open"
	// TriageStatusInProgress — work in flight (a fix is being attempted
	// or an investigation is open).
	TriageStatusInProgress TriageStatus = "in-progress"
	// TriageStatusFixed — a patch landed that addresses the finding.
	// A subsequent Resolution should follow to settle stake.
	TriageStatusFixed TriageStatus = "fixed"
	// TriageStatusFalsePositive — investigation concluded the finding
	// was incorrect. Stake will be slashed on Resolution.
	TriageStatusFalsePositive TriageStatus = "false-positive"
	// TriageStatusWontFix — accepted as a known issue that won't be
	// addressed in scope. No Resolution required; stake stays parked.
	TriageStatusWontFix TriageStatus = "wont-fix"
	// TriageStatusUncertain — analysis didn't reach a conclusion;
	// re-triage on a future pass.
	TriageStatusUncertain TriageStatus = "uncertain"
)

// IsValid reports whether s is one of the six known triage statuses.
func (s TriageStatus) IsValid() bool {
	switch s {
	case TriageStatusOpen, TriageStatusInProgress, TriageStatusFixed,
		TriageStatusFalsePositive, TriageStatusWontFix, TriageStatusUncertain:
		return true
	}
	return false
}

// TriageEvent is appended every time a finding's disposition changes.
// Multiple events per finding are expected: the latest in-file order wins.
// Like Finding and Resolution, it's signed; the triager's keypair attests
// to the transition.
type TriageEvent struct {
	Kind          Kind         `json:"kind"`
	FindingID     string       `json:"finding_id"`
	PlanID        string       `json:"plan_id"`
	Status        TriageStatus `json:"status"`
	Note          string       `json:"note,omitempty"`
	TriagerPubkey string       `json:"triager_pubkey"`
	TriagerLabel  string       `json:"triager_label"`
	Timestamp     time.Time    `json:"timestamp"`
	Signature     string       `json:"signature"`
}

// NewTriageEvent constructs a TriageEvent with the current UTC timestamp
// and Kind set. The caller must call Sign before persisting.
func NewTriageEvent(findingID, planID string, status TriageStatus, kp *agent.Keypair, triagerLabel, note string) *TriageEvent {
	return &TriageEvent{
		Kind:          KindTriage,
		FindingID:     findingID,
		PlanID:        planID,
		Status:        status,
		Note:          note,
		TriagerPubkey: kp.PublicKeyString(),
		TriagerLabel:  triagerLabel,
		Timestamp:     time.Now().UTC(),
	}
}

// SigningPayload mirrors Finding.SigningPayload — canonical JSON with the
// Signature field zeroed.
func (t *TriageEvent) SigningPayload() ([]byte, error) {
	if !t.Status.IsValid() {
		return nil, fmt.Errorf("triage: invalid status %q", t.Status)
	}
	cp := *t
	cp.Signature = ""
	cp.Kind = KindTriage
	return json.Marshal(&cp)
}

// Sign computes and assigns the triager's signature over the canonical
// payload.
func (t *TriageEvent) Sign(kp *agent.Keypair) error {
	if kp.PublicKeyString() != t.TriagerPubkey {
		return fmt.Errorf("triage: keypair pubkey %q does not match triager_pubkey %q", kp.PublicKeyString(), t.TriagerPubkey)
	}
	payload, err := t.SigningPayload()
	if err != nil {
		return err
	}
	t.Signature = kp.Sign(payload)
	return nil
}

// Verify checks the embedded signature against the canonical payload.
func (t *TriageEvent) Verify() error {
	if t.Signature == "" {
		return fmt.Errorf("triage %s: missing signature", t.FindingID)
	}
	payload, err := t.SigningPayload()
	if err != nil {
		return err
	}
	return agent.Verify(t.TriagerPubkey, t.Signature, payload)
}
