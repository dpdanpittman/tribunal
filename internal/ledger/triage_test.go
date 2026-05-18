package ledger

import (
	"encoding/json"
	"testing"
)

func TestTriageEventSignVerifyRoundtrip(t *testing.T) {
	kp := mustKeypair(t, 0x21)
	evt := NewTriageEvent("F-1", "P-1", TriageStatusInProgress, kp, "human-triager", "investigating")
	if err := evt.Sign(kp); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := evt.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestTriageEventSignRejectsWrongKeypair(t *testing.T) {
	k1 := mustKeypair(t, 0x22)
	k2 := mustKeypair(t, 0x23)
	evt := NewTriageEvent("F-1", "P-1", TriageStatusOpen, k1, "triager", "")
	if err := evt.Sign(k2); err == nil {
		t.Fatal("expected sign with wrong keypair to fail")
	}
}

func TestTriageEventRejectsInvalidStatus(t *testing.T) {
	kp := mustKeypair(t, 0x24)
	evt := NewTriageEvent("F-1", "P-1", TriageStatus("bogus"), kp, "triager", "")
	if err := evt.Sign(kp); err == nil {
		t.Fatal("expected sign with invalid status to fail")
	}
}

func TestLedgerAppendTriageRoundtrip(t *testing.T) {
	l := tempLedger(t)
	kp := mustKeypair(t, 0x25)

	// Also append a finding so the ledger has mixed entries — confirms
	// AllTriage filters by kind, not by file position.
	f := NewFinding("F-1", "P-1", 1, kp, "agent", SeverityWarning, CategoryEdgeCase, "h", "u")
	mustSignFinding(t, f, kp)
	if err := l.AppendFinding(f); err != nil {
		t.Fatal(err)
	}

	evt := NewTriageEvent("F-1", "P-1", TriageStatusInProgress, kp, "triager", "looking")
	if err := evt.Sign(kp); err != nil {
		t.Fatal(err)
	}
	if err := l.AppendTriage(evt); err != nil {
		t.Fatal(err)
	}

	// All() must still return one finding, zero resolutions, and skip the triage line.
	findings, resolutions, err := l.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || len(resolutions) != 0 {
		t.Fatalf("All() got %d findings / %d resolutions, want 1/0", len(findings), len(resolutions))
	}

	events, err := l.AllTriage()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("AllTriage() got %d events, want 1", len(events))
	}
	if events[0].FindingID != "F-1" || events[0].Status != TriageStatusInProgress {
		t.Fatalf("unexpected triage event: %+v", events[0])
	}
	if err := events[0].Verify(); err != nil {
		t.Fatalf("verify roundtripped triage: %v", err)
	}
}

func TestLedgerAppendTriageRejectsUnsigned(t *testing.T) {
	l := tempLedger(t)
	kp := mustKeypair(t, 0x26)
	evt := NewTriageEvent("F-1", "P-1", TriageStatusOpen, kp, "triager", "")
	// Intentionally don't sign.
	if err := l.AppendTriage(evt); err == nil {
		t.Fatal("expected AppendTriage to refuse an unsigned event")
	}
}

func TestLatestTriageByFindingPicksLastInFileOrder(t *testing.T) {
	l := tempLedger(t)
	kp := mustKeypair(t, 0x27)

	transitions := []TriageStatus{
		TriageStatusOpen,
		TriageStatusInProgress,
		TriageStatusFixed,
	}
	for _, st := range transitions {
		evt := NewTriageEvent("F-1", "P-1", st, kp, "triager", "")
		if err := evt.Sign(kp); err != nil {
			t.Fatal(err)
		}
		if err := l.AppendTriage(evt); err != nil {
			t.Fatal(err)
		}
	}

	// A second finding gets one event so the map has multiple keys.
	other := NewTriageEvent("F-2", "P-1", TriageStatusFalsePositive, kp, "triager", "")
	if err := other.Sign(kp); err != nil {
		t.Fatal(err)
	}
	if err := l.AppendTriage(other); err != nil {
		t.Fatal(err)
	}

	latest, err := l.LatestTriageByFinding()
	if err != nil {
		t.Fatal(err)
	}
	if latest["F-1"].Status != TriageStatusFixed {
		t.Errorf("F-1 latest = %s, want fixed", latest["F-1"].Status)
	}
	if latest["F-2"].Status != TriageStatusFalsePositive {
		t.Errorf("F-2 latest = %s, want false-positive", latest["F-2"].Status)
	}
}

func TestVerifyAllCatchesTamperedTriage(t *testing.T) {
	l := tempLedger(t)
	kp := mustKeypair(t, 0x28)

	evt := NewTriageEvent("F-1", "P-1", TriageStatusInProgress, kp, "triager", "")
	if err := evt.Sign(kp); err != nil {
		t.Fatal(err)
	}
	if err := l.AppendTriage(evt); err != nil {
		t.Fatal(err)
	}

	// Forge a second triage event with a tampered status but reuse the
	// real one's signature. Bypass AppendTriage's verify guard by writing
	// directly so we can simulate on-disk tampering.
	forged := *evt
	forged.Status = TriageStatusFixed
	if err := l.appendJSON(&forged); err != nil {
		t.Fatal(err)
	}

	if err := l.VerifyAll(); err == nil {
		t.Fatal("expected VerifyAll to flag the tampered triage event")
	}
}

func TestFindingClawpatchIDJSONOmitemptyBackcompat(t *testing.T) {
	// Old entries with no clawpatch_id must still round-trip cleanly.
	kp := mustKeypair(t, 0x29)
	f := NewFinding("F-1", "P-1", 1, kp, "agent", SeverityWarning, CategoryEdgeCase, "h", "u")
	mustSignFinding(t, f, kp)

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if containsBytes(data, "clawpatch_id") {
		t.Fatalf("expected clawpatch_id to be omitted on legacy finding, got %s", data)
	}

	var decoded Finding
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ClawpatchID != "" {
		t.Errorf("expected empty ClawpatchID after decode, got %q", decoded.ClawpatchID)
	}
	if err := decoded.Verify(); err != nil {
		t.Errorf("legacy finding (no clawpatch_id) should still verify: %v", err)
	}

	// And when set, the value round-trips and the signature still validates.
	f2 := NewFinding("F-2", "P-1", 1, kp, "agent", SeverityWarning, CategoryEdgeCase, "h", "u")
	f2.ClawpatchID = "claw-finding-42"
	mustSignFinding(t, f2, kp)
	data2, err := json.Marshal(f2)
	if err != nil {
		t.Fatal(err)
	}
	if !containsBytes(data2, "clawpatch_id") {
		t.Fatalf("expected clawpatch_id to be present when set, got %s", data2)
	}
	var decoded2 Finding
	if err := json.Unmarshal(data2, &decoded2); err != nil {
		t.Fatal(err)
	}
	if decoded2.ClawpatchID != "claw-finding-42" {
		t.Errorf("ClawpatchID = %q, want claw-finding-42", decoded2.ClawpatchID)
	}
	if err := decoded2.Verify(); err != nil {
		t.Errorf("verify with ClawpatchID set: %v", err)
	}
}

func containsBytes(haystack []byte, needle string) bool {
	n := []byte(needle)
	if len(n) == 0 || len(haystack) < len(n) {
		return false
	}
	for i := 0; i+len(n) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(n); j++ {
			if haystack[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
