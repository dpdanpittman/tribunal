# Adversary Report — P-v035-followup

This is a scoped, post-hoc filing of F-OPUS-004 — the most novel finding
from the four-adversary panel run on 2026-05-17. Full reasoning, evidence,
and attack scenario live in
`.tribunal/reports/P-multi-adversary/adversary-opus.md` (search for
"F-OPUS-004"). This report exists so `tribunal-batch-file` can sign and
append the finding to the ledger.

## FINDINGS-TO-FILE

```
warning|adversarial_input|F-OPUS-004|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-004|looksLikeTestChain is Unicode-bypass-able — MAİNNET-test-fork (U+0130 Turkish dotted I) survives the mainnet token check but trips the test token, so tribunal-seed --send proceeds without --allow-prod against a chain whose id is human-readable as mainnet. Duplicated at cmd/tribunal-seed/main.go:129 and internal/chain/client.go:62.
```

