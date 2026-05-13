package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// PubkeyPrefix is prepended to all serialized public keys so the algorithm
// is unambiguous at a glance.
const PubkeyPrefix = "ed25519:"

// Keypair holds a Tribunal agent's ed25519 keypair plus its public-key string
// form. Use NewKeypair to generate from system entropy or NewKeypairFromSeed
// for deterministic tests.
type Keypair struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// NewKeypair generates a new keypair from crypto/rand.
func NewKeypair() (*Keypair, error) {
	return NewKeypairFromReader(rand.Reader)
}

// NewKeypairFromReader generates a keypair using the provided entropy source.
// Useful in tests for deterministic generation.
func NewKeypairFromReader(r io.Reader) (*Keypair, error) {
	pub, priv, err := ed25519.GenerateKey(r)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return &Keypair{Public: pub, Private: priv}, nil
}

// NewKeypairFromSeed derives a keypair from a 32-byte seed. The same seed
// always produces the same keypair (RFC 8032). Used in tests.
func NewKeypairFromSeed(seed []byte) (*Keypair, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return &Keypair{Public: pub, Private: priv}, nil
}

// Sign produces an ed25519 signature over msg using the keypair's private
// key. Returns hex-encoded bytes.
func (k *Keypair) Sign(msg []byte) string {
	sig := ed25519.Sign(k.Private, msg)
	return hex.EncodeToString(sig)
}

// PublicKeyString returns the public key in the canonical "ed25519:<hex>"
// form used everywhere in Tribunal artifacts and ledger entries.
func (k *Keypair) PublicKeyString() string {
	return FormatPubkey(k.Public)
}

// FormatPubkey returns the canonical "ed25519:<hex>" form of a raw ed25519
// public key.
func FormatPubkey(pub ed25519.PublicKey) string {
	return PubkeyPrefix + hex.EncodeToString(pub)
}

// ParsePubkey accepts the canonical "ed25519:<hex>" form and returns the
// raw 32-byte public key. Errors on malformed input.
func ParsePubkey(s string) (ed25519.PublicKey, error) {
	rest, ok := strings.CutPrefix(s, PubkeyPrefix)
	if !ok {
		return nil, fmt.Errorf("pubkey must start with %q", PubkeyPrefix)
	}
	raw, err := hex.DecodeString(rest)
	if err != nil {
		return nil, fmt.Errorf("pubkey hex: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("pubkey must decode to %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// Verify checks an ed25519 signature (hex-encoded) over msg with the given
// public-key string. Returns nil on success, ErrBadSignature on mismatch.
func Verify(pubkeyString, sigHex string, msg []byte) error {
	pub, err := ParsePubkey(pubkeyString)
	if err != nil {
		return err
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("signature hex: %w", err)
	}
	if !ed25519.Verify(pub, msg, sig) {
		return ErrBadSignature
	}
	return nil
}

// ErrBadSignature is returned when an ed25519 signature verification fails.
var ErrBadSignature = errors.New("tribunal: signature verification failed")
