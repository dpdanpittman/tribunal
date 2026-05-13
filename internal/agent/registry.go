package agent

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Agent is the metadata for a registered Tribunal agent. The private key
// material is *not* stored on this struct — it lives next to the metadata
// in <label>.key (0600). Use Registry.LoadKeypair to read the private key.
type Agent struct {
	Label     string    `json:"label"`
	ModelID   string    `json:"model_id"`
	Role      Role      `json:"role"`
	Pubkey    string    `json:"pubkey"`
	CreatedAt time.Time `json:"created_at"`
	// RetiredAt is non-zero once the agent has been rotated out.
	RetiredAt time.Time `json:"retired_at,omitempty"`
	// SupersededBy is the label that succeeded this agent on rotation.
	SupersededBy string `json:"superseded_by,omitempty"`
}

// Registry manages the on-disk store of agent keypairs and metadata at
// <root>/agents/. Typical root is ~/.tribunal.
type Registry struct {
	root string
}

// NewRegistry returns a Registry rooted at the given directory. The
// directory is created on Add if it doesn't exist.
func NewRegistry(root string) *Registry {
	return &Registry{root: root}
}

// AgentsDir is the on-disk subdirectory where agent files live.
func (r *Registry) AgentsDir() string {
	return filepath.Join(r.root, "agents")
}

// Add generates a fresh keypair for the given label/model/role and writes
// both the private key and metadata to disk with appropriate permissions.
// Errors on duplicate label.
func (r *Registry) Add(label, modelID string, role Role) (*Agent, error) {
	if label == "" {
		return nil, errors.New("label must not be empty")
	}
	if strings.ContainsAny(label, "/\\.") {
		return nil, fmt.Errorf("label %q must not contain '/', '\\', or '.'", label)
	}
	if _, err := r.Get(label); err == nil {
		return nil, fmt.Errorf("agent label %q already taken", label)
	}
	if err := os.MkdirAll(r.AgentsDir(), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir agents dir: %w", err)
	}
	kp, err := NewKeypair()
	if err != nil {
		return nil, err
	}
	agent := &Agent{
		Label:     label,
		ModelID:   modelID,
		Role:      role,
		Pubkey:    kp.PublicKeyString(),
		CreatedAt: time.Now().UTC(),
	}
	if err := r.write(agent, kp); err != nil {
		return nil, err
	}
	return agent, nil
}

func (r *Registry) write(agent *Agent, kp *Keypair) error {
	keyPath := filepath.Join(r.AgentsDir(), agent.Label+".key")
	pubPath := filepath.Join(r.AgentsDir(), agent.Label+".pub")

	keyBytes := []byte(hex.EncodeToString(kp.Private.Seed()) + "\n")
	if err := writeFileAtomic(keyPath, keyBytes, 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	metaBytes, err := json.MarshalIndent(agent, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if err := writeFileAtomic(pubPath, append(metaBytes, '\n'), 0o644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	return nil
}

// Get returns the metadata for the agent with the given label.
func (r *Registry) Get(label string) (*Agent, error) {
	pubPath := filepath.Join(r.AgentsDir(), label+".pub")
	data, err := os.ReadFile(pubPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("agent %q not found", label)
		}
		return nil, err
	}
	var a Agent
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("decode agent %q: %w", label, err)
	}
	return &a, nil
}

// LoadKeypair returns the full keypair for a label, including the private
// key read from disk. Use this only for signing.
func (r *Registry) LoadKeypair(label string) (*Keypair, error) {
	a, err := r.Get(label)
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(r.AgentsDir(), label+".key")
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	hexSeed := strings.TrimSpace(string(raw))
	seed, err := hex.DecodeString(hexSeed)
	if err != nil {
		return nil, fmt.Errorf("decode seed: %w", err)
	}
	kp, err := NewKeypairFromSeed(seed)
	if err != nil {
		return nil, err
	}
	if kp.PublicKeyString() != a.Pubkey {
		return nil, fmt.Errorf("agent %q: on-disk private key does not match recorded pubkey", label)
	}
	return kp, nil
}

// List returns all agents in label-sorted order.
func (r *Registry) List() ([]*Agent, error) {
	dir := r.AgentsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Agent
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		label := strings.TrimSuffix(e.Name(), ".pub")
		a, err := r.Get(label)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

// Rotate generates a new keypair under newLabel, marks oldLabel retired,
// records the supersession link, and returns the new agent. The old
// agent's metadata is preserved so signatures on past findings remain
// verifiable.
func (r *Registry) Rotate(oldLabel, newLabel string) (*Agent, error) {
	oldAgent, err := r.Get(oldLabel)
	if err != nil {
		return nil, err
	}
	if oldAgent.RetiredAt.Unix() != 0 && !oldAgent.RetiredAt.IsZero() {
		return nil, fmt.Errorf("agent %q is already retired", oldLabel)
	}
	if _, err := r.Get(newLabel); err == nil {
		return nil, fmt.Errorf("agent label %q already taken", newLabel)
	}
	newAgent, err := r.Add(newLabel, oldAgent.ModelID, oldAgent.Role)
	if err != nil {
		return nil, err
	}
	oldAgent.RetiredAt = time.Now().UTC()
	oldAgent.SupersededBy = newLabel
	pubPath := filepath.Join(r.AgentsDir(), oldLabel+".pub")
	metaBytes, err := json.MarshalIndent(oldAgent, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(pubPath, append(metaBytes, '\n'), 0o644); err != nil {
		return nil, fmt.Errorf("rewrite retired metadata: %w", err)
	}
	return newAgent, nil
}

// DefaultRoot returns the conventional Tribunal root directory: ~/.tribunal.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tribunal"), nil
}

// writeFileAtomic writes data to path via a temporary file + rename so a
// crash mid-write doesn't leave a partial file behind.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tribunal-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
