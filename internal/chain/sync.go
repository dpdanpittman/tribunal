package chain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// KeyResolver returns the keypair for a given canonical pubkey string
// ("ed25519:<hex>"). The CLI wires this to internal/agent.Registry; tests
// pass a map-based stub.
type KeyResolver interface {
	KeypairFor(pubkey string) (*agent.Keypair, error)
}

// Sync is the hybrid settlement orchestrator. Given a ledger plus an
// (optional) queue of failed real-time commits, it builds + submits the
// per-plan batched ExecuteMsgs that flush the local state to chain.
//
// Settlement layers, in order:
//  1. Drain any queued real-time commits for this plan into the batch.
//  2. Translate every Finding + Resolution in the ledger for the plan into
//     the contract wire format.
//  3. Pre-flight chain queries (parallel, bounded) to skip entries that
//     are already on-chain. Fast path for normal retries.
//  4. Submit batched CommitFindingBatch and ResolveFindingBatch via
//     submitCommitBatch / submitResolveBatch. On rejection these query
//     the contract's actual state (same primitive as pre-flight),
//     filter out entries the contract now considers committed/resolved,
//     and retry. v0.3.4 switched from regex-on-raw_log to this
//     contract-state-query primitive because the regex approach was
//     narrower than the contract's identifier grammar (P-v033-audit
//     F-ARCH-301) and trusted LCD-sourced text (F-SEC-301).
type Sync struct {
	Client *Client
	Keys   KeyResolver
	Queue  *Queue
}

// SyncResult summarizes one plan's settlement.
type SyncResult struct {
	PlanID            string
	FindingsSent      int
	ResolutionsSent   int
	CommitTxHash      string
	ResolveTxHash     string
	QueueDrainedCount int
}

// defaultPreflightConcurrency caps the number of in-flight LCD pre-flight
// queries when no operator-configured value is set. Keeps a large batch
// (say 100 findings) from saturating the LCD while still cutting latency
// vs. the serial approach in v0.3.2.
const defaultPreflightConcurrency = 8

// perPlanSyncBudget is the wallclock budget allotted to a single plan
// inside SyncAll. SyncAll derives a child ctx per plan from this constant
// so that one slow plan's recovery cycle doesn't starve subsequent plans
// of the caller's outer ctx. The caller's outer ctx still binds — this
// is the additional per-plan cap on top of it. P-v033-audit's F-NEW-401.
const perPlanSyncBudget = 90 * time.Second

// SyncBudgetForPlans returns an upper bound on the wallclock budget needed
// to sync n plans serially via SyncAll, sized so the outer ctx is never
// the binding constraint when each plan stays inside perPlanSyncBudget.
// Includes ~20% slack on top of the worst case, plus a floor so a
// single-plan invocation gets some headroom for slow LCDs.
//
// F-OPUS-002: callers that derive an outer ctx from a fixed minute count
// (the v0.3.4 CLI used 5m) silently truncate plans 4+ when the per-plan
// budget could legitimately stretch the total above the outer bound.
// Scaling per-plan removes that mismatch.
func SyncBudgetForPlans(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	const floor = 5 * time.Minute
	raw := time.Duration(n) * perPlanSyncBudget * 12 / 10
	if raw < floor {
		return floor
	}
	return raw
}

// CommitRealtime submits a single FindingCommit immediately and, on
// failure, persists the message to the retry queue so plan-close sync
// picks it up. Used for critical-severity findings where the round-trip
// can't wait for plan close.
func (s *Sync) CommitRealtime(ctx context.Context, f *ledger.Finding) error {
	kp, err := s.Keys.KeypairFor(f.AgentPubkey)
	if err != nil {
		return fmt.Errorf("realtime commit: resolve key for %s: %w", f.AgentPubkey, err)
	}
	commit, err := BuildFindingCommit(f, kp)
	if err != nil {
		return fmt.Errorf("realtime commit: build msg: %w", err)
	}
	msg := &ExecuteMsg{CommitFinding: commit}
	if _, err := s.Client.Execute(ctx, msg); err != nil {
		// Best-effort queue. If even queueing fails, surface both errors.
		qErr := s.queueFailure(f.PlanID, f.FindingID, err.Error(), msg)
		if qErr != nil {
			return fmt.Errorf("realtime commit failed and queue write failed: chain=%w queue=%v", err, qErr)
		}
		return fmt.Errorf("realtime commit failed (queued for retry): %w", err)
	}
	return nil
}

// SyncPlan submits the batched commits + resolutions for a single plan.
// Pulls in queued retries for that plan as well. Returns the per-plan
// summary; nil error means both batches succeeded (or were empty).
func (s *Sync) SyncPlan(ctx context.Context, planID string, findings []*ledger.Finding, resolutions []*ledger.Resolution) (*SyncResult, error) {
	result := &SyncResult{PlanID: planID}

	// Drain queue first so requeued commits land in this batch.
	var queued []QueueEntry
	if s.Queue != nil {
		drained, err := s.Queue.Drain(planID)
		if err != nil {
			return nil, fmt.Errorf("drain queue: %w", err)
		}
		queued = drained
		result.QueueDrainedCount = len(drained)
	}

	// Pre-flight: parallel chain queries for each finding in this plan.
	// Findings already committed are skipped on the commit side; findings
	// already resolved are skipped on the resolve side. Pre-flight errors
	// are tolerated here because submitCommitBatch / submitResolveBatch
	// absorb the resulting "already committed" / "already resolved" errors
	// from the contract — F5's idempotency is two-layered in v0.3.3.
	checkIDs := map[string]struct{}{}
	for _, f := range findings {
		if f.PlanID == planID {
			checkIDs[f.FindingID] = struct{}{}
		}
	}
	for _, r := range resolutions {
		if r.PlanID == planID {
			checkIDs[r.FindingID] = struct{}{}
		}
	}
	for _, q := range queued {
		if q.Msg != nil && q.Msg.CommitFinding != nil {
			checkIDs[q.Msg.CommitFinding.FindingID] = struct{}{}
		}
	}
	committedOnChain, resolvedOnChain := s.preflight(ctx, planID, checkIDs)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("pre-flight cancelled: %w", ctxErr)
	}

	// Build commits (skip findings already on-chain).
	var commits []FindingCommit
	seenCommit := map[string]struct{}{}
	for _, f := range findings {
		if f.PlanID != planID {
			continue
		}
		if _, dup := seenCommit[f.FindingID]; dup {
			continue
		}
		seenCommit[f.FindingID] = struct{}{}
		if state := committedOnChain[f.FindingID]; state != nil {
			if err := verifyOnChainCommit(f, state); err != nil {
				return nil, fmt.Errorf("preflight conflict (plan=%s): %w", planID, err)
			}
			continue
		}
		kp, err := s.Keys.KeypairFor(f.AgentPubkey)
		if err != nil {
			return nil, fmt.Errorf("resolve key for finding %s: %w", f.FindingID, err)
		}
		commit, err := BuildFindingCommit(f, kp)
		if err != nil {
			return nil, fmt.Errorf("build finding commit %s: %w", f.FindingID, err)
		}
		commits = append(commits, *commit)
	}
	for _, q := range queued {
		if q.Msg == nil || q.Msg.CommitFinding == nil {
			continue
		}
		if _, dup := seenCommit[q.Msg.CommitFinding.FindingID]; dup {
			continue
		}
		seenCommit[q.Msg.CommitFinding.FindingID] = struct{}{}
		if state := committedOnChain[q.Msg.CommitFinding.FindingID]; state != nil {
			// Queued real-time commits don't carry the original ledger
			// claim_hash separately from the message itself, so compare
			// the message's claim_hash directly to the on-chain state.
			if state.ClaimHash != q.Msg.CommitFinding.ClaimHash {
				return nil, fmt.Errorf("preflight conflict (plan=%s) for queued commit %s: claim_hash mismatch (local=%q, on-chain=%q)",
					planID, q.Msg.CommitFinding.FindingID, q.Msg.CommitFinding.ClaimHash, state.ClaimHash)
			}
			continue
		}
		commits = append(commits, *q.Msg.CommitFinding)
	}

	// Build resolutions (dedup + skip ones already resolved on-chain).
	var resCommits []ResolutionCommit
	seenResolve := map[string]struct{}{}
	for _, r := range resolutions {
		if r.PlanID != planID {
			continue
		}
		if _, dup := seenResolve[r.FindingID]; dup {
			continue
		}
		seenResolve[r.FindingID] = struct{}{}
		if rec := resolvedOnChain[r.FindingID]; rec != nil {
			if err := verifyOnChainResolution(r, rec); err != nil {
				return nil, fmt.Errorf("preflight conflict (plan=%s): %w", planID, err)
			}
			continue
		}
		kp, err := s.Keys.KeypairFor(r.ResolverPubkey)
		if err != nil {
			return nil, fmt.Errorf("resolve key for resolution %s: %w", r.FindingID, err)
		}
		rc, err := BuildResolutionCommit(r, kp)
		if err != nil {
			return nil, fmt.Errorf("build resolution commit %s: %w", r.FindingID, err)
		}
		resCommits = append(resCommits, *rc)
	}

	if len(commits) > 0 {
		br, sent, err := s.submitCommitBatch(ctx, planID, commits)
		if err != nil {
			return nil, fmt.Errorf("commit batch (plan=%s, n=%d): %w", planID, len(commits), err)
		}
		result.FindingsSent = sent
		if br != nil {
			result.CommitTxHash = br.TxHash
		}
	}

	if len(resCommits) > 0 {
		br, sent, err := s.submitResolveBatch(ctx, planID, resCommits)
		if err != nil {
			return nil, fmt.Errorf("resolve batch (plan=%s, n=%d): %w", planID, len(resCommits), err)
		}
		result.ResolutionsSent = sent
		if br != nil {
			result.ResolveTxHash = br.TxHash
		}
	}

	return result, nil
}

// preflight queries the chain in parallel (bounded fan-out) for the state
// of each finding ID. Per-query errors are tolerated — they fall through
// to submitCommitBatch / submitResolveBatch which absorb the resulting
// duplicate-rejection errors from the contract. After progress-threshold
// elapses, emits a stderr note so operators see the loop is alive.
//
// v0.3.5: returns the full on-chain state (FindingState / ResolutionRecord)
// per id instead of opaque booleans. Callers verify the on-chain claim_hash
// + agent_pubkey + severity + stake match the local commit before trusting
// "already committed" reports. A hostile LCD that fabricates a committed
// response is caught at verifyOnChainCommit; without verification it could
// silently drop a finding from sync at both call sites — recovery (already
// addressed in v0.3.4) and the success path (F-OPUS-001). A nil entry in
// either map means the LCD did not report on-chain state for that id.
func (s *Sync) preflight(ctx context.Context, planID string, ids map[string]struct{}) (committed map[string]*FindingState, resolved map[string]*ResolutionRecord) {
	committed = map[string]*FindingState{}
	resolved = map[string]*ResolutionRecord{}
	if len(ids) == 0 {
		return
	}

	type result struct {
		id    string
		state *FindingState
	}

	idCh := make(chan string, len(ids))
	for id := range ids {
		idCh <- id
	}
	close(idCh)

	resCh := make(chan result, len(ids))
	var wg sync.WaitGroup
	workers := defaultPreflightConcurrency
	if s.Client != nil && s.Client.Config() != nil && s.Client.Config().PreflightConcurrency > 0 {
		workers = s.Client.Config().PreflightConcurrency
	}
	if len(ids) < workers {
		workers = len(ids)
	}

	start := time.Now()
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for id := range idCh {
				if ctx.Err() != nil {
					return
				}
				attemptCtx, cancel := context.WithTimeout(ctx, preflightAttemptTimeout)
				resp, err := s.Client.Finding(attemptCtx, planID, id)
				cancel()
				if err != nil || resp == nil || resp.Finding == nil {
					resCh <- result{id: id}
					continue
				}
				resCh <- result{id: id, state: resp.Finding}
			}
		}()
	}

	// Progress signal for slow LCDs.
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(waitProgressInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, "tribunal: still pre-flighting plan=%s (elapsed=%s, ids=%d)\n",
					planID, time.Since(start).Round(time.Second), len(ids))
			}
		}
	}()

	wg.Wait()
	close(resCh)
	close(done)

	for r := range resCh {
		if r.state != nil {
			committed[r.id] = r.state
			if r.state.Resolution != nil {
				resolved[r.id] = r.state.Resolution
			}
		}
	}
	return
}

// verifyOnChainCommit compares an on-chain FindingState against the local
// ledger.Finding the operator is about to commit. Used by both the
// preflight success path and the post-rejection recovery paths to refuse
// a hostile-LCD silent drop: if the LCD reports a finding as already
// committed but its on-chain claim_hash / agent_pubkey / severity / stake
// don't match what we'd submit, we treat the report as adversarial and
// surface it instead of trusting it.
//
// Returns nil if the on-chain entry matches; a non-nil error names the
// first mismatched field.
func verifyOnChainCommit(local *ledger.Finding, onChain *FindingState) error {
	if local == nil || onChain == nil {
		return fmt.Errorf("verify commit: nil input (local=%v, onChain=%v)", local == nil, onChain == nil)
	}
	if local.ClaimHash != onChain.ClaimHash {
		return fmt.Errorf("verify commit %s: claim_hash mismatch (local=%q, on-chain=%q)", local.FindingID, local.ClaimHash, onChain.ClaimHash)
	}
	wirePub, err := PubkeyToWire(local.AgentPubkey)
	if err != nil {
		return fmt.Errorf("verify commit %s: local agent_pubkey not parseable: %w", local.FindingID, err)
	}
	if wirePub != onChain.AgentPubkey {
		return fmt.Errorf("verify commit %s: agent_pubkey mismatch (local=%q, on-chain=%q)", local.FindingID, wirePub, onChain.AgentPubkey)
	}
	if string(local.Severity) != onChain.Severity {
		return fmt.Errorf("verify commit %s: severity mismatch (local=%q, on-chain=%q)", local.FindingID, local.Severity, onChain.Severity)
	}
	localStake := strconv.FormatUint(uint64(local.Stake), 10)
	if localStake != onChain.Stake {
		return fmt.Errorf("verify commit %s: stake mismatch (local=%q, on-chain=%q)", local.FindingID, localStake, onChain.Stake)
	}
	return nil
}

// verifyOnChainResolution is the resolution-side equivalent of
// verifyOnChainCommit — guards against a hostile LCD claiming a resolution
// exists on-chain whose evidence_hash / outcome / resolver_pubkey differ
// from what the operator's about to submit.
func verifyOnChainResolution(local *ledger.Resolution, onChain *ResolutionRecord) error {
	if local == nil || onChain == nil {
		return fmt.Errorf("verify resolution: nil input (local=%v, onChain=%v)", local == nil, onChain == nil)
	}
	if local.EvidenceHash != onChain.EvidenceHash {
		return fmt.Errorf("verify resolution %s: evidence_hash mismatch (local=%q, on-chain=%q)", local.FindingID, local.EvidenceHash, onChain.EvidenceHash)
	}
	if string(local.Outcome) != onChain.Outcome {
		return fmt.Errorf("verify resolution %s: outcome mismatch (local=%q, on-chain=%q)", local.FindingID, local.Outcome, onChain.Outcome)
	}
	wirePub, err := PubkeyToWire(local.ResolverPubkey)
	if err != nil {
		return fmt.Errorf("verify resolution %s: local resolver_pubkey not parseable: %w", local.FindingID, err)
	}
	if wirePub != onChain.ResolverPubkey {
		return fmt.Errorf("verify resolution %s: resolver_pubkey mismatch (local=%q, on-chain=%q)", local.FindingID, wirePub, onChain.ResolverPubkey)
	}
	return nil
}

// maxRecoveryAttempts caps the number of batch-level retries the recovery
// layer will perform regardless of batch size. v0.3.3 bounded recovery by
// len(batch), which let a hostile LCD amplify gas consumption against
// large batches; v0.3.4 caps at a constant so worst-case cost is bounded.
// Five attempts handles every realistic partial-failure scenario: each
// retry drops at least one entry, so five retries can absorb up to ~5
// duplicate findings in any single sync, with the remainder surfacing as
// an explicit error for operator inspection.
const maxRecoveryAttempts = 5

// submitCommitBatch posts a commit_finding_batch and, on rejection,
// queries the contract for actual per-finding state and retries with
// duplicates filtered out. Bounded by maxRecoveryAttempts.
//
// v0.3.4: replaced v0.3.3's regex-on-raw_log recovery with a structured
// contract-state-query primitive. The old approach parsed the contract's
// FindingAlreadyCommitted error string with a regex tied to a specific
// identifier character set, which had two structural problems:
//
//  1. The regex character class didn't agree with the contract's
//     validate_id_field rules (it rejected slashes in plan_id and spaces
//     in finding_id, both of which the contract permits) — P-v033-audit's
//     F-ARCH-301 Critical.
//  2. The raw_log was LCD-sourced text; a hostile LCD could choose which
//     finding the operator drops on retry — P-v033-audit's F-SEC-301.
//
// The new approach: on Execute rejection, re-run the preflight (same
// primitive used on the success path) to ask the contract authoritatively
// which findings are now committed. Filter the batch down to the
// uncommitted set and retry. If the contract's view doesn't differ from
// the batch (no duplicates), the rejection is for some other reason and
// we surface it.
//
// Returns the final broadcast result, the count of findings actually
// committed (excludes filtered duplicates), and an error if the batch
// could not be made to land for a reason unrelated to duplicates.
//
// v0.3.5: chunks at maxBatchChunkSize before invoking the recovery loop.
// The contract enforces MAX_BATCH_SIZE=100; a plan with >100 commits used
// to hit BatchTooLarge on every attempt and the structured-query recovery
// couldn't help (preflight returns no committed entries for fresh ones,
// so the loop bails immediately). Chunking on the client side keeps each
// tx under the contract limit. On partial failure the function returns
// the chunks that did land along with the underlying error so callers
// can render partial progress (F-OPUS-003).
func (s *Sync) submitCommitBatch(ctx context.Context, planID string, commits []FindingCommit) (*BroadcastResult, int, error) {
	if len(commits) == 0 {
		return nil, 0, nil
	}
	chunks := chunkFindingCommits(commits)
	if len(chunks) == 1 {
		return s.submitCommitChunk(ctx, planID, chunks[0])
	}
	var (
		hashes    []string
		totalSent int
	)
	for i, chunk := range chunks {
		br, sent, err := s.submitCommitChunk(ctx, planID, chunk)
		totalSent += sent
		if br != nil && br.TxHash != "" {
			hashes = append(hashes, br.TxHash)
		}
		if err != nil {
			agg := &BroadcastResult{TxHash: strings.Join(hashes, ",")}
			return agg, totalSent, fmt.Errorf("commit batch chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return &BroadcastResult{TxHash: strings.Join(hashes, ",")}, totalSent, nil
}

// maxBatchChunkSize is the client-side ceiling on entries per
// CommitFindingBatch / ResolveFindingBatch tx. Matches the contract's
// MAX_BATCH_SIZE in `contracts/tribunal-reputation/src/validate.rs`. If
// the contract ever raises its cap, this value can lag — exceeding it
// surfaces as a BatchTooLarge error from the contract itself.
const maxBatchChunkSize = 100

// chunkFindingCommits splits a flat commit slice into sub-slices each
// bounded by maxBatchChunkSize. Returns the input unchanged when it
// already fits in one chunk. Pure function, exposed for unit testing of
// the F-OPUS-003 fix.
func chunkFindingCommits(commits []FindingCommit) [][]FindingCommit {
	if len(commits) <= maxBatchChunkSize {
		return [][]FindingCommit{commits}
	}
	chunks := make([][]FindingCommit, 0, (len(commits)+maxBatchChunkSize-1)/maxBatchChunkSize)
	for i := 0; i < len(commits); i += maxBatchChunkSize {
		end := i + maxBatchChunkSize
		if end > len(commits) {
			end = len(commits)
		}
		chunks = append(chunks, commits[i:end])
	}
	return chunks
}

// chunkResolutionCommits is the resolution-side equivalent of chunkFindingCommits.
func chunkResolutionCommits(resCommits []ResolutionCommit) [][]ResolutionCommit {
	if len(resCommits) <= maxBatchChunkSize {
		return [][]ResolutionCommit{resCommits}
	}
	chunks := make([][]ResolutionCommit, 0, (len(resCommits)+maxBatchChunkSize-1)/maxBatchChunkSize)
	for i := 0; i < len(resCommits); i += maxBatchChunkSize {
		end := i + maxBatchChunkSize
		if end > len(resCommits) {
			end = len(resCommits)
		}
		chunks = append(chunks, resCommits[i:end])
	}
	return chunks
}

// submitCommitChunk is the single-chunk recovery loop — the body of what
// submitCommitBatch was in v0.3.4. The outer chunking lives in
// submitCommitBatch (v0.3.5).
func (s *Sync) submitCommitChunk(ctx context.Context, planID string, commits []FindingCommit) (*BroadcastResult, int, error) {
	var lastBroadcastErr error
	for attempt := 0; attempt < maxRecoveryAttempts; attempt++ {
		if len(commits) == 0 {
			// Everything in the batch was already on-chain. No tx needed.
			return nil, 0, nil
		}
		msg := &ExecuteMsg{CommitFindingBatch: &CommitBatchMsg{PlanID: planID, Findings: commits}}
		br, err := s.Client.Execute(ctx, msg)
		if err == nil {
			return br, len(commits), nil
		}

		// Recovery: query the contract for actual state of every entry in
		// the batch. Anything the contract considers committed AND matches
		// the local copy gets filtered; a mismatch means the LCD is lying
		// (or another party committed a different payload under the same
		// finding_id) and we surface it instead of trusting the report.
		ids := map[string]struct{}{}
		for _, c := range commits {
			ids[c.FindingID] = struct{}{}
		}
		committed, _ := s.preflight(ctx, planID, ids)
		filtered := commits[:0]
		for _, c := range commits {
			state := committed[c.FindingID]
			if state == nil {
				filtered = append(filtered, c)
				continue
			}
			// LCD says committed — verify it matches the commit we built.
			if state.ClaimHash != c.ClaimHash || state.AgentPubkey != c.AgentPubkey || state.Severity != c.Severity || state.Stake != c.Stake {
				return br, 0, fmt.Errorf("commit batch recovery: on-chain state for %s disagrees with local commit (claim_hash on-chain=%q local=%q): %w",
					c.FindingID, state.ClaimHash, c.ClaimHash, err)
			}
			// Genuine duplicate — safe to drop from retry.
		}
		if len(filtered) == len(commits) {
			// No duplicates explain the rejection. Surface the underlying error.
			return br, 0, fmt.Errorf("commit batch rejected and no entries already on-chain: %w", err)
		}
		fmt.Fprintf(os.Stderr, "tribunal: commit batch recovered via state query, dropped %d already-committed, retrying with %d findings\n",
			len(commits)-len(filtered), len(filtered))
		lastBroadcastErr = err
		commits = filtered
	}
	remainingIDs := make([]string, 0, len(commits))
	for _, c := range commits {
		remainingIDs = append(remainingIDs, c.FindingID)
	}
	return nil, 0, fmt.Errorf("commit batch exhausted recovery attempts (cap=%d, remaining=%d %v): last_error=%w",
		maxRecoveryAttempts, len(commits), remainingIDs, lastBroadcastErr)
}

// submitResolveBatch is the resolution-side equivalent of submitCommitBatch.
// Uses the same structured-query recovery primitive — on rejection, queries
// the contract for which findings already have resolutions and filters them
// out of the retry. Bounded by maxRecoveryAttempts.
//
// v0.3.5: chunks at maxBatchChunkSize before delegating to submitResolveChunk,
// mirroring submitCommitBatch's F-OPUS-003 fix.
func (s *Sync) submitResolveBatch(ctx context.Context, planID string, resCommits []ResolutionCommit) (*BroadcastResult, int, error) {
	if len(resCommits) == 0 {
		return nil, 0, nil
	}
	chunks := chunkResolutionCommits(resCommits)
	if len(chunks) == 1 {
		return s.submitResolveChunk(ctx, planID, chunks[0])
	}
	var (
		hashes    []string
		totalSent int
	)
	for i, chunk := range chunks {
		br, sent, err := s.submitResolveChunk(ctx, planID, chunk)
		totalSent += sent
		if br != nil && br.TxHash != "" {
			hashes = append(hashes, br.TxHash)
		}
		if err != nil {
			agg := &BroadcastResult{TxHash: strings.Join(hashes, ",")}
			return agg, totalSent, fmt.Errorf("resolve batch chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return &BroadcastResult{TxHash: strings.Join(hashes, ",")}, totalSent, nil
}

// submitResolveChunk is the single-chunk recovery loop — body of v0.3.4's
// submitResolveBatch. Chunking lives in submitResolveBatch (v0.3.5).
func (s *Sync) submitResolveChunk(ctx context.Context, planID string, resCommits []ResolutionCommit) (*BroadcastResult, int, error) {
	var lastBroadcastErr error
	for attempt := 0; attempt < maxRecoveryAttempts; attempt++ {
		if len(resCommits) == 0 {
			return nil, 0, nil
		}
		msg := &ExecuteMsg{ResolveFindingBatch: &ResolveBatchMsg{PlanID: planID, Resolutions: resCommits}}
		br, err := s.Client.Execute(ctx, msg)
		if err == nil {
			return br, len(resCommits), nil
		}

		// Recovery via contract-state query. resolved map carries the
		// on-chain ResolutionRecord per finding_id; a mismatch means the
		// LCD is asserting a resolution exists that doesn't match what
		// we'd submit, and we surface it instead of trusting it.
		ids := map[string]struct{}{}
		for _, r := range resCommits {
			ids[r.FindingID] = struct{}{}
		}
		_, resolved := s.preflight(ctx, planID, ids)
		filtered := resCommits[:0]
		for _, r := range resCommits {
			rec := resolved[r.FindingID]
			if rec == nil {
				filtered = append(filtered, r)
				continue
			}
			if rec.EvidenceHash != r.EvidenceHash || rec.Outcome != r.Outcome || rec.ResolverPubkey != r.ResolverPubkey {
				return br, 0, fmt.Errorf("resolve batch recovery: on-chain resolution for %s disagrees with local commit (evidence_hash on-chain=%q local=%q): %w",
					r.FindingID, rec.EvidenceHash, r.EvidenceHash, err)
			}
		}
		if len(filtered) == len(resCommits) {
			return br, 0, fmt.Errorf("resolve batch rejected and no entries already resolved: %w", err)
		}
		fmt.Fprintf(os.Stderr, "tribunal: resolve batch recovered via state query, dropped %d already-resolved, retrying with %d resolutions\n",
			len(resCommits)-len(filtered), len(filtered))
		lastBroadcastErr = err
		resCommits = filtered
	}
	remainingIDs := make([]string, 0, len(resCommits))
	for _, r := range resCommits {
		remainingIDs = append(remainingIDs, r.FindingID)
	}
	return nil, 0, fmt.Errorf("resolve batch exhausted recovery attempts (cap=%d, remaining=%d %v): last_error=%w",
		maxRecoveryAttempts, len(resCommits), remainingIDs, lastBroadcastErr)
}

// SyncAll groups every entry in the ledger by plan_id and runs SyncPlan
// per group. Returns one SyncResult per plan, in the order plans first
// appear in the ledger.
//
// In v0.3.3 SyncAll continues past per-plan failures instead of aborting
// — a single bad plan no longer blocks every subsequent plan from being
// settled. The returned error wraps the per-plan errors via errors.Join;
// the returned results slice carries the successful plans in their
// original order.
func (s *Sync) SyncAll(ctx context.Context, lg *ledger.Ledger) ([]*SyncResult, error) {
	findings, resolutions, err := lg.All()
	if err != nil {
		return nil, fmt.Errorf("read ledger: %w", err)
	}
	planOrder := []string{}
	seenPlan := map[string]struct{}{}
	planFindings := map[string][]*ledger.Finding{}
	planResolutions := map[string][]*ledger.Resolution{}
	// v0.5.6: trajectory findings (TrajectoryID set, PlanID empty) stay
	// local-only. Skip them at the SyncAll grouping step so we never try
	// to submit an empty-plan_id batch.
	skippedTrajectoryFindings := 0
	skippedTrajectoryResolutions := 0
	for _, f := range findings {
		if f.PlanID == "" && f.TrajectoryID != "" {
			skippedTrajectoryFindings++
			continue
		}
		if _, ok := seenPlan[f.PlanID]; !ok {
			seenPlan[f.PlanID] = struct{}{}
			planOrder = append(planOrder, f.PlanID)
		}
		planFindings[f.PlanID] = append(planFindings[f.PlanID], f)
	}
	for _, r := range resolutions {
		if r.PlanID == "" && r.TrajectoryID != "" {
			skippedTrajectoryResolutions++
			continue
		}
		if _, ok := seenPlan[r.PlanID]; !ok {
			seenPlan[r.PlanID] = struct{}{}
			planOrder = append(planOrder, r.PlanID)
		}
		planResolutions[r.PlanID] = append(planResolutions[r.PlanID], r)
	}
	if skippedTrajectoryFindings+skippedTrajectoryResolutions > 0 {
		// Best-effort surfacing — operator should know auxiliary local-only
		// state exists. Trajectory findings settle locally; chain settlement
		// is plan-scoped only until the contract grows trajectory support.
		fmt.Fprintf(os.Stderr, "chain sync: skipped %d trajectory findings + %d trajectory resolutions (local-only by design)\n",
			skippedTrajectoryFindings, skippedTrajectoryResolutions)
	}

	var out []*SyncResult
	var errs []error
	for _, planID := range planOrder {
		// Per-plan ctx with bounded budget so a slow plan's recovery cycle
		// can't starve subsequent plans of the caller's outer ctx
		// (P-v033-audit F-NEW-401). The outer ctx still binds: if the
		// caller's ctx is shorter than perPlanSyncBudget, this WithTimeout
		// resolves to the caller's deadline.
		planCtx, planCancel := context.WithTimeout(ctx, perPlanSyncBudget)
		res, err := s.SyncPlan(planCtx, planID, planFindings[planID], planResolutions[planID])
		planCancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("plan %s: %w", planID, err))
			continue
		}
		out = append(out, res)
	}
	if len(errs) > 0 {
		return out, errors.Join(errs...)
	}
	return out, nil
}

func (s *Sync) queueFailure(planID, findingID, reason string, msg *ExecuteMsg) error {
	if s.Queue == nil {
		return nil
	}
	return s.Queue.Enqueue(QueueEntry{
		PlanID:    planID,
		FindingID: findingID,
		Reason:    reason,
		Msg:       msg,
	})
}
