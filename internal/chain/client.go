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
	if cfg.KeyringBackend == "test" && !LooksLikeTestChain(cfg.ChainID) {
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

// LooksLikeTestChain reports whether the chain id looks like a dev /
// test environment. Used to decide whether to emit the keyring warning
// here and whether `tribunal-seed --send` requires `--allow-prod`.
//
// v0.3.4 — token-aware instead of substring. v0.3.3's substring check
// false-positived on hostile/borderline chain ids like
// `xion-mainnet-test-fork` (P-v033-audit F-SEC-303). Behavior:
//   - Explicit `mainnet` / `main` / `prod` / `production` markers ALWAYS
//     win (return false), regardless of whether other markers also match.
//   - Otherwise, look for `devnet` / `testnet` / `test` / `dev` / `local`
//     as discrete dash-separated tokens, not substrings.
//
// v0.3.5 — printable-ASCII guard against Unicode confusables (F-OPUS-004,
// P-v035-followup). Chain ids containing any rune outside U+0020..=U+007E
// are refused outright (return false → "not a test chain, require
// --allow-prod"). Without this guard, `MAİNNET-test-fork` (Turkish dotted
// I, U+0130) lowercases to `mai̇nnet-test-fork` (U+0307 combining dot
// above), which fails the literal `mainnet` token check but still trips
// the `test` token, classifying a human-readable-mainnet id as test.
// Cosmos chain-ids are ASCII by convention; rejecting non-ASCII costs
// nothing for legitimate chains and closes the bypass for hostile ones.
//
// Consolidates the previously-duplicated copy from cmd/tribunal-seed.
func LooksLikeTestChain(chainID string) bool {
	for _, r := range chainID {
		if r < 0x20 || r > 0x7E {
			return false
		}
	}
	id := strings.ToLower(chainID)
	parts := strings.Split(id, "-")
	for _, p := range parts {
		switch p {
		case "mainnet", "main", "prod", "production":
			return false
		}
	}
	for _, p := range parts {
		switch p {
		case "devnet", "testnet", "test", "dev", "local":
			return true
		}
	}
	return false
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

// fetchTxAttemptTimeout caps each individual REST poll. Picked short so
// a slow / hostile LCD can't starve sync's overall ctx budget — sync's
// outer ctx is typically minutes, but a single poll shouldn't be allowed
// to consume more than a couple seconds of it.
const fetchTxAttemptTimeout = 3 * time.Second

// preflightAttemptTimeout caps a single pre-flight chain query in sync.
// Same reasoning as fetchTxAttemptTimeout — bounded LCD-DoS surface.
const preflightAttemptTimeout = 3 * time.Second

// waitProgressInterval is the threshold at which WaitForTx and sync's
// pre-flight start emitting stderr progress notes so operators see the
// loop is alive during multi-second waits.
const waitProgressInterval = 5 * time.Second

// Execute submits the given ExecuteMsg to the contract via `xiond tx wasm
// execute` and waits for the tx to be included in a block. Returns the
// broadcast result with on-chain status (code, raw_log) filled in.
//
// xiond's `broadcast-mode sync` only confirms mempool acceptance — back-to-back
// Execute calls without a wait hit `account sequence mismatch` because the
// caller's cached sequence is stale until the prior tx lands. Polling the
// REST tx endpoint here makes sequential Executes safe.
//
// The caller-supplied ctx bounds the entire round-trip including the wait.
//
// IMPORTANT: when an error is returned, the *BroadcastResult may still be
// non-nil with a valid TxHash. The tx may have broadcast successfully but
// failed during the wait stage (transient or terminal). Callers must check
// res != nil and the txhash even on error so they can resume polling or
// surface the on-chain status to the operator.
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
		return &res, fmt.Errorf("tx broadcast failed (code=%d): %s", res.Code, res.RawLog)
	}
	if res.TxHash == "" {
		return &res, fmt.Errorf("xiond returned empty txhash; raw output=%q", string(out))
	}
	if err := c.WaitForTx(ctx, res.TxHash); err != nil {
		return &res, fmt.Errorf("wait for inclusion (txhash=%s): %w", res.TxHash, err)
	}
	return &res, nil
}

// WaitForTx polls the REST tx endpoint until the given hash is found,
// the tx fails on-chain, or ctx is cancelled. Each individual REST poll
// is bounded by fetchTxAttemptTimeout so a hostile / slow LCD can't
// starve the caller's overall ctx budget. Transient errors (network blip,
// connection refused, 5xx, parse failure on a partial body) are absorbed
// and the loop continues — the whole point of the wait is to be resilient
// to broadcast→inclusion gaps. Terminal errors (4xx other than 404,
// on-chain code != 0) propagate immediately. After waitProgressInterval
// without resolution, the loop emits a one-line stderr note so operators
// see the wait is alive.
func (c *Client) WaitForTx(ctx context.Context, txhash string) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	start := time.Now()
	lastProgress := start
	var transientStreak int

	for {
		ok, code, log, terminal, err := c.fetchTx(ctx, txhash)
		if err != nil {
			if terminal {
				return err
			}
			// Transient: keep polling. Count consecutive transients so an
			// LCD that's been broken for the full ctx surfaces something
			// useful in the timeout error.
			transientStreak++
		} else if ok {
			if code != 0 {
				return fmt.Errorf("tx %s failed on-chain (code=%d): %s", txhash, code, log)
			}
			return nil
		} else {
			transientStreak = 0
		}

		if since := time.Since(lastProgress); since >= waitProgressInterval {
			fmt.Fprintf(os.Stderr, "tribunal: still waiting on tx %s (elapsed=%s, transient_streak=%d)\n",
				txhash, time.Since(start).Round(time.Second), transientStreak)
			lastProgress = time.Now()
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("tx %s not included before context done (elapsed=%s, transient_streak=%d): %w",
				txhash, time.Since(start).Round(time.Second), transientStreak, ctx.Err())
		case <-ticker.C:
			// poll again
		}
	}
}

// fetchTx returns (found, code, raw_log, terminal, err). A 404 from the
// REST endpoint means "tx not yet indexed" and is reported as found=false
// with no error. Transient HTTP errors (5xx, connection refused, timeout,
// parse failure on a partial body) are returned with terminal=false so
// the caller absorbs them and keeps polling. 4xx other than 404 are
// returned with terminal=true.
func (c *Client) fetchTx(ctx context.Context, txhash string) (found bool, code int, rawLog string, terminal bool, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, fetchTxAttemptTimeout)
	defer cancel()

	u, urlErr := url.JoinPath(c.cfg.NodeREST, "cosmos", "tx", "v1beta1", "txs", txhash)
	if urlErr != nil {
		return false, 0, "", true, urlErr
	}
	req, reqErr := http.NewRequestWithContext(attemptCtx, http.MethodGet, u, nil)
	if reqErr != nil {
		return false, 0, "", true, reqErr
	}
	resp, doErr := c.http.Do(req)
	if doErr != nil {
		// Network-layer error (refused, reset, timeout). Treat as transient.
		return false, 0, "", false, fmt.Errorf("tx fetch http: %w", doErr)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		// Body read failure mid-response. Transient.
		return false, 0, "", false, readErr
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, 0, "", false, nil
	}
	if resp.StatusCode >= 500 {
		// LCD's own fault. Transient.
		return false, 0, "", false, fmt.Errorf("tx fetch http %d: %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode != http.StatusOK {
		// 4xx other than 404. Terminal.
		return false, 0, "", true, fmt.Errorf("tx fetch http %d: %s", resp.StatusCode, string(body))
	}
	var env struct {
		TxResponse struct {
			Code   int    `json:"code"`
			RawLog string `json:"raw_log"`
			Height string `json:"height"`
		} `json:"tx_response"`
	}
	if jsonErr := json.Unmarshal(body, &env); jsonErr != nil {
		// LCD returned 200 with a body we couldn't parse. Could be a
		// partial response mid-write or a non-LCD endpoint masquerading
		// as one. Treat as transient — a real LCD will produce parseable
		// JSON on retry.
		return false, 0, "", false, fmt.Errorf("parse tx response: %w (body=%q)", jsonErr, string(body))
	}
	// Some LCD implementations 200 with an empty payload while a tx is in
	// limbo between mempool and indexing. Treat height=="" as "not yet
	// indexed" so the caller keeps polling.
	if env.TxResponse.Height == "" {
		return false, 0, "", false, nil
	}
	return true, env.TxResponse.Code, env.TxResponse.RawLog, false, nil
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
