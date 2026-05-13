// Package chain integrates Tribunal with the on-chain reputation contract
// deployed on Burnt XION. It builds CosmWasm ExecuteMsg and QueryMsg JSON,
// signs and submits transactions via the xiond CLI, and queries the LCD
// REST endpoint for reputation state. Local ledger entries are translated
// into contract messages by sync.go; failed real-time commits land in a
// retry queue (queue.go) so they get picked up on the next plan-close sync.
package chain

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the persistent chain configuration. Lives at ~/.tribunal/chain.yaml.
// All fields are required for any chain operation except OutcomeRewardMultiplier
// which mirrors the contract's instantiation parameter for client-side reward
// previews.
type Config struct {
	// ChainID is the Cosmos chain identifier, e.g. "xion-testnet-2".
	ChainID string `yaml:"chain_id"`
	// NodeRPC is the Tendermint RPC endpoint, e.g. "https://rpc.xion-testnet-2.burnt.com:443".
	NodeRPC string `yaml:"node_rpc"`
	// NodeREST is the LCD/REST endpoint, e.g. "https://api.xion-testnet-2.burnt.com".
	NodeREST string `yaml:"node_rest"`
	// ContractAddress is the bech32 address of the deployed tribunal-reputation
	// contract.
	ContractAddress string `yaml:"contract_address"`
	// OperatorKeyName is the local xiond keyring entry used to sign
	// transactions. Has to exist in the user's xiond keyring; this client
	// never touches the seed phrase directly.
	OperatorKeyName string `yaml:"operator_key_name"`
	// KeyringBackend is one of {os, file, test, kwallet, pass, memory}.
	// Defaults to "test" for testnet ease; production usage should set "os".
	KeyringBackend string `yaml:"keyring_backend"`
	// GasPrices is the fee unit, e.g. "0.025uxion".
	GasPrices string `yaml:"gas_prices"`
	// GasAdjustment is the safety multiplier on simulated gas.
	GasAdjustment string `yaml:"gas_adjustment"`
	// XiondBinary is the path or name of the xiond binary. Defaults to "xiond".
	XiondBinary string `yaml:"xiond_binary"`
	// OutcomeRewardMultiplier mirrors the contract instantiation parameter
	// so the client can preview rewards without an extra round-trip. Default 2.
	OutcomeRewardMultiplier uint64 `yaml:"outcome_reward_multiplier"`
}

// LoadConfig reads ~/.tribunal/chain.yaml (or path if non-empty) and
// validates required fields.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		path = filepath.Join(home, ".tribunal", "chain.yaml")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.XiondBinary == "" {
		c.XiondBinary = "xiond"
	}
	if c.KeyringBackend == "" {
		c.KeyringBackend = "test"
	}
	if c.GasAdjustment == "" {
		c.GasAdjustment = "1.4"
	}
	if c.OutcomeRewardMultiplier == 0 {
		c.OutcomeRewardMultiplier = 2
	}
}

func (c *Config) validate() error {
	if c.ChainID == "" {
		return fmt.Errorf("chain_id is required")
	}
	if c.NodeRPC == "" {
		return fmt.Errorf("node_rpc is required")
	}
	if c.NodeREST == "" {
		return fmt.Errorf("node_rest is required")
	}
	if c.ContractAddress == "" {
		return fmt.Errorf("contract_address is required")
	}
	if c.OperatorKeyName == "" {
		return fmt.Errorf("operator_key_name is required")
	}
	if c.GasPrices == "" {
		return fmt.Errorf("gas_prices is required")
	}
	return nil
}

// Save writes the config back to disk. Used by `tribunal chain init` after
// contract deploy.
func (c *Config) Save(path string) error {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		path = filepath.Join(home, ".tribunal", "chain.yaml")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
