package ledger

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"pgregory.net/rapid"

	"github.com/dpdanpittman/tribunal/internal/agent"
)

// kpFromSeedBytes builds a deterministic keypair from rapid-generated
// seed bytes. Local helper because the existing test kpFromSeed lives
// in internal/chain and uses a single byte.
func kpFromSeedBytes(t *rapid.T, seed []byte) *agent.Keypair {
	// Pad to ed25519.SeedSize (32). rapid produces variable-length slices.
	pad := make([]byte, ed25519.SeedSize)
	copy(pad, seed)
	kp, err := agent.NewKeypairFromSeed(pad)
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	return kp
}

// validSeverityGen draws from the three real severity tiers. Tribunal's
// signing payload validates severity before computing bytes, so invalid
// severities are out of scope for this roundtrip property.
func validSeverityGen() *rapid.Generator[Severity] {
	return rapid.SampledFrom([]Severity{SeverityCritical, SeverityWarning, SeveritySuggestion})
}

func validCategoryGen() *rapid.Generator[Category] {
	return rapid.SampledFrom([]Category{
		CategorySharedBlindSpot,
		CategoryEdgeCase,
		CategoryRefinementMismatch,
		CategoryAmbiguity,
		CategoryTemporalStateMismatch,
	})
}

// idStringGen produces a "reasonable" identifier: 1-32 chars from
// [a-zA-Z0-9._-]. Matches the realistic Tribunal id space without
// pulling in the contract's full validate_id_field grammar (which is
// covered by separate Rust integration tests).
func idStringGen() *rapid.Generator[string] {
	return rapid.StringMatching("[a-zA-Z0-9._-]{1,32}")
}

// hashStringGen produces a sha256:<64-hex> shape.
func hashStringGen() *rapid.Generator[string] {
	return rapid.StringMatching("sha256:[0-9a-f]{64}")
}

// TestPBT_FindingSignVerifyRoundtrip — for any well-formed Finding +
// any matching keypair, Sign produces a signature that Verify accepts.
// 1000+ generated inputs across the configured severity + category +
// id-string spaces; rapid shrinks any counterexample to a minimal form.
func TestPBT_FindingSignVerifyRoundtrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.SliceOfN(rapid.Byte(), 32, 32).Draw(t, "seed")
		kp := kpFromSeedBytes(t, seed)

		f := &Finding{
			Kind:        KindFinding,
			FindingID:   idStringGen().Draw(t, "findingID"),
			PlanID:      idStringGen().Draw(t, "planID"),
			Round:       rapid.IntRange(1, 10).Draw(t, "round"),
			AgentPubkey: kp.PublicKeyString(),
			AgentLabel:  idStringGen().Draw(t, "agentLabel"),
			Severity:    validSeverityGen().Draw(t, "severity"),
			Category:    validCategoryGen().Draw(t, "category"),
			ClaimHash:   hashStringGen().Draw(t, "claimHash"),
			ClaimURI:    idStringGen().Draw(t, "claimURI"),
		}
		// Severity-driven stake set by Sign path; default here for clarity.
		f.Stake = f.Severity.DefaultStake()

		if err := f.Sign(kp); err != nil {
			t.Fatalf("sign: %v", err)
		}
		if err := f.Verify(); err != nil {
			t.Fatalf("verify after sign failed: %v (finding=%+v)", err, f)
		}
	})
}

// TestPBT_FindingTamperingBreaksVerify — for any signed Finding,
// changing any string field invalidates the signature. The property
// is the cryptographic primitive's core guarantee: signatures are
// bound to the canonical payload, not to the agent identity alone.
func TestPBT_FindingTamperingBreaksVerify(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.SliceOfN(rapid.Byte(), 32, 32).Draw(t, "seed")
		kp := kpFromSeedBytes(t, seed)

		f := &Finding{
			Kind:        KindFinding,
			FindingID:   idStringGen().Draw(t, "findingID"),
			PlanID:      idStringGen().Draw(t, "planID"),
			Round:       1,
			AgentPubkey: kp.PublicKeyString(),
			AgentLabel:  idStringGen().Draw(t, "agentLabel"),
			Severity:    validSeverityGen().Draw(t, "severity"),
			Category:    validCategoryGen().Draw(t, "category"),
			ClaimHash:   hashStringGen().Draw(t, "claimHash"),
			ClaimURI:    idStringGen().Draw(t, "claimURI"),
		}
		f.Stake = f.Severity.DefaultStake()
		if err := f.Sign(kp); err != nil {
			t.Fatalf("sign: %v", err)
		}
		// Sanity: verify passes pre-tamper.
		if err := f.Verify(); err != nil {
			t.Fatalf("baseline verify failed: %v", err)
		}

		// Pick a field to tamper. Each branch mutates one identifier in a
		// way that's guaranteed to differ from the original (append a
		// stable suffix) so we don't accidentally produce equality.
		field := rapid.SampledFrom([]string{
			"FindingID", "PlanID", "ClaimHash", "ClaimURI", "AgentLabel",
		}).Draw(t, "tamperField")
		switch field {
		case "FindingID":
			f.FindingID = f.FindingID + "-tampered"
		case "PlanID":
			f.PlanID = f.PlanID + "-tampered"
		case "ClaimHash":
			// Keep the sha256: prefix shape but change the digest.
			if len(f.ClaimHash) > 10 {
				f.ClaimHash = "sha256:" + reverseString(f.ClaimHash[7:])
			} else {
				f.ClaimHash = f.ClaimHash + "x"
			}
		case "ClaimURI":
			f.ClaimURI = f.ClaimURI + "-tampered"
		case "AgentLabel":
			f.AgentLabel = f.AgentLabel + "-tampered"
		}
		if err := f.Verify(); err == nil {
			t.Fatalf("verify still passes after tampering %s — signature not bound to payload", field)
		}
	})
}

// TestPBT_SigningPayloadDeterministic — same Finding → byte-identical
// signing payload across calls. JSON marshalling order is the
// invariant; the package doc claims it; the property pins it.
func TestPBT_SigningPayloadDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.SliceOfN(rapid.Byte(), 32, 32).Draw(t, "seed")
		kp := kpFromSeedBytes(t, seed)

		f := &Finding{
			Kind:        KindFinding,
			FindingID:   idStringGen().Draw(t, "findingID"),
			PlanID:      idStringGen().Draw(t, "planID"),
			Round:       rapid.IntRange(1, 20).Draw(t, "round"),
			AgentPubkey: kp.PublicKeyString(),
			AgentLabel:  idStringGen().Draw(t, "agentLabel"),
			Severity:    validSeverityGen().Draw(t, "severity"),
			Category:    validCategoryGen().Draw(t, "category"),
			ClaimHash:   hashStringGen().Draw(t, "claimHash"),
			ClaimURI:    idStringGen().Draw(t, "claimURI"),
		}
		f.Stake = f.Severity.DefaultStake()

		a, err := f.SigningPayload()
		if err != nil {
			t.Fatalf("SigningPayload (1st): %v", err)
		}
		b, err := f.SigningPayload()
		if err != nil {
			t.Fatalf("SigningPayload (2nd): %v", err)
		}
		if string(a) != string(b) {
			t.Fatalf("non-deterministic payload:\n a=%s\n b=%s", string(a), string(b))
		}
		// And the payload must actually round-trip through json (sanity
		// check that the encoded bytes are valid JSON, not random bytes).
		var anyobj map[string]any
		if err := json.Unmarshal(a, &anyobj); err != nil {
			t.Fatalf("SigningPayload bytes are not valid JSON: %v", err)
		}
	})
}

func reverseString(s string) string {
	r := []byte(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
