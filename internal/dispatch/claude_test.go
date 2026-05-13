package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubClaudeServer returns an httptest server that imitates Anthropic's
// /v1/messages endpoint enough to drive ClaudeProvider end-to-end.
func stubClaudeServer(t *testing.T, replyText string, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if got := r.Header.Get("x-api-key"); got == "" {
			t.Errorf("missing x-api-key header")
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Errorf("missing anthropic-version header")
		}
		// Drain body so the client doesn't see an EOF mid-stream.
		body, _ := io.ReadAll(r.Body)
		var parsed claudeRequest
		_ = json.Unmarshal(body, &parsed)
		if parsed.Model == "" {
			t.Errorf("empty model in request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if statusCode/100 != 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "error",
				"error": map[string]string{
					"type":    "invalid_request_error",
					"message": replyText,
				},
			})
			return
		}
		resp := claudeResponse{
			ID:   "msg_test",
			Type: "message",
			Role: "assistant",
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "text", Text: replyText},
			},
			StopReason: "end_turn",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestClaudeProviderHappyPath(t *testing.T) {
	reply := `VERDICT: BREAKS

### Finding 1 — edge_case
- Category: edge_case
- Severity: critical
- Scenario: ...
`
	server := stubClaudeServer(t, reply, http.StatusOK)
	defer server.Close()

	provider := &ClaudeProvider{
		APIKey:           "test-key",
		BaseURL:          server.URL,
		HTTPClient:       server.Client(),
		AnthropicVersion: "2023-06-01",
	}
	member := PanelMember{Label: "claude-spec", Provider: "claude", Model: "claude-opus-4-7", Temperature: 0, Focus: "spec"}
	r, err := provider.Attack(context.Background(), member, "system prompt", "user prompt")
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != VerdictBreaks {
		t.Errorf("verdict = %q", r.Verdict)
	}
	if !strings.Contains(r.RawText, "edge_case") {
		t.Errorf("raw text = %q", r.RawText)
	}
	if len(r.Findings) == 0 {
		t.Fatalf("expected findings parsed")
	}
	if r.Findings[0].Severity != "critical" {
		t.Errorf("finding severity = %q", r.Findings[0].Severity)
	}
}

func TestClaudeProviderAPIError(t *testing.T) {
	server := stubClaudeServer(t, "bad model", http.StatusBadRequest)
	defer server.Close()

	provider := &ClaudeProvider{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}
	_, err := provider.Attack(context.Background(), PanelMember{Provider: "claude", Model: "bad"}, "", "")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "bad model") {
		t.Errorf("error doesn't surface API message: %v", err)
	}
}

func TestClaudeProviderRequiresAPIKey(t *testing.T) {
	p := &ClaudeProvider{} // no API key
	_, err := p.Attack(context.Background(), PanelMember{Provider: "claude", Model: "claude-opus-4-7"}, "", "")
	if err == nil {
		t.Fatal("expected error when APIKey unset")
	}
}

func TestClaudeProviderUsesMemberTemperatureAndMaxTokens(t *testing.T) {
	var seenTemp float64
	var seenMax int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed claudeRequest
		_ = json.Unmarshal(body, &parsed)
		seenTemp = parsed.Temperature
		seenMax = parsed.MaxTokens
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(claudeResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{{Type: "text", Text: "VERDICT: SURVIVES"}},
		})
	}))
	defer server.Close()
	p := &ClaudeProvider{APIKey: "k", BaseURL: server.URL, HTTPClient: server.Client()}
	_, err := p.Attack(context.Background(), PanelMember{Provider: "claude", Model: "m", Temperature: 0.7, MaxTokens: 2048}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if seenTemp != 0.7 {
		t.Errorf("temperature wire value = %v", seenTemp)
	}
	if seenMax != 2048 {
		t.Errorf("max_tokens wire value = %v", seenMax)
	}
}
