package chain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
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
//     submitCommitBatch / submitResolveBatch, which absorb pre-flight
//     false-negatives by parsing the contract's `FindingAlreadyCommitted`
//     / `FindingAlreadyResolved` errors and retrying the batch with the
//     offending entry dropped.
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

// preflightConcurrency caps the number of in-flight LCD pre-flight queries.
// Keeps a large batch (say 100 findings) from saturating the LCD while
// still cutting latency significantly vs. the serial approach in v0.3.2.
const preflightConcurrency = 8

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
		if committedOnChain[f.FindingID] {
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
		if committedOnChain[q.Msg.CommitFinding.FindingID] {
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
		if resolvedOnChain[r.FindingID] {
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
func (s *Sync) preflight(ctx context.Context, planID string, ids map[string]struct{}) (committed, resolved map[string]bool) {
	committed = map[string]bool{}
	resolved = map[string]bool{}
	if len(ids) == 0 {
		return
	}

	type result struct {
		id        string
		committed bool
		resolved  bool
	}

	idCh := make(chan string, len(ids))
	for id := range ids {
		idCh <- id
	}
	close(idCh)

	resCh := make(chan result, len(ids))
	var wg sync.WaitGroup
	workers := preflightConcurrency
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
				resCh <- result{id: id, committed: true, resolved: resp.Finding.Resolution != nil}
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
		if r.committed {
			committed[r.id] = true
		}
		if r.resolved {
			resolved[r.id] = true
		}
	}
	return
}

// alreadyCommittedRE captures the contract's FindingAlreadyCommitted error
// payload so the recovery layer can identify which finding to drop.
var alreadyCommittedRE = regexp.MustCompile(`finding ([^/]+)/([^ ]+) already committed`)

// alreadyResolvedRE captures the contract's FindingAlreadyResolved error.
var alreadyResolvedRE = regexp.MustCompile(`finding ([^/]+)/([^ ]+) already resolved`)

// submitCommitBatch posts a commit_finding_batch and, on
// "FindingAlreadyCommitted" rejection, drops the offending entry and
// retries. Bounded by len(commits) retries (each retry guarantees at
// least one entry leaves the batch), so termination is guaranteed.
//
// Returns the final broadcast result, the count of findings actually
// committed (excludes dropped duplicates), and an error if the batch
// could not be made to land for some reason other than duplicate
// rejection.
func (s *Sync) submitCommitBatch(ctx context.Context, planID string, commits []FindingCommit) (*BroadcastResult, int, error) {
	originalLen := len(commits)
	for attempt := 0; attempt <= originalLen; attempt++ {
		if len(commits) == 0 {
			// Everything in the batch was already on-chain. No tx needed.
			return nil, 0, nil
		}
		msg := &ExecuteMsg{CommitFindingBatch: &CommitBatchMsg{PlanID: planID, Findings: commits}}
		br, err := s.Client.Execute(ctx, msg)
		if err == nil {
			return br, len(commits), nil
		}
		dupID, ok := matchDuplicate(err.Error(), alreadyCommittedRE)
		if !ok {
			return br, 0, err
		}
		filtered := commits[:0]
		for _, c := range commits {
			if c.FindingID != dupID {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == len(commits) {
			// Contract reported a duplicate not in our batch. Bail loudly.
			return br, 0, fmt.Errorf("contract reported duplicate commit %q not in batch: %w", dupID, err)
		}
		fmt.Fprintf(os.Stderr, "tribunal: commit batch recovered from duplicate %s/%s, retrying with %d findings\n",
			planID, dupID, len(filtered))
		commits = filtered
	}
	return nil, 0, fmt.Errorf("commit batch exhausted recovery attempts (started=%d)", originalLen)
}

// submitResolveBatch is the resolution-side equivalent of submitCommitBatch.
// Absorbs FindingAlreadyResolved rejections by dropping the offending entry
// and retrying. Bounded by len(resCommits) retries.
func (s *Sync) submitResolveBatch(ctx context.Context, planID string, resCommits []ResolutionCommit) (*BroadcastResult, int, error) {
	originalLen := len(resCommits)
	for attempt := 0; attempt <= originalLen; attempt++ {
		if len(resCommits) == 0 {
			return nil, 0, nil
		}
		msg := &ExecuteMsg{ResolveFindingBatch: &ResolveBatchMsg{PlanID: planID, Resolutions: resCommits}}
		br, err := s.Client.Execute(ctx, msg)
		if err == nil {
			return br, len(resCommits), nil
		}
		dupID, ok := matchDuplicate(err.Error(), alreadyResolvedRE)
		if !ok {
			return br, 0, err
		}
		filtered := resCommits[:0]
		for _, r := range resCommits {
			if r.FindingID != dupID {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == len(resCommits) {
			return br, 0, fmt.Errorf("contract reported duplicate resolution %q not in batch: %w", dupID, err)
		}
		fmt.Fprintf(os.Stderr, "tribunal: resolve batch recovered from duplicate %s/%s, retrying with %d resolutions\n",
			planID, dupID, len(filtered))
		resCommits = filtered
	}
	return nil, 0, fmt.Errorf("resolve batch exhausted recovery attempts (started=%d)", originalLen)
}

// matchDuplicate inspects an error message for a known "already X"
// pattern emitted by the contract and returns the offending finding_id.
// The plan_id capture group is discarded — the caller knows the planID.
func matchDuplicate(errMsg string, re *regexp.Regexp) (string, bool) {
	m := re.FindStringSubmatch(errMsg)
	if len(m) != 3 {
		return "", false
	}
	// Strip any trailing characters from finding_id that the regex's
	// "[^ ]+" greedily captured but that aren't actually part of the ID
	// (e.g. wrapping quotes or punctuation in the xiond error text).
	fid := strings.TrimRight(m[2], "\"',;.)")
	return fid, fid != ""
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
	for _, f := range findings {
		if _, ok := seenPlan[f.PlanID]; !ok {
			seenPlan[f.PlanID] = struct{}{}
			planOrder = append(planOrder, f.PlanID)
		}
		planFindings[f.PlanID] = append(planFindings[f.PlanID], f)
	}
	for _, r := range resolutions {
		if _, ok := seenPlan[r.PlanID]; !ok {
			seenPlan[r.PlanID] = struct{}{}
			planOrder = append(planOrder, r.PlanID)
		}
		planResolutions[r.PlanID] = append(planResolutions[r.PlanID], r)
	}

	var out []*SyncResult
	var errs []error
	for _, planID := range planOrder {
		res, err := s.SyncPlan(ctx, planID, planFindings[planID], planResolutions[planID])
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
