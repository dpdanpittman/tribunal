package chain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// TestMatchDuplicate_CommitErrorParsing verifies the regex that drives
// post-broadcast batch recovery actually extracts the finding_id out of
// the contract's `FindingAlreadyCommitted` / `FindingAlreadyResolved`
// errors. If this regex drifts and these tests don't catch it, sync's
// recovery layer silently degrades to "give up on the whole batch" —
// which is exactly the v0.3.2 defect F-NEW-301 surfaced.
func TestMatchDuplicate_CommitErrorParsing(t *testing.T) {
	tests := []struct {
		name    string
		errMsg  string
		re      string
		wantID  string
		wantOK  bool
	}{
		{
			name:   "already committed plain",
			errMsg: "commit batch (plan=P-1, n=2): wait for inclusion: tx failed on-chain (code=18): finding P-1/F-99 already committed",
			re:     "commit",
			wantID: "F-99",
			wantOK: true,
		},
		{
			name:   "already committed with dotted hash",
			errMsg: "commit batch: finding P-abc/F-sec.201 already committed",
			re:     "commit",
			wantID: "F-sec.201",
			wantOK: true,
		},
		{
			name:   "already resolved",
			errMsg: "resolve batch (plan=P-7, n=1): tx failed on-chain: finding P-7/F-1 already resolved",
			re:     "resolve",
			wantID: "F-1",
			wantOK: true,
		},
		{
			name:   "unrelated error, no match",
			errMsg: "account sequence mismatch, expected 4, got 3",
			re:     "commit",
			wantOK: false,
		},
		{
			name:   "wrong regex against committed message",
			errMsg: "finding P-1/F-1 already committed",
			re:     "resolve",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var re = alreadyCommittedRE
			if tt.re == "resolve" {
				re = alreadyResolvedRE
			}
			gotID, gotOK := matchDuplicate(tt.errMsg, re)
			if gotOK != tt.wantOK {
				t.Fatalf("ok=%v want=%v (id=%q errMsg=%q)", gotOK, tt.wantOK, gotID, tt.errMsg)
			}
			if gotID != tt.wantID {
				t.Fatalf("id=%q want=%q", gotID, tt.wantID)
			}
		})
	}
}

// stubKeyResolver maps canonical pubkeys to keypairs without touching disk.
type stubKeyResolver map[string]*agent.Keypair

func (s stubKeyResolver) KeypairFor(pubkey string) (*agent.Keypair, error) {
	kp, ok := s[pubkey]
	if !ok {
		return nil, &resolverNotFoundError{pubkey: pubkey}
	}
	return kp, nil
}

type resolverNotFoundError struct{ pubkey string }

func (e *resolverNotFoundError) Error() string { return "no key for " + e.pubkey }

// fakeXiondServer fakes both the LCD REST API (queries) and the Tendermint
// /status endpoint. It does NOT simulate xiond CLI broadcast — those tests
// use a mock Client.Execute via httptest only where the LCD path is hit;
// transaction tests are covered by the CosmWasm integration tests in
// contracts/tribunal-reputation/tests/integration.rs.
func fakeLCDServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func TestClient_Reputation_ParsesEnvelope(t *testing.T) {
	srv := fakeLCDServer(t, func(w http.ResponseWriter, r *http.Request) {
		// LCD wraps contract responses in {"data": ...}.
		body := map[string]any{
			"data": map[string]any{
				"pubkey":   "AAAA",
				"balance":  "116",
				"tp_count": 2,
				"fp_count": 0,
			},
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	defer srv.Close()
	cfg := &Config{
		ChainID:         "test",
		NodeRPC:         srv.URL,
		NodeREST:        srv.URL,
		ContractAddress: "cosmwasm1xxx",
		OperatorKeyName: "x",
		GasPrices:       "0",
	}
	cfg.applyDefaults()
	client := New(cfg)

	kp := kpFromSeed(t, 0x42)
	rep, err := client.Reputation(context.Background(), kp.PublicKeyString())
	if err != nil {
		t.Fatalf("reputation: %v", err)
	}
	if rep.Balance != "116" || rep.TPCount != 2 || rep.FPCount != 0 {
		t.Fatalf("rep parse drift: %+v", rep)
	}
}

func TestClient_Status_ParsesHeight(t *testing.T) {
	srv := fakeLCDServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := `{"result":{"sync_info":{"latest_block_height":"12345"}}}`
		_, _ = w.Write([]byte(body))
	})
	defer srv.Close()
	cfg := &Config{
		ChainID: "test", NodeRPC: srv.URL, NodeREST: srv.URL,
		ContractAddress: "x", OperatorKeyName: "x", GasPrices: "0",
	}
	cfg.applyDefaults()
	client := New(cfg)

	h, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if h != 12345 {
		t.Fatalf("height: got %d, want 12345", h)
	}
}

func TestQueue_EnqueueAndDrain(t *testing.T) {
	dir := t.TempDir()
	q := NewQueue(filepath.Join(dir, "chain-queue.jsonl"))

	e1 := QueueEntry{PlanID: "P-1", FindingID: "F-1", Reason: "rpc down", Msg: &ExecuteMsg{}}
	e2 := QueueEntry{PlanID: "P-1", FindingID: "F-2", Reason: "timeout", Msg: &ExecuteMsg{}}
	e3 := QueueEntry{PlanID: "P-2", FindingID: "F-3", Reason: "rpc down", Msg: &ExecuteMsg{}}
	for _, e := range []QueueEntry{e1, e2, e3} {
		if err := q.Enqueue(e); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	all, err := q.All()
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all len: got %d, want 3", len(all))
	}

	drained, err := q.Drain("P-1")
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(drained) != 2 {
		t.Fatalf("drained: got %d, want 2", len(drained))
	}

	remaining, err := q.All()
	if err != nil {
		t.Fatalf("all after drain: %v", err)
	}
	if len(remaining) != 1 || remaining[0].PlanID != "P-2" {
		t.Fatalf("remaining: %+v", remaining)
	}
}

func TestSync_BuildsCommitsFromLedger(t *testing.T) {
	dir := t.TempDir()
	lg := ledger.New(filepath.Join(dir, "ledger.jsonl"))

	adv := kpFromSeed(t, 0x90)
	pm := kpFromSeed(t, 0x91)

	// Two findings, one resolution, all on plan P-A.
	f1 := ledger.NewFinding("F-1", "P-A", 1, adv, "adv", ledger.SeverityCritical, ledger.CategorySharedBlindSpot, "h1", "uri")
	f2 := ledger.NewFinding("F-2", "P-A", 1, adv, "adv", ledger.SeverityWarning, ledger.CategoryEdgeCase, "h2", "uri")
	if err := f1.Sign(adv); err != nil {
		t.Fatal(err)
	}
	if err := f2.Sign(adv); err != nil {
		t.Fatal(err)
	}
	if err := lg.AppendFinding(f1); err != nil {
		t.Fatal(err)
	}
	if err := lg.AppendFinding(f2); err != nil {
		t.Fatal(err)
	}
	r1 := ledger.NewResolution("F-1", "P-A", ledger.OutcomeTruePositive, pm, "pm", "ev1", "uri")
	if err := r1.Sign(pm); err != nil {
		t.Fatal(err)
	}
	if err := lg.AppendResolution(r1); err != nil {
		t.Fatal(err)
	}

	// The Sync orchestrator uses a stub Client whose Execute is fed by a
	// fake HTTP server — but since Execute shells to xiond, we test the
	// *build* step here by exercising private helpers. The integration
	// path is covered end-to-end against the local devnet via the CLI.
	resolver := stubKeyResolver{
		adv.PublicKeyString(): adv,
		pm.PublicKeyString():  pm,
	}

	findings, resolutions, err := lg.All()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	commits := make([]FindingCommit, 0)
	for _, f := range findings {
		kp, err := resolver.KeypairFor(f.AgentPubkey)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		c, err := BuildFindingCommit(f, kp)
		if err != nil {
			t.Fatalf("build commit: %v", err)
		}
		commits = append(commits, *c)
	}
	if len(commits) != 2 {
		t.Fatalf("commits: got %d, want 2", len(commits))
	}
	resCommits := make([]ResolutionCommit, 0)
	for _, r := range resolutions {
		kp, err := resolver.KeypairFor(r.ResolverPubkey)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		c, err := BuildResolutionCommit(r, kp)
		if err != nil {
			t.Fatalf("build resolution: %v", err)
		}
		resCommits = append(resCommits, *c)
	}
	if len(resCommits) != 1 {
		t.Fatalf("resolutions: got %d, want 1", len(resCommits))
	}
}
