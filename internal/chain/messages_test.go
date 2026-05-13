package chain

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// kpFromSeed makes a deterministic keypair from a single seed byte. Mirrors
// the helper used by the contract integration tests so the two test suites
// stay symmetric.
func kpFromSeed(t *testing.T, b byte) *agent.Keypair {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = b
	}
	kp, err := agent.NewKeypairFromSeed(seed)
	if err != nil {
		t.Fatalf("seed kp: %v", err)
	}
	return kp
}

func TestCanonicalFindingMessage_StableFormat(t *testing.T) {
	got := string(CanonicalFindingMessage("P-1", "F-1", "critical", "h", 8))
	want := "TRIBUNAL_FINDING|P-1|F-1|critical|h|8"
	if got != want {
		t.Fatalf("canonical finding bytes drift\n got: %q\nwant: %q", got, want)
	}
}

func TestCanonicalResolutionMessage_StableFormat(t *testing.T) {
	got := string(CanonicalResolutionMessage("P-1", "F-1", "true_positive", "ev"))
	want := "TRIBUNAL_RESOLUTION|P-1|F-1|true_positive|ev"
	if got != want {
		t.Fatalf("canonical resolution bytes drift\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildFindingCommit_SignatureRoundtrips(t *testing.T) {
	kp := kpFromSeed(t, 0xAB)
	f := &ledger.Finding{
		Kind:        ledger.KindFinding,
		FindingID:   "F-99",
		PlanID:      "P-42",
		AgentPubkey: kp.PublicKeyString(),
		Severity:    ledger.SeverityCritical,
		Category:    ledger.CategorySharedBlindSpot,
		ClaimHash:   "sha256:cafe",
		Stake:       8,
	}
	commit, err := BuildFindingCommit(f, kp)
	if err != nil {
		t.Fatalf("build commit: %v", err)
	}
	// Verify the embedded signature with the same canonical bytes.
	sig, err := base64.StdEncoding.DecodeString(commit.Signature)
	if err != nil {
		t.Fatalf("sig b64: %v", err)
	}
	pub, err := base64.StdEncoding.DecodeString(commit.AgentPubkey)
	if err != nil {
		t.Fatalf("pub b64: %v", err)
	}
	msg := CanonicalFindingMessage(f.PlanID, f.FindingID, string(f.Severity), f.ClaimHash, uint64(f.Stake))
	if !ed25519.Verify(pub, msg, sig) {
		t.Fatalf("signature did not verify")
	}
}

func TestBuildFindingCommit_RejectsPubkeyMismatch(t *testing.T) {
	kp := kpFromSeed(t, 0xCD)
	other := kpFromSeed(t, 0xEF)
	f := &ledger.Finding{
		Kind:        ledger.KindFinding,
		FindingID:   "F-x",
		PlanID:      "P-x",
		AgentPubkey: other.PublicKeyString(),
		Severity:    ledger.SeverityWarning,
		ClaimHash:   "h",
		Stake:       4,
	}
	if _, err := BuildFindingCommit(f, kp); err == nil {
		t.Fatalf("expected pubkey mismatch error")
	}
}

func TestBuildResolutionCommit_SignatureRoundtrips(t *testing.T) {
	kp := kpFromSeed(t, 0x10)
	r := &ledger.Resolution{
		Kind:           ledger.KindResolution,
		FindingID:      "F-r",
		PlanID:         "P-r",
		Outcome:        ledger.OutcomeTruePositive,
		ResolverPubkey: kp.PublicKeyString(),
		EvidenceHash:   "ev",
	}
	commit, err := BuildResolutionCommit(r, kp)
	if err != nil {
		t.Fatalf("build resolution: %v", err)
	}
	sig, _ := base64.StdEncoding.DecodeString(commit.Signature)
	pub, _ := base64.StdEncoding.DecodeString(commit.ResolverPubkey)
	msg := CanonicalResolutionMessage(r.PlanID, r.FindingID, string(r.Outcome), r.EvidenceHash)
	if !ed25519.Verify(pub, msg, sig) {
		t.Fatalf("resolution signature did not verify")
	}
}

func TestExecuteMsg_SerdeShape(t *testing.T) {
	// Mirror the Rust serde(rename_all="snake_case") shape with a single-key
	// JSON object that picks the variant.
	kp := kpFromSeed(t, 0x20)
	msg, err := BuildRegisterAgent(kp, "tester", "model-x", agent.RoleAdversary, 42)
	if err != nil {
		t.Fatalf("build register: %v", err)
	}
	out, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		`"register_agent"`,
		`"pubkey"`,
		`"label":"tester"`,
		`"role":"adversary"`,
		`"initial_balance":"42"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ExecuteMsg JSON missing %s\n got: %s", want, got)
		}
	}
}

func TestPubkeyToWire_Roundtrip(t *testing.T) {
	kp := kpFromSeed(t, 0x77)
	wire, err := PubkeyToWire(kp.PublicKeyString())
	if err != nil {
		t.Fatalf("to wire: %v", err)
	}
	back, err := DecodePubkeyBinary(wire)
	if err != nil {
		t.Fatalf("from wire: %v", err)
	}
	if back != kp.PublicKeyString() {
		t.Fatalf("roundtrip drift\n got: %s\nwant: %s", back, kp.PublicKeyString())
	}
}
