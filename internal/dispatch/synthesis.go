package dispatch

import (
	"sort"
	"strings"
)

// Synthesis is the aggregated read across all panel reports. It captures
// shared findings, unique findings, per-member verdicts, and category
// coverage so the orchestrator can advance, loop back, or escalate without
// re-reading every member report.
type Synthesis struct {
	Panel    []PanelMember       `json:"panel"`
	Reports  []*Report           `json:"reports"`
	Verdicts map[string]string   `json:"verdicts"` // member-label -> verdict
	Buckets  map[string]string   `json:"buckets"`  // member-label -> bucket
	Shared   []SharedFinding     `json:"shared_findings"`
	Unique   []UniqueFinding     `json:"unique_findings"`
	Coverage map[string][]string `json:"coverage"` // category -> [member-label]
	Overall  string              `json:"overall_verdict"`
}

// SharedFinding describes a category that ≥2 panel members surfaced.
type SharedFinding struct {
	Category string   `json:"category"`
	Severity string   `json:"severity"` // worst severity reported across members
	Members  []string `json:"members"`
}

// UniqueFinding describes a category that exactly one panel member
// surfaced. These are the most interesting class for adversarial review —
// blind-spot escapes.
type UniqueFinding struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	Member   string `json:"member"`
	Bucket   string `json:"bucket"`
}

// Synthesize merges N reports into a single Synthesis using the given
// bucket function for diversity attribution.
func Synthesize(reports []*Report, bucket BucketFn) *Synthesis {
	syn := &Synthesis{
		Reports:  reports,
		Verdicts: map[string]string{},
		Buckets:  map[string]string{},
		Coverage: map[string][]string{},
	}
	if bucket == nil {
		bucket = BucketByVendorFamily
	}

	// Walk each report; collect verdicts and findings by category.
	type catEntry struct {
		members  map[string]string // member-label -> severity
		buckets  map[string]string // member-label -> bucket
		severity string
	}
	cats := map[string]*catEntry{}

	for _, r := range reports {
		if r == nil {
			continue
		}
		label := labelOf(r.Member)
		syn.Panel = append(syn.Panel, r.Member)
		syn.Verdicts[label] = r.Verdict
		syn.Buckets[label] = bucket(r.Member)

		for _, f := range r.Findings {
			cat := strings.ToLower(f.Category)
			if cat == "" {
				cat = "uncategorized"
			}
			entry := cats[cat]
			if entry == nil {
				entry = &catEntry{
					members: map[string]string{},
					buckets: map[string]string{},
				}
				cats[cat] = entry
			}
			entry.members[label] = strings.ToLower(f.Severity)
			entry.buckets[label] = syn.Buckets[label]
			if severityRank(strings.ToLower(f.Severity)) > severityRank(entry.severity) {
				entry.severity = strings.ToLower(f.Severity)
			}
			syn.Coverage[cat] = appendOnce(syn.Coverage[cat], label)
		}
	}

	// Sort categories for stable output.
	keys := make([]string, 0, len(cats))
	for k := range cats {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, cat := range keys {
		entry := cats[cat]
		if len(entry.members) >= 2 {
			members := keysSorted(entry.members)
			syn.Shared = append(syn.Shared, SharedFinding{
				Category: cat,
				Severity: entry.severity,
				Members:  members,
			})
		} else if len(entry.members) == 1 {
			for member := range entry.members {
				syn.Unique = append(syn.Unique, UniqueFinding{
					Category: cat,
					Severity: entry.severity,
					Member:   member,
					Bucket:   entry.buckets[member],
				})
			}
		}
	}
	// Stable orderings for Members within shared findings.
	for i := range syn.Shared {
		sort.Strings(syn.Shared[i].Members)
	}
	sort.SliceStable(syn.Unique, func(i, j int) bool {
		if syn.Unique[i].Category != syn.Unique[j].Category {
			return syn.Unique[i].Category < syn.Unique[j].Category
		}
		return syn.Unique[i].Member < syn.Unique[j].Member
	})

	syn.Overall = overallVerdict(reports)
	return syn
}

// overallVerdict computes the panel-level verdict:
//   - BREAKS if any member returned BREAKS with at least one critical finding.
//   - INDETERMINATE if any member errored, returned INDETERMINATE, or had no findings recorded for a non-survives verdict.
//   - SURVIVES only when every member returned SURVIVES (or BREAKS with only suggestion-severity findings, which we treat as survives-with-residual).
//
// The conservative rule: any BREAKS-critical anywhere → BREAKS overall.
func overallVerdict(reports []*Report) string {
	allSurvived := true
	anyIndeterminate := false
	for _, r := range reports {
		if r == nil {
			continue
		}
		v := strings.ToUpper(strings.TrimSpace(r.Verdict))
		switch v {
		case VerdictBreaks:
			if anyCritical(r.Findings) {
				return VerdictBreaks
			}
			// BREAKS with only non-critical findings: degrade to survives.
			// But flag that the panel had a non-uniform result.
			allSurvived = false
		case VerdictSurvives:
			// nothing.
		case VerdictIndeterminate, "":
			anyIndeterminate = true
			allSurvived = false
		}
	}
	if anyIndeterminate {
		return VerdictIndeterminate
	}
	if allSurvived {
		return VerdictSurvives
	}
	return VerdictSurvives
}

func anyCritical(fs []ParsedFinding) bool {
	for _, f := range fs {
		if strings.EqualFold(f.Severity, "critical") {
			return true
		}
	}
	return false
}

// severityRank maps severity strings to a comparable integer; higher =
// more severe.
func severityRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "serious", "warning":
		return 2
	case "cosmetic", "suggestion":
		return 1
	}
	return 0
}

func labelOf(m PanelMember) string {
	if m.Label != "" {
		return m.Label
	}
	return m.Provider + ":" + m.Model
}

func appendOnce(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func keysSorted(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
