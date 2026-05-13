package chain

import "fmt"

// CanonicalFindingMessage returns the bytes an agent signs to authorize a
// commit. The encoding is deliberately simple: ASCII-only, pipe-separated,
// stake serialized as decimal. Mirrored EXACTLY in the Rust contract's
// canonical_finding_message helper — any change here requires a
// corresponding contract change and chain migration.
func CanonicalFindingMessage(planID, findingID, severity, claimHash string, stake uint64) []byte {
	return []byte(fmt.Sprintf("TRIBUNAL_FINDING|%s|%s|%s|%s|%d",
		planID, findingID, severity, claimHash, stake))
}

// CanonicalResolutionMessage returns the bytes a resolver signs.
// Mirrors the Rust contract's canonical_resolution_message helper.
func CanonicalResolutionMessage(planID, findingID, outcome, evidenceHash string) []byte {
	return []byte(fmt.Sprintf("TRIBUNAL_RESOLUTION|%s|%s|%s|%s",
		planID, findingID, outcome, evidenceHash))
}
