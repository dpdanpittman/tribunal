package chain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Client is the gateway to the on-chain reputation contract. It composes
// two transports:
//
//   - Transactions (state-changing): shelled out to `xiond tx wasm execute`.
//     xiond handles keyring, signing, gas simulation, and broadcast. This
//     keeps Tribunal off the cosmos-sdk Go dep graph and lets users
//     manage keys with the same tool their devops already uses.
//   - Queries (read-only): direct HTTP to the LCD REST endpoint. No
//     keyring, no signing. Faster, more portable.
//
// Construct with New(cfg). The zero value is not usable.
type Client struct {
	cfg  *Config
	http *http.Client
}

// New returns a Client ready to call the chain. Logs a one-line warning
// to stderr if the operator's `keyring_backend = test` is combined with
// a non-test-looking `chain_id` — `test` stores keys in plaintext and
// should never be used against a production chain.
func New(cfg *Config) *Client {
	if cfg.KeyringBackend == "test" && !looksLikeTestChain(cfg.ChainID) {
		fmt.Fprintf(os.Stderr,
			"tribunal: WARNING — keyring_backend=test against chain_id=%q. The test backend stores signing keys in plaintext; use keyring_backend=os for any non-dev environment.\n",
			cfg.ChainID)
	}
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// looksLikeTestChain reports whether the chain id looks like a dev /
// test environment. Used to decide whether to emit the keyring warning.
// Chain ids containing "devnet", "testnet", or "test" are considered
// non-production; anything else triggers the warning.
func looksLikeTestChain(chainID string) bool {
	id := strings.ToLower(chainID)
	return strings.Contains(id, "devnet") ||
		strings.Contains(id, "testnet") ||
		strings.Contains(id, "test") ||
		strings.Contains(id, "local")
}

// Config returns the active configuration.
func (c *Client) Config() *Config { return c.cfg }

// BroadcastResult is the structured xiond output after a tx broadcast.
// Only the fields Tribunal cares about are captured; xiond returns more.
type BroadcastResult struct {
	TxHash string `json:"txhash"`
	Code   int    `json:"code"`
	RawLog string `json:"raw_log"`
}

// Execute submits the given ExecuteMsg to the contract via `xiond tx wasm
// execute`. Blocks until the tx is included in a block (broadcast mode
// `sync` + a tx query). Returns the broadcast result.
//
// The caller-supplied ctx bounds the entire round-trip.
func (c *Client) Execute(ctx context.Context, msg *ExecuteMsg) (*BroadcastResult, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal execute: %w", err)
	}
	args := []string{
		"tx", "wasm", "execute", c.cfg.ContractAddress, string(payload),
		"--from", c.cfg.OperatorKeyName,
		"--chain-id", c.cfg.ChainID,
		"--node", c.cfg.NodeRPC,
		"--keyring-backend", c.cfg.KeyringBackend,
		"--gas-prices", c.cfg.GasPrices,
		"--gas-adjustment", c.cfg.GasAdjustment,
		"--gas", "auto",
		"--broadcast-mode", "sync",
		"--output", "json",
		"--yes",
	}
	out, err := c.runXiond(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("xiond execute: %w", err)
	}
	var res BroadcastResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("parse xiond output: %w (output=%q)", err, string(out))
	}
	if res.Code != 0 {
		return &res, fmt.Errorf("tx failed (code=%d): %s", res.Code, res.RawLog)
	}
	return &res, nil
}

// Query executes a smart query against the contract via the LCD REST
// endpoint. The result is the contract's response JSON; the caller
// unmarshals into a typed response struct.
func (c *Client) Query(ctx context.Context, msg *QueryMsg) ([]byte, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal query: %w", err)
	}
	// LCD path: /cosmwasm/wasm/v1/contract/{addr}/smart/{query_data_b64}
	encoded := base64.URLEncoding.EncodeToString(payload)
	u, err := url.JoinPath(
		c.cfg.NodeREST,
		"cosmwasm", "wasm", "v1", "contract", c.cfg.ContractAddress, "smart", encoded,
	)
	if err != nil {
		return nil, fmt.Errorf("build query url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query http: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query http %d: %s", resp.StatusCode, string(body))
	}
	// LCD wraps the contract response in { "data": ... }.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse query envelope: %w (body=%q)", err, string(body))
	}
	return envelope.Data, nil
}

// Status pings the Tendermint RPC and returns the latest block height.
// Used by `tribunal chain status` as a sanity check.
func (c *Client) Status(ctx context.Context) (int64, error) {
	u, err := url.JoinPath(c.cfg.NodeRPC, "status")
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("status http: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status http %d: %s", resp.StatusCode, string(body))
	}
	var env struct {
		Result struct {
			SyncInfo struct {
				LatestBlockHeight string `json:"latest_block_height"`
			} `json:"sync_info"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return 0, fmt.Errorf("parse status: %w (body=%q)", err, string(body))
	}
	var h int64
	if _, err := fmt.Sscanf(env.Result.SyncInfo.LatestBlockHeight, "%d", &h); err != nil {
		return 0, fmt.Errorf("parse height %q: %w", env.Result.SyncInfo.LatestBlockHeight, err)
	}
	return h, nil
}

// runXiond runs the configured xiond invocation with the given args and
// returns stdout. stderr is folded into the error on failure.
//
// XiondBinary may be a single executable name ("xiond") or a space-separated
// command-with-prefix ("docker exec devnet-xion-1 xiond"). The latter is
// useful when xiond runs inside a container on the same host as Tribunal.
func (c *Client) runXiond(ctx context.Context, args []string) ([]byte, error) {
	binSpec := c.cfg.XiondBinary
	if binSpec == "" {
		binSpec = "xiond"
	}
	parts := strings.Fields(binSpec)
	bin := parts[0]
	fullArgs := append(append([]string{}, parts[1:]...), args...)
	cmd := exec.CommandContext(ctx, bin, fullArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w (stderr=%s)",
			binSpec, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}
