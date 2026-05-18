package chain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// TestLooksLikeTestChain_TokenAware verifies the v0.3.4 token-aware
// implementation correctly classifies hostile / borderline chain ids.
// v0.3.3 used `strings.Contains(id, "test")` which false-positived on
// names like `xion-mainnet-test-fork` (P-v033-audit F-SEC-303).
//
// v0.3.5 adds the F-OPUS-004 cases — non-ASCII chain ids must be refused
// outright so Unicode confusables can't classify a mainnet-looking id
// as test.
func TestLooksLikeTestChain_TokenAware(t *testing.T) {
	tests := []struct {
		chainID string
		want    bool
		reason  string
	}{
		{"xion-testnet-2", true, "standard testnet"},
		{"xion-devnet-1", true, "standard devnet"},
		{"xion-mainnet-1", false, "standard mainnet"},
		{"xion-mainnet-test-fork", false, "mainnet token wins over substring 'test'"},
		{"xion-test-mainnet-fork", false, "mainnet token still wins regardless of position"},
		{"xion-prod-1", false, "prod marker"},
		{"local-devnet", true, "local + devnet both signal test"},
		{"my-attestation-chain", false, "substring 'test' inside 'attestation' does not match"},
		{"some-untested-network", false, "substring 'test' inside 'untested' does not match"},
		{"production", false, "production marker"},
		{"", false, "empty"},
		// F-OPUS-004 — Unicode confusables must NOT classify as test.
		{"MAİNNET-test-fork", false, "Turkish dotted I (U+0130) bypass attempt"},
		{"xion-tëst-1", false, "non-ASCII anywhere in id rejects"},
		{"xion­testnet-1", false, "soft hyphen U+00AD bypass attempt"},
		{"chain\tid", false, "control character (tab) rejected"},
		{"chain\x7Fid", false, "DEL (U+007F) is non-printable"},
	}
	for _, tt := range tests {
		t.Run(tt.chainID, func(t *testing.T) {
			got := LooksLikeTestChain(tt.chainID)
			if got != tt.want {
				t.Fatalf("LooksLikeTestChain(%q) = %v, want %v (%s)", tt.chainID, got, tt.want, tt.reason)
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

// TestVerifyOnChainCommit_MatchAndMismatch pins the F-OPUS-001 defense:
// a hostile LCD that reports a finding as committed under a *different*
// claim_hash / agent_pubkey / severity / stake must be caught before the
// caller silently drops the finding from sync. A matching on-chain state
// is the only case that returns nil.
func TestVerifyOnChainCommit_MatchAndMismatch(t *testing.T) {
	adv := kpFromSeed(t, 0xA1)
	f := &ledger.Finding{
		FindingID:   "F-1",
		PlanID:      "P-A",
		AgentPubkey: adv.PublicKeyString(),
		Severity:    ledger.SeverityCritical,
		ClaimHash:   "h1",
		Stake:       8,
	}
	wirePub, err := PubkeyToWire(adv.PublicKeyString())
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	matching := &FindingState{
		PlanID:      "P-A",
		FindingID:   "F-1",
		AgentPubkey: wirePub,
		Severity:    "critical",
		ClaimHash:   "h1",
		Stake:       "8",
	}
	if err := verifyOnChainCommit(f, matching); err != nil {
		t.Fatalf("matching state should pass: %v", err)
	}

	mismatches := []struct {
		name  string
		patch func(*FindingState)
	}{
		{"claim_hash", func(s *FindingState) { s.ClaimHash = "different" }},
		{"agent_pubkey", func(s *FindingState) { s.AgentPubkey = "AAAAAAAA" }},
		{"severity", func(s *FindingState) { s.Severity = "warning" }},
		{"stake", func(s *FindingState) { s.Stake = "16" }},
	}
	for _, tt := range mismatches {
		t.Run(tt.name, func(t *testing.T) {
			cp := *matching
			tt.patch(&cp)
			if err := verifyOnChainCommit(f, &cp); err == nil {
				t.Fatalf("%s mismatch should error", tt.name)
			}
		})
	}
}

// TestVerifyOnChainResolution_MatchAndMismatch mirrors the commit-side test
// for resolution verification (F-OPUS-001 / F-OPUS-006).
func TestVerifyOnChainResolution_MatchAndMismatch(t *testing.T) {
	pm := kpFromSeed(t, 0xB2)
	r := &ledger.Resolution{
		FindingID:      "F-1",
		PlanID:         "P-A",
		ResolverPubkey: pm.PublicKeyString(),
		Outcome:        ledger.OutcomeTruePositive,
		EvidenceHash:   "ev1",
	}
	wirePub, err := PubkeyToWire(pm.PublicKeyString())
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	matching := &ResolutionRecord{
		Outcome:        "true_positive",
		ResolverPubkey: wirePub,
		EvidenceHash:   "ev1",
	}
	if err := verifyOnChainResolution(r, matching); err != nil {
		t.Fatalf("matching state should pass: %v", err)
	}
	mismatches := []struct {
		name  string
		patch func(*ResolutionRecord)
	}{
		{"evidence_hash", func(s *ResolutionRecord) { s.EvidenceHash = "different" }},
		{"outcome", func(s *ResolutionRecord) { s.Outcome = "false_positive" }},
		{"resolver_pubkey", func(s *ResolutionRecord) { s.ResolverPubkey = "AAAAAAAA" }},
	}
	for _, tt := range mismatches {
		t.Run(tt.name, func(t *testing.T) {
			cp := *matching
			tt.patch(&cp)
			if err := verifyOnChainResolution(r, &cp); err == nil {
				t.Fatalf("%s mismatch should error", tt.name)
			}
		})
	}
}

// TestChunkFindingCommits pins the F-OPUS-003 chunking helper. The contract
// rejects batches >100 with BatchTooLarge; client-side chunking keeps every
// tx under that ceiling so >100-finding plans can settle.
func TestChunkFindingCommits(t *testing.T) {
	tests := []struct {
		size       int
		wantChunks int
		wantSizes  []int
	}{
		{0, 1, []int{0}},
		{1, 1, []int{1}},
		{100, 1, []int{100}},
		{101, 2, []int{100, 1}},
		{200, 2, []int{100, 100}},
		{250, 3, []int{100, 100, 50}},
	}
	for _, tt := range tests {
		commits := make([]FindingCommit, tt.size)
		for i := range commits {
			commits[i] = FindingCommit{FindingID: "F", PlanID: "P"}
		}
		chunks := chunkFindingCommits(commits)
		if len(chunks) != tt.wantChunks {
			t.Fatalf("size=%d chunks: got %d want %d", tt.size, len(chunks), tt.wantChunks)
		}
		for i, c := range chunks {
			if len(c) != tt.wantSizes[i] {
				t.Fatalf("size=%d chunk %d: got %d want %d", tt.size, i, len(c), tt.wantSizes[i])
			}
			if len(c) > maxBatchChunkSize {
				t.Fatalf("size=%d chunk %d exceeds max (%d > %d)", tt.size, i, len(c), maxBatchChunkSize)
			}
		}
	}
}

// TestChunkResolutionCommits mirrors the commit-side chunking test.
func TestChunkResolutionCommits(t *testing.T) {
	commits := make([]ResolutionCommit, 235)
	chunks := chunkResolutionCommits(commits)
	if len(chunks) != 3 {
		t.Fatalf("235 entries -> got %d chunks, want 3", len(chunks))
	}
	if len(chunks[0]) != 100 || len(chunks[1]) != 100 || len(chunks[2]) != 35 {
		t.Fatalf("chunk sizes drift: %d,%d,%d", len(chunks[0]), len(chunks[1]), len(chunks[2]))
	}
}

// TestSyncBudgetForPlans_Scales pins the F-OPUS-002 helper: the outer ctx
// budget must scale with the number of plans so plans N+1 aren't truncated
// by a fixed minute count.
func TestSyncBudgetForPlans_Scales(t *testing.T) {
	tests := []struct {
		n       int
		wantMin time.Duration
	}{
		{0, 5 * time.Minute},   // floor
		{1, 5 * time.Minute},   // floor
		{4, 5 * time.Minute},   // 4*90s*1.2 = 432s < 5m floor
		{10, 18 * time.Minute}, // 10*90s*1.2 = 1080s
		{20, 36 * time.Minute}, // 20*90s*1.2 = 2160s
	}
	for _, tt := range tests {
		got := SyncBudgetForPlans(tt.n)
		if got < tt.wantMin {
			t.Fatalf("SyncBudgetForPlans(%d) = %v, want at least %v", tt.n, got, tt.wantMin)
		}
	}
}
