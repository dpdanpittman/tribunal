package chain

import (
	"fmt"

	"github.com/dpdanpittman/tribunal/internal/agent"
)

// RegistryResolver adapts internal/agent.Registry to the KeyResolver
// interface expected by Sync. It caches the pubkey → label map on first
// use so subsequent lookups are O(1).
type RegistryResolver struct {
	reg   *agent.Registry
	index map[string]string // canonical pubkey -> label
}

// NewRegistryResolver returns a resolver backed by the given Registry.
func NewRegistryResolver(reg *agent.Registry) *RegistryResolver {
	return &RegistryResolver{reg: reg}
}

// KeypairFor satisfies KeyResolver: returns the private+public keypair
// whose recorded pubkey matches the canonical "ed25519:<hex>" string.
func (r *RegistryResolver) KeypairFor(pubkey string) (*agent.Keypair, error) {
	if r.index == nil {
		if err := r.rebuild(); err != nil {
			return nil, err
		}
	}
	label, ok := r.index[pubkey]
	if !ok {
		// One refresh in case the agent was just added.
		if err := r.rebuild(); err != nil {
			return nil, err
		}
		label, ok = r.index[pubkey]
		if !ok {
			return nil, fmt.Errorf("no registered agent for pubkey %s", pubkey)
		}
	}
	return r.reg.LoadKeypair(label)
}

func (r *RegistryResolver) rebuild() error {
	agents, err := r.reg.List()
	if err != nil {
		return err
	}
	idx := make(map[string]string, len(agents))
	for _, a := range agents {
		idx[a.Pubkey] = a.Label
	}
	r.index = idx
	return nil
}
