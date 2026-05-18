//! Identifier validation. The canonical signing format is pipe-separated
//! ASCII (`TRIBUNAL_FINDING|...`, `TRIBUNAL_RESOLUTION|...`), so any
//! identifier field that lands in it must be free of `|`, NUL, and other
//! control characters — otherwise downstream parsers that split on `|`
//! see ambiguous bytes even though signature verification still passes.
//!
//! Length caps additionally prevent storage-cost amplification via huge
//! keys: CosmWasm `Map<(&str, &str), ...>` stores the full key in the
//! storage prefix on every entry.

use crate::error::ContractError;

/// Maximum allowed length for plan_id / finding_id (storage key components).
pub const MAX_ID_LEN: usize = 64;
/// Maximum allowed length for claim_hash / evidence_hash. These are
/// content-addressed strings, so 128 chars accommodates `sha256:<64hex>`
/// or `b3:<64hex>` plus headroom for prefixed schemes.
pub const MAX_HASH_LEN: usize = 128;
/// Maximum allowed length for agent labels and rotation reasons.
pub const MAX_LABEL_LEN: usize = 64;
pub const MAX_MODEL_ID_LEN: usize = 128;
pub const MAX_REASON_LEN: usize = 256;

/// Reject identifiers that contain bytes outside printable ASCII
/// (U+0020..=U+007E). This subsumes the original "no pipe / no control
/// character" check and additionally rejects every multibyte UTF-8
/// sequence — closing the on-chain leg of the XSS surface that any
/// future leaderboard frontend would otherwise have to police on its
/// own (e.g. `<img src=x>` as an agent label). Empty strings are
/// rejected for storage-key fields; allowed where explicitly noted.
pub fn validate_id_field(field: &str, value: &str, max_len: usize) -> Result<(), ContractError> {
    if value.is_empty() {
        return Err(ContractError::InvalidIdentifier {
            field: field.to_string(),
            reason: "empty".to_string(),
        });
    }
    if value.len() > max_len {
        return Err(ContractError::InvalidIdentifier {
            field: field.to_string(),
            reason: format!("length {} exceeds max {}", value.len(), max_len),
        });
    }
    for (i, c) in value.chars().enumerate() {
        if c == '|' {
            return Err(ContractError::InvalidIdentifier {
                field: field.to_string(),
                reason: format!("contains pipe character at index {}", i),
            });
        }
        let cp = c as u32;
        if !(0x20..=0x7E).contains(&cp) {
            return Err(ContractError::InvalidIdentifier {
                field: field.to_string(),
                reason: format!("contains non-printable-ASCII character at index {}", i),
            });
        }
    }
    Ok(())
}

/// Reject identifiers like `validate_id_field` but allow empty (used for
/// rotation `reason`, which is documentary).
pub fn validate_optional_text(
    field: &str,
    value: &str,
    max_len: usize,
) -> Result<(), ContractError> {
    if value.is_empty() {
        return Ok(());
    }
    validate_id_field(field, value, max_len)
}

/// Maximum batch size for `commit_finding_batch` / `resolve_finding_batch`.
/// Tuned conservatively: each item costs ~140k gas on commit, so a 100-item
/// batch is ~14M gas, well within typical block budgets.
pub const MAX_BATCH_SIZE: usize = 100;

pub fn validate_batch_size(actual: usize) -> Result<(), ContractError> {
    if actual == 0 {
        return Err(ContractError::EmptyBatch);
    }
    if actual > MAX_BATCH_SIZE {
        return Err(ContractError::BatchTooLarge {
            actual,
            max: MAX_BATCH_SIZE,
        });
    }
    Ok(())
}
