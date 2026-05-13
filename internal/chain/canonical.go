package chain

import "fmt"

// CanonicalFindingMessage returns the bytes an agent signs to authorize a
// commit. The encoding is deliberately simple: ASCII-only, pipe-separated,
// stake passed in as a decimal string so the representation matches the
// `cosmwasm_std::Uint128` wire format identically regardless of magnitude.
// Mirrored EXACTLY in the Rust contract's `canonical_finding_message`
// helper — any change here requires a corresponding contract change and
// chain migration.
func CanonicalFindingMessage(planID, findingID, severity, claimHash, stake string) []byte {
	return []byte(fmt.Sprintf("TRIBUNAL_FINDING|%s|%s|%s|%s|%s",
		planID, findingID, severity, claimHash, stake))
}

// CanonicalResolutionMessage returns the bytes a resolver signs.
// Mirrors the Rust contract's canonical_resolution_message helper.
func CanonicalResolutionMessage(planID, findingID, outcome, evidenceHash string) []byte {
	return []byte(fmt.Sprintf("TRIBUNAL_RESOLUTION|%s|%s|%s|%s",
		planID, findingID, outcome, evidenceHash))
}
