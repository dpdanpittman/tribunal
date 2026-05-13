package dispatch

import (
	"regexp"
	"strings"
)

// ParseReport extracts the verdict and a best-effort structured list of
// findings from a model's raw response text. The parser is intentionally
// lenient — model output formatting varies — but it does require:
//
//  1. A first non-empty line beginning with "VERDICT:" followed by one of
//     BREAKS / SURVIVES / INDETERMINATE.
//  2. Findings (optional) as `### Finding N — <category>` blocks containing
//     `Category:`, `Severity:`, optional `Scenario:` and `Defense:` lines.
//
// If no VERDICT line is present, the parser returns Verdict =
// INDETERMINATE and embeds the raw text for human review.
func ParseReport(raw string) (verdict, reason string, findings []ParsedFinding) {
	lines := strings.Split(raw, "\n")
	verdict = VerdictIndeterminate
	reason = "parser: no VERDICT line found"

	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if v, ok := extractVerdict(t); ok {
			verdict = v
			reason = ""
			break
		}
		// First non-blank line wasn't a VERDICT — keep scanning a few more.
	}

	findings = parseFindings(raw)
	return verdict, reason, findings
}

var verdictRE = regexp.MustCompile(`(?i)^VERDICT:\s*(BREAKS|SURVIVES|INDETERMINATE)\b`)

func extractVerdict(line string) (string, bool) {
	m := verdictRE.FindStringSubmatch(line)
	if len(m) == 2 {
		return strings.ToUpper(m[1]), true
	}
	return "", false
}

// findingHeaderRE matches headings like `### Finding 1 — under_specification`.
var findingHeaderRE = regexp.MustCompile(`(?im)^#{2,4}\s*Finding\s+\d+\s*[—:-]?\s*([a-z_][a-z0-9_]*)?\s*$`)

// fieldRE matches `Category: ...`, `Severity: ...`, `Scenario: ...`,
// `Suggested defense: ...`.
var fieldRE = regexp.MustCompile(`(?im)^\s*-?\s*(category|severity|scenario|suggested\s+defense|defense)\s*:\s*(.+)$`)

func parseFindings(raw string) []ParsedFinding {
	var out []ParsedFinding
	// Split on finding headers; everything between headers is one finding.
	idxs := findingHeaderRE.FindAllStringIndex(raw, -1)
	if len(idxs) == 0 {
		return out
	}
	// Append a sentinel so we can compute spans uniformly.
	idxs = append(idxs, []int{len(raw), len(raw)})

	for i := 0; i < len(idxs)-1; i++ {
		start := idxs[i][0]
		end := idxs[i+1][0]
		block := raw[start:end]
		out = append(out, parseOneFinding(block))
	}
	return out
}

func parseOneFinding(block string) ParsedFinding {
	var f ParsedFinding
	// Header line may include the category.
	if m := findingHeaderRE.FindStringSubmatch(block); len(m) == 2 && m[1] != "" {
		f.Category = strings.ToLower(m[1])
	}
	// Field-style lines override / fill.
	matches := fieldRE.FindAllStringSubmatch(block, -1)
	for _, m := range matches {
		key := strings.ToLower(strings.TrimSpace(m[1]))
		val := strings.TrimSpace(m[2])
		switch key {
		case "category":
			f.Category = strings.ToLower(val)
		case "severity":
			f.Severity = strings.ToLower(val)
		case "scenario":
			f.Scenario = val
		case "suggested defense", "defense":
			f.Defense = val
		}
	}
	return f
}
