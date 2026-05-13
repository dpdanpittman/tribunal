package chain

import (
	"fmt"
	"unicode"
)

// Identifier-validation constraints. Mirror the Rust contract's
// `src/validate.rs` constants so commit-time rejection on the Go side
// matches what the contract enforces, surfacing errors before a tx is
// even submitted.
const (
	MaxIDLen    = 64
	MaxHashLen  = 128
	MaxLabelLen = 64
)

// validateIDField rejects identifiers that contain bytes the canonical
// signing format can't safely round-trip: the pipe separator (which is
// the field delimiter) and any ASCII control character. Empty strings
// are rejected.
func validateIDField(field, value string, maxLen int) error {
	if value == "" {
		return fmt.Errorf("%s: empty", field)
	}
	if len(value) > maxLen {
		return fmt.Errorf("%s: length %d exceeds max %d", field, len(value), maxLen)
	}
	for i, r := range value {
		if r == '|' {
			return fmt.Errorf("%s: contains pipe character at index %d", field, i)
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("%s: contains control character at index %d", field, i)
		}
	}
	return nil
}
