package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func tempRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	return NewRegistry(dir)
}

func TestRegistryAddAndGet(t *testing.T) {
	r := tempRegistry(t)
	a, err := r.Add("claude-adversary", "claude-opus-4-7", RoleAdversary)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if a.Label != "claude-adversary" || a.Role != RoleAdversary {
		t.Fatalf("wrong agent: %+v", a)
	}
	if a.Pubkey == "" {
		t.Fatal("expected non-empty pubkey")
	}

	got, err := r.Get("claude-adversary")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Pubkey != a.Pubkey {
		t.Fatal("get returned different pubkey")
	}
}

func TestRegistryAddDuplicateLabelRejected(t *testing.T) {
	r := tempRegistry(t)
	if _, err := r.Add("dup", "m", RoleAdversary); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add("dup", "m", RoleAdversary); err == nil {
		t.Fatal("expected duplicate label to fail")
	}
}

func TestRegistryRejectInvalidLabel(t *testing.T) {
	r := tempRegistry(t)
	bad := []string{"", "has/slash", "has.dot", "has\\backslash"}
	for _, b := range bad {
		if _, err := r.Add(b, "m", RoleAdversary); err == nil {
			t.Errorf("expected error for label %q", b)
		}
	}
}

func TestRegistryLoadKeypairRoundtrip(t *testing.T) {
	r := tempRegistry(t)
	a, err := r.Add("alice", "model-x", RoleReviewerArch)
	if err != nil {
		t.Fatal(err)
	}
	kp, err := r.LoadKeypair("alice")
	if err != nil {
		t.Fatal(err)
	}
	if kp.PublicKeyString() != a.Pubkey {
		t.Fatal("loaded keypair pubkey mismatch")
	}
	msg := []byte("hello")
	sig := kp.Sign(msg)
	if err := Verify(kp.PublicKeyString(), sig, msg); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryListSortsByLabel(t *testing.T) {
	r := tempRegistry(t)
	for _, label := range []string{"charlie", "alpha", "bravo"} {
		if _, err := r.Add(label, "m", RoleAdversary); err != nil {
			t.Fatal(err)
		}
	}
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d agents, want 3", len(list))
	}
	if list[0].Label != "alpha" || list[1].Label != "bravo" || list[2].Label != "charlie" {
		t.Fatalf("unsorted: %v %v %v", list[0].Label, list[1].Label, list[2].Label)
	}
}

func TestRegistryRotate(t *testing.T) {
	r := tempRegistry(t)
	if _, err := r.Add("v1", "model-old", RoleAdversary); err != nil {
		t.Fatal(err)
	}
	newAgent, err := r.Rotate("v1", "v2")
	if err != nil {
		t.Fatal(err)
	}
	if newAgent.Label != "v2" {
		t.Fatalf("new agent label = %q", newAgent.Label)
	}

	old, err := r.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if old.RetiredAt.IsZero() {
		t.Fatal("old agent should have RetiredAt set")
	}
	if old.SupersededBy != "v2" {
		t.Fatalf("SupersededBy = %q, want v2", old.SupersededBy)
	}
}

func TestRegistryPrivateKeyMode(t *testing.T) {
	r := tempRegistry(t)
	if _, err := r.Add("perm-check", "m", RoleAdversary); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(r.AgentsDir(), "perm-check.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %v, want 0600", info.Mode().Perm())
	}
}
