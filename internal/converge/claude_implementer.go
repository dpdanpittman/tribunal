package converge

import (
	"context"
	"fmt"
	"strings"

	"github.com/dpdanpittman/tribunal/internal/dispatch"
)

// ClaudeImplementer is the production Implementer backed by Anthropic's
// Messages API via dispatch.ClaudeProvider.Generate. The prompt asks for
// a strict unified-diff response so the controller can route it through
// `git apply` without further parsing.
type ClaudeImplementer struct {
	Provider    *dispatch.ClaudeProvider
	Model       string
	Temperature float64
	MaxTokens   int
	LabelStr    string
}

func (c *ClaudeImplementer) Label() string {
	if c.LabelStr != "" {
		return c.LabelStr
	}
	if c.Model != "" {
		return "implementer-" + c.Model
	}
	return "implementer-claude"
}

func (c *ClaudeImplementer) Patch(ctx context.Context, in PatchInput) (*PatchOutput, error) {
	if c.Provider == nil {
		return nil, fmt.Errorf("ClaudeImplementer: Provider unset")
	}
	system := implementerSystemPrompt
	user := buildImplementerUserPrompt(in)
	res, err := c.Provider.Generate(ctx, dispatch.GenerateOptions{
		Model:       c.Model,
		Temperature: c.Temperature,
		MaxTokens:   c.MaxTokens,
		System:      system,
		User:        user,
	})
	if err != nil {
		return nil, err
	}
	patch, reasoning, refused := parseImplementerResponse(res.Text)
	return &PatchOutput{
		Patch:     patch,
		Reasoning: reasoning,
		Refused:   refused,
		TokenCost: res.InputTokens + res.OutputTokens,
	}, nil
}

// implementerSystemPrompt is the canonical Tribunal implementer prompt.
// Asks for a deterministic two-block response: REASONING block first,
// then a single PATCH block containing a valid unified diff (or REFUSE).
const implementerSystemPrompt = `You are the Tribunal implementer. The convergence loop just completed a review round that surfaced one or more Critical/Warning findings. Your job: author a patch that resolves them.

Hard constraints:

1. Output a single PATCH block, fenced as ` + "```" + `diff … ` + "```" + `. The patch must be a valid unified diff (git apply format) anchored to the current working tree state. Include the standard ` + "`" + `diff --git a/... b/...` + "`" + ` and ` + "`" + `--- a/...` + "`" + ` / ` + "`" + `+++ b/...` + "`" + ` headers per file.
2. Address ONLY the findings supplied. Do not refactor unrelated code, rename unrelated identifiers, or add unrelated tests.
3. If a finding's fix requires architectural changes you can't make safely in one patch (renaming a public API, changing a contract surface), explain why in the REASONING block and emit ` + "```diff\n# REFUSE\n```" + ` as the PATCH block.
4. Preserve existing test files; add new tests next to the code you change when the patched behavior is observable.
5. Do not invent file paths or symbol names. If the finding references a path that doesn't appear in the supplied diff, ask the operator (via REASONING) before guessing.

Output format (mandatory):

` + "```" + `
REASONING:
<2-6 paragraphs explaining the fix choices, what each hunk does, and why.>

PATCH:
<single fenced diff block>
` + "```" + `

The convergence controller may choose to apply your patch directly via ` + "`git apply`" + ` — patches that fail ` + "`git apply --check`" + ` waste a round and drop your implementer reputation in this convergence cycle. Anchor every hunk against text that actually appears in the working tree.`

// buildImplementerUserPrompt assembles the per-round user message.
// Includes intent, original diff under review, the round's findings,
// and (when available) the per-finding markdown bodies the adversary
// stage wrote.
func buildImplementerUserPrompt(in PatchInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan ID: %s\nRound:   %d\nProject: %s\n\n", in.PlanID, in.Round, in.ProjectRoot)

	if strings.TrimSpace(in.Intent) != "" {
		fmt.Fprintln(&b, "## Intent")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, strings.TrimSpace(in.Intent))
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, "## Diff under review")
	fmt.Fprintln(&b)
	if strings.TrimSpace(in.Diff) == "" {
		fmt.Fprintln(&b, "(no diff captured — review the working tree as-is)")
	} else {
		fmt.Fprintln(&b, "```diff")
		fmt.Fprintln(&b, strings.TrimSpace(in.Diff))
		fmt.Fprintln(&b, "```")
	}
	fmt.Fprintln(&b)

	fmt.Fprintf(&b, "## Findings to address (%d)\n\n", len(in.Findings))
	for i, f := range in.Findings {
		fmt.Fprintf(&b, "### Finding %d — %s [%s]\n", i+1, f.Category, f.Severity)
		fmt.Fprintf(&b, "- Surfaced by: %s\n", f.Member)
		fmt.Fprintf(&b, "- Claim hash: %s\n", f.ClaimHash)
		if f.CarryForward {
			fmt.Fprintln(&b, "- Carry-forward: yes (prior round filed the same hash; current attempt failed)")
		}
		if f.Scenario != "" {
			fmt.Fprintf(&b, "\nScenario:\n%s\n", f.Scenario)
		}
		// Per-finding body, if the controller supplied it.
		if body := in.FindingBodies[f.ClaimHash]; body != "" {
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, "Full finding text:")
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, body)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, "Author a patch resolving the findings above. Follow the system-prompt output format exactly.")
	return b.String()
}

// parseImplementerResponse pulls a unified-diff block out of the model
// response. Returns (patch, reasoning, refused). A REFUSE block is
// recognized as `# REFUSE` (case-insensitive) inside the diff fence.
func parseImplementerResponse(text string) (patch, reasoning string, refused bool) {
	reasoning, rest := splitReasoning(text)
	// Find the first ```diff (or bare ```) fenced block in the rest.
	patch = extractFencedBlock(rest)
	if strings.Contains(strings.ToUpper(patch), "# REFUSE") {
		return "", strings.TrimSpace(reasoning), true
	}
	return strings.TrimSpace(patch), strings.TrimSpace(reasoning), false
}

// splitReasoning extracts the REASONING section (between "REASONING:"
// and "PATCH:" markers, or the start of a fenced diff). Returns
// (reasoning, remainderForPatchExtraction).
func splitReasoning(text string) (string, string) {
	lower := strings.ToLower(text)
	rIdx := strings.Index(lower, "reasoning:")
	pIdx := strings.Index(lower, "patch:")
	if rIdx < 0 || pIdx < 0 || pIdx < rIdx {
		return "", text
	}
	reason := strings.TrimSpace(text[rIdx+len("reasoning:") : pIdx])
	return reason, text[pIdx+len("patch:"):]
}

func extractFencedBlock(text string) string {
	// Prefer ```diff …``` when present; fall back to bare ```.
	if idx := strings.Index(text, "```diff"); idx >= 0 {
		rest := text[idx+len("```diff"):]
		end := strings.Index(rest, "```")
		if end < 0 {
			return strings.TrimSpace(rest)
		}
		return rest[:end]
	}
	if idx := strings.Index(text, "```"); idx >= 0 {
		rest := text[idx+3:]
		end := strings.Index(rest, "```")
		if end < 0 {
			return strings.TrimSpace(rest)
		}
		return rest[:end]
	}
	return ""
}
