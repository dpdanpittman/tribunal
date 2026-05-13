package agent

import (
	"bytes"
	"errors"
	"testing"
)

func TestKeypairFromSeedDeterministic(t *testing.T) {
	seed := bytes.Repeat([]byte{0x42}, 32)
	k1, err := NewKeypairFromSeed(seed)
	if err != nil {
		t.Fatalf("seed1: %v", err)
	}
	k2, err := NewKeypairFromSeed(seed)
	if err != nil {
		t.Fatalf("seed2: %v", err)
	}
	if !bytes.Equal(k1.Public, k2.Public) {
		t.Fatal("same seed produced different pubkeys")
	}
	if !bytes.Equal(k1.Private, k2.Private) {
		t.Fatal("same seed produced different privkeys")
	}
}

func TestSignVerifyRoundtrip(t *testing.T) {
	k, err := NewKeypairFromSeed(bytes.Repeat([]byte{0x01}, 32))
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("a fingerprint of a finding")
	sig := k.Sign(msg)
	if err := Verify(k.PublicKeyString(), sig, msg); err != nil {
		t.Fatalf("verify on identical msg: %v", err)
	}
	if err := Verify(k.PublicKeyString(), sig, []byte("different msg")); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature on tampered msg, got %v", err)
	}
}

func TestPubkeyRoundtrip(t *testing.T) {
	k, err := NewKeypairFromSeed(bytes.Repeat([]byte{0x09}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s := k.PublicKeyString()
	parsed, err := ParsePubkey(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed, k.Public) {
		t.Fatal("parsed pubkey differs from original")
	}
}

func TestParsePubkeyRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"deadbeef",
		"ed25519:not-hex",
		"ed25519:abcd", // valid hex but wrong length
	}
	for _, c := range cases {
		if _, err := ParsePubkey(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}
