package chain

import (
	"context"
	"fmt"

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
//  3. Submit one CommitFindingBatch and one ResolveFindingBatch per plan.
//     Either may be empty, in which case the corresponding tx is skipped.
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

	// Pre-flight: query the chain for state of each finding in this plan.
	// Findings already committed are skipped on the commit side; findings
	// already resolved are skipped on the resolve side. This makes sync
	// idempotent — retrying after a partial failure no longer dies with
	// "already committed".
	committedOnChain := map[string]bool{}
	resolvedOnChain := map[string]bool{}
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
	for id := range checkIDs {
		resp, err := s.Client.Finding(ctx, planID, id)
		if err != nil {
			// Don't fail the whole sync if a single pre-flight query
			// errors — fall back to "unknown" (treat as not committed)
			// and let the contract's own duplicate guard be the final
			// authority. This keeps sync resilient to flaky REST.
			continue
		}
		if resp.Finding == nil {
			continue
		}
		committedOnChain[id] = true
		if resp.Finding.Resolution != nil {
			resolvedOnChain[id] = true
		}
	}

	// Build commits (skip findings already on-chain).
	var commits []FindingCommit
	seen := map[string]struct{}{}
	for _, f := range findings {
		if f.PlanID != planID {
			continue
		}
		if _, dup := seen[f.FindingID]; dup {
			continue
		}
		seen[f.FindingID] = struct{}{}
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
	// Fold in queue entries that are commits (also skip if already on-chain).
	for _, q := range queued {
		if q.Msg == nil || q.Msg.CommitFinding == nil {
			continue
		}
		if _, dup := seen[q.Msg.CommitFinding.FindingID]; dup {
			continue
		}
		seen[q.Msg.CommitFinding.FindingID] = struct{}{}
		if committedOnChain[q.Msg.CommitFinding.FindingID] {
			continue
		}
		commits = append(commits, *q.Msg.CommitFinding)
	}

	// Build resolutions (skip ones already resolved on-chain).
	var resCommits []ResolutionCommit
	for _, r := range resolutions {
		if r.PlanID != planID {
			continue
		}
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
		msg := &ExecuteMsg{CommitFindingBatch: &CommitBatchMsg{PlanID: planID, Findings: commits}}
		br, err := s.Client.Execute(ctx, msg)
		if err != nil {
			return nil, fmt.Errorf("commit batch (plan=%s, n=%d): %w", planID, len(commits), err)
		}
		result.FindingsSent = len(commits)
		result.CommitTxHash = br.TxHash
	}

	if len(resCommits) > 0 {
		msg := &ExecuteMsg{ResolveFindingBatch: &ResolveBatchMsg{PlanID: planID, Resolutions: resCommits}}
		br, err := s.Client.Execute(ctx, msg)
		if err != nil {
			return nil, fmt.Errorf("resolve batch (plan=%s, n=%d): %w", planID, len(resCommits), err)
		}
		result.ResolutionsSent = len(resCommits)
		result.ResolveTxHash = br.TxHash
	}

	return result, nil
}

// SyncAll groups every entry in the ledger by plan_id and runs SyncPlan
// per group. Returns one SyncResult per plan, in the order plans first
// appear in the ledger.
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
	for _, planID := range planOrder {
		res, err := s.SyncPlan(ctx, planID, planFindings[planID], planResolutions[planID])
		if err != nil {
			return out, err
		}
		out = append(out, res)
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
