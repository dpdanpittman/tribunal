// Package clawpatch wraps the `clawpatch` CLI as a subprocess from Tribunal,
// translating its findings into Tribunal-shaped ledger.Finding records that
// the existing signing + persistence layer can ingest.
//
// Tribunal's role: trust layer (identity, signed findings, adversary,
// convergence, on-chain settlement).
// Clawpatch's role: discovery layer (mapping + per-feature LLM review).
//
// The two systems communicate through clawpatch's on-disk JSON artifacts
// under `.clawpatch/`. This package defines the Go mirrors of those
// artifacts. See `/home/dan/src/clawpatch/src/types.ts` for the upstream
// TypeScript shapes — fields here track that file.
package clawpatch

// Finding mirrors clawpatch's `FindingRecord` (types.ts:221). One JSON
// file per finding lives at `.clawpatch/findings/<findingId>.json`.
//
// Severity uses clawpatch's 4-tier scale (critical/high/medium/low); the
// translator in translate.go maps that to Tribunal's 3-tier ledger.Severity.
type Finding struct {
	SchemaVersion           int        `json:"schemaVersion"`
	FindingID               string     `json:"findingId"`
	FeatureID               string     `json:"featureId"`
	Title                   string     `json:"title"`
	Category                string     `json:"category"` // bug|security|performance|concurrency|api-contract|data-loss|test-gap|docs-gap|build-release|maintainability
	Severity                string     `json:"severity"` // critical|high|medium|low
	Confidence              string     `json:"confidence"`
	Triage                  string     `json:"triage,omitempty"`
	Evidence                []Evidence `json:"evidence"`
	Reasoning               string     `json:"reasoning"`
	Reproduction            *string    `json:"reproduction"`
	Recommendation          string     `json:"recommendation"`
	WhyTestsMiss            string     `json:"whyTestsDoNotAlreadyCoverThis,omitempty"`
	SuggestedRegressionTest *string    `json:"suggestedRegressionTest,omitempty"`
	MinimumFixScope         string     `json:"minimumFixScope,omitempty"`
	Status                  string     `json:"status"` // open|false-positive|fixed|wont-fix|uncertain
	History                 []History  `json:"history,omitempty"`
	// Signature is clawpatch's stable hash for finding-deduplication. Not
	// a cryptographic signature — Tribunal computes its own ed25519
	// signature over a different canonical payload at ingest time.
	Signature           string   `json:"signature"`
	LinkedPatchAttempts []string `json:"linkedPatchAttemptIds"`
	CreatedByRunID      string   `json:"createdByRunId"`
	CreatedAt           string   `json:"createdAt"`
	UpdatedAt           string   `json:"updatedAt"`
}

// Evidence is one citation inside a finding. `StartLine` and `EndLine` are
// 1-indexed (clawpatch convention); they are nullable in clawpatch's schema.
type Evidence struct {
	Path      string  `json:"path"`
	StartLine *int    `json:"startLine"`
	EndLine   *int    `json:"endLine"`
	Symbol    *string `json:"symbol"`
	Quote     *string `json:"quote"`
}

// History is one entry from clawpatch's finding history (review,
// revalidate, triage, fix). Tribunal currently doesn't consume these;
// kept on the struct so JSON unmarshalling doesn't lose data on re-reads.
type History struct {
	RunID     *string  `json:"runId"`
	Kind      string   `json:"kind"`
	Status    *string  `json:"status"`
	Note      *string  `json:"note"`
	Reasoning *string  `json:"reasoning"`
	Commands  []string `json:"commands"`
	CreatedAt string   `json:"createdAt"`
}

// MapResult is the JSON payload returned by `clawpatch map --json`.
// Mirrors the return value at /home/dan/src/clawpatch/src/app.ts:158-167.
type MapResult struct {
	Features  int    `json:"features"`
	New       int    `json:"new"`
	Changed   int    `json:"changed"`
	Stale     int    `json:"stale"`
	Source    string `json:"source"` // heuristic|auto|agent
	UsedAgent bool   `json:"usedAgent"`
	Reason    string `json:"reason"`
}

// ReviewResult is the JSON payload returned by `clawpatch review --json`.
// Mirrors the return value at /home/dan/src/clawpatch/src/app.ts:291-299.
// Per-finding details live on disk; this is just the summary.
type ReviewResult struct {
	Run      string `json:"run"`      // runId
	Reviewed int    `json:"reviewed"` // features reviewed this run
	Findings int    `json:"findings"` // findings produced this run
	Jobs     int    `json:"jobs"`     // concurrency that was used
	Report   string `json:"report"`   // path to .clawpatch/reports/<runId>.md
	Next     string `json:"next"`     // suggested next command (advisory)
}

// ReviewOpts are the subset of `clawpatch review` flags Tribunal exercises.
// We deliberately do not surface every flag — only the ones whose Phase 1
// behavior we care about.
type ReviewOpts struct {
	// Limit caps the number of features reviewed in this run. Zero = no
	// limit (clawpatch's default).
	Limit int
	// Jobs sets parallel review concurrency; clawpatch defaults to 10.
	// Zero = leave clawpatch's default.
	Jobs int
	// Since restricts review to features touched after a git ref.
	// Empty = full review.
	Since string
	// Feature targets a single feature ID. Empty = all features.
	Feature string
	// DryRun runs without invoking the provider. Useful for plumbing tests.
	DryRun bool
	// PromptFile passes through `--prompt-file <path>` (clawpatch PR #64,
	// landed upstream 2026-05-18). Empty = no extra reviewer guidance.
	// Use "-" to read from stdin.
	PromptFile string
	// ExportTribunalLedger passes through `--export-tribunal-ledger <path>`
	// (clawpatch PR #65, landed upstream 2026-05-18). Empty = no export.
	// The emitted JSONL contains thin signed-ledger-shaped entries (no
	// body / evidence / reasoning); useful for a downstream consumer that
	// wants ledger entries without rendering lens reports. The lens path
	// still needs ListFindings for full bodies.
	ExportTribunalLedger string
}

// ExportEntry mirrors a single line of the JSONL emitted by
// `clawpatch review --export-tribunal-ledger`. Field shapes match the
// schema documented inline at maybeExportTribunalLedger in clawpatch's
// app.ts.
type ExportEntry struct {
	Kind        string  `json:"kind"`         // "clawpatch-review"
	FindingID   string  `json:"finding_id"`   // stable clawpatch id
	PlanID      *string `json:"plan_id"`      // always null on this path
	Round       int     `json:"round"`        // always 1
	AgentPubkey *string `json:"agent_pubkey"` // null — Tribunal signs on ingest
	AgentLabel  string  `json:"agent_label"`  // "clawpatch-<provider>"
	Severity    string  `json:"severity"`     // critical|high|medium|low
	Category    string  `json:"category"`     // bug|security|...
	ClaimHash   string  `json:"claim_hash"`   // clawpatch finding signature
	ClaimURI    *string `json:"claim_uri"`    // null on this path
	Stake       *int    `json:"stake"`        // null on this path
	Timestamp   string  `json:"timestamp"`    // ISO-8601
	Signature   *string `json:"signature"`    // null — Tribunal signs on ingest
	RunID       string  `json:"run_id"`
}

// FixOpts are the flags Tribunal forwards to `clawpatch fix`.
type FixOpts struct {
	// Finding is the clawpatch finding ID to fix. Required.
	Finding string
	// DryRun runs the fix planner without applying a patch.
	DryRun bool
}

// FixResult mirrors the JSON object emitted by `clawpatch fix --json`.
// Fields are tagged with omitempty because the dry-run and live shapes
// differ (dry-run carries plan + validation; live carries status +
// filesChanged + changedFiles).
type FixResult struct {
	Finding      string `json:"finding"`
	DryRun       bool   `json:"dryRun"`
	PatchAttempt string `json:"patchAttempt"`
	Plan         string `json:"plan,omitempty"`         // dry-run only
	Status       string `json:"status,omitempty"`       // live: planned|applied|failed|validated
	FilesChanged int    `json:"filesChanged,omitempty"` // live
	ChangedFiles string `json:"changedFiles,omitempty"` // live
	Commands     int    `json:"commands,omitempty"`     // live
	Validation   string `json:"validation,omitempty"`   // both shapes
	Next         string `json:"next,omitempty"`         // live
}

// RevalidateOpts are the flags Tribunal forwards to `clawpatch revalidate`.
// At most one of Finding / All / Since should be set; the runner picks the
// matching clawpatch invocation. If multiple are set, Finding wins, then
// All, then Since.
type RevalidateOpts struct {
	Finding string
	All     bool
	Since   string
	Limit   int
}

// RevalidateOutcome mirrors the per-finding result from
// `clawpatch revalidate --json`. Both the single-finding and bulk shapes
// reduce to this Go type — runner.Revalidate normalises both.
type RevalidateOutcome struct {
	Finding   string `json:"finding"`
	Outcome   string `json:"outcome"`             // open|fixed|false-positive|uncertain|wont-fix
	Reasoning string `json:"reasoning,omitempty"` // only present in single-finding mode
}
