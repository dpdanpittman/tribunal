package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ClaudeProvider talks to Anthropic's Messages API at /v1/messages.
// Implementations of Provider are safe for concurrent use; only HTTPClient
// and BaseURL should be mutated before first call.
type ClaudeProvider struct {
	// APIKey defaults to the ANTHROPIC_API_KEY environment variable.
	APIKey string
	// BaseURL defaults to https://api.anthropic.com. Override in tests via
	// httptest.NewServer().URL.
	BaseURL string
	// HTTPClient defaults to a 5-minute-timeout client. Override for tests
	// or to plug in a different transport.
	HTTPClient *http.Client
	// AnthropicVersion is sent in the `anthropic-version` header.
	AnthropicVersion string
}

// NewClaudeProvider returns a provider with sensible defaults reading from
// environment variables. Returns an error if no API key is available
// (neither argument nor environment).
func NewClaudeProvider() (*ClaudeProvider, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, errors.New("dispatch.claude: ANTHROPIC_API_KEY not set")
	}
	return &ClaudeProvider{
		APIKey:           key,
		BaseURL:          "https://api.anthropic.com",
		HTTPClient:       &http.Client{Timeout: 5 * time.Minute},
		AnthropicVersion: "2023-06-01",
	}, nil
}

// Name returns the registry key for this provider.
func (c *ClaudeProvider) Name() string { return "claude" }

// claudeRequest is the JSON body sent to /v1/messages.
type claudeRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature float64         `json:"temperature"`
	System      string          `json:"system,omitempty"`
	Messages    []claudeMessage `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// claudeResponse is the JSON body returned by /v1/messages on success.
type claudeResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// claudeError is the error envelope Anthropic returns on non-2xx.
type claudeError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Attack implements Provider for the Claude Messages API.
func (c *ClaudeProvider) Attack(ctx context.Context, member PanelMember, system, user string) (*Report, error) {
	if c.APIKey == "" {
		return nil, errors.New("dispatch.claude: APIKey unset")
	}
	maxTokens := member.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	body := claudeRequest{
		Model:       member.Model,
		MaxTokens:   maxTokens,
		Temperature: member.Temperature,
		System:      system,
		Messages: []claudeMessage{
			{Role: "user", Content: user},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("claude: marshal: %w", err)
	}
	url := strings.TrimRight(c.baseURL(), "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("claude: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", c.version())

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude: do request: %w", err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("claude: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var apiErr claudeError
		_ = json.Unmarshal(respBytes, &apiErr)
		msg := apiErr.Error.Message
		if msg == "" {
			msg = string(respBytes)
		}
		return nil, fmt.Errorf("claude: HTTP %d: %s", resp.StatusCode, msg)
	}
	var apiResp claudeResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("claude: decode response: %w (body=%s)", err, truncateString(string(respBytes), 500))
	}
	raw_text := concatTextBlocks(apiResp.Content)
	verdict, reason, findings := ParseReport(raw_text)
	return &Report{
		Member:   member,
		Verdict:  verdict,
		Reason:   reason,
		Findings: findings,
		RawText:  raw_text,
	}, nil
}

// GenerateOptions configures a raw Generate call.
type GenerateOptions struct {
	Model       string
	Temperature float64
	MaxTokens   int
	System      string
	User        string
}

// GenerateResult is what Generate returns: raw text plus best-effort
// token usage counts pulled from the Anthropic Usage envelope.
type GenerateResult struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

// Generate is a raw text-completion call to /v1/messages — same wire
// shape as Attack but without the adversary-report parsing layer. Used
// by callers that need raw model output (v0.4.2 convergence
// Implementer; future Plan/Implement stages).
func (c *ClaudeProvider) Generate(ctx context.Context, opts GenerateOptions) (*GenerateResult, error) {
	if c.APIKey == "" {
		return nil, errors.New("dispatch.claude: APIKey unset")
	}
	maxTokens := opts.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	body := claudeRequest{
		Model:       opts.Model,
		MaxTokens:   maxTokens,
		Temperature: opts.Temperature,
		System:      opts.System,
		Messages: []claudeMessage{
			{Role: "user", Content: opts.User},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("claude.generate: marshal: %w", err)
	}
	url := strings.TrimRight(c.baseURL(), "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("claude.generate: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", c.version())

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude.generate: do request: %w", err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("claude.generate: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var apiErr claudeError
		_ = json.Unmarshal(respBytes, &apiErr)
		msg := apiErr.Error.Message
		if msg == "" {
			msg = string(respBytes)
		}
		return nil, fmt.Errorf("claude.generate: HTTP %d: %s", resp.StatusCode, msg)
	}
	var apiResp claudeResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("claude.generate: decode: %w (body=%s)", err, truncateString(string(respBytes), 500))
	}
	return &GenerateResult{
		Text:         concatTextBlocks(apiResp.Content),
		InputTokens:  apiResp.Usage.InputTokens,
		OutputTokens: apiResp.Usage.OutputTokens,
	}, nil
}

func (c *ClaudeProvider) baseURL() string {
	if c.BaseURL == "" {
		return "https://api.anthropic.com"
	}
	return c.BaseURL
}

func (c *ClaudeProvider) version() string {
	if c.AnthropicVersion == "" {
		return "2023-06-01"
	}
	return c.AnthropicVersion
}

func (c *ClaudeProvider) client() *http.Client {
	if c.HTTPClient == nil {
		return &http.Client{Timeout: 5 * time.Minute}
	}
	return c.HTTPClient
}

func concatTextBlocks(blocks []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
