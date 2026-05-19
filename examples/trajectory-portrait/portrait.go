// Package portrait is a worked-example "system under test" for the
// Tribunal trajectory PBT scaffold (internal/trajectory, v0.5.2 M3).
//
// The domain is deliberately session-essence-flavoured: a Portrait
// is a list of sections, some marked load-bearing. Operations include
// adding sections, surgically editing them, and running a Synthesize
// pass that produces a merged portrait (the way session-essence's
// PreCompact hook does in real life).
//
// The temporal lens audits session-essence and identifies the
// trajectory property: "no Synthesize pass should ever drop a section
// marked load-bearing." That's a state-machine property — the
// invariant must hold after every operation, not just at the end of a
// single test case. ADR-0003 M3's claim is that such properties become
// executable via rapid's stateful PBT engine; the trajectory scaffold
// is the idiom for encoding them.
//
// Synthesize is intentionally written to come in two flavours:
//
//   - SynthesizeSafe: respects the load-bearing marker. The Property
//     defined against this passes — the test shows the invariant
//     genuinely holds across random trajectories.
//
//   - SynthesizeBuggy: drops sections with probability proportional to
//     a "compression aggressiveness" parameter, ignoring the marker.
//     The Property defined against this fails — and rapid will shrink
//     the failing trajectory to a minimal counterexample (typically:
//     one Add-load-bearing, one SynthesizeBuggy, invariant violation).
//
// The buggy-side test is marked t.Skip in CI so the suite stays green;
// operators can un-skip to watch rapid shrink the bug. See README.md.
package portrait

// Section is one named block of portrait content. LoadBearing marks
// the section as something the next session's behaviour depends on
// (the temporal lens's "operative" memory class from session-essence).
type Section struct {
	Name        string
	Content     string
	LoadBearing bool
}

// Portrait is the system under test. It's a thin model — just enough
// state for the trajectory property to be meaningful.
type Portrait struct {
	Sections []Section

	// CompressionAggressiveness controls SynthesizeBuggy's drop rate.
	// SynthesizeSafe ignores it. Modeling a real-world failure mode:
	// the buggy implementation got fast by being more aggressive about
	// pruning, but didn't honour the load-bearing marker.
	CompressionAggressiveness float64
}

// New returns an empty Portrait with default aggressiveness.
func New() *Portrait {
	return &Portrait{CompressionAggressiveness: 0.3}
}

// Add appends a section. Used as a trajectory operation.
func (p *Portrait) Add(s Section) {
	p.Sections = append(p.Sections, s)
}

// Edit updates the content of a named section. If the section doesn't
// exist, Edit is a no-op (modeling: the synthesizer asked to edit a
// section that didn't exist — should not affect load-bearing count).
func (p *Portrait) Edit(name, content string) {
	for i := range p.Sections {
		if p.Sections[i].Name == name {
			p.Sections[i].Content = content
			return
		}
	}
}

// LoadBearingCount returns the number of sections currently marked
// load-bearing. This is the invariant target.
func (p *Portrait) LoadBearingCount() int {
	n := 0
	for _, s := range p.Sections {
		if s.LoadBearing {
			n++
		}
	}
	return n
}

// SynthesizeSafe simulates a correctness-respecting compression pass.
// It may drop non-load-bearing sections (modeling: pruning irrelevant
// detail) but never drops load-bearing ones. Replaces the section
// list with the survivors.
func (p *Portrait) SynthesizeSafe(dropAt int) {
	// Drop every dropAt-th non-load-bearing section. The exact policy
	// doesn't matter — only that load-bearing sections are spared.
	if dropAt < 2 {
		dropAt = 2
	}
	out := p.Sections[:0]
	i := 0
	for _, s := range p.Sections {
		if s.LoadBearing {
			out = append(out, s)
			continue
		}
		if i%dropAt != 0 {
			out = append(out, s)
		}
		i++
	}
	p.Sections = out
}

// SynthesizeBuggy simulates the "got fast at the cost of honouring the
// marker" failure mode. It drops sections proportional to
// CompressionAggressiveness regardless of LoadBearing. This is the
// implementation the trajectory Property is designed to catch.
//
// Drop policy: drop every Nth section where N is computed from the
// aggressiveness. The bug is the absence of a LoadBearing check.
func (p *Portrait) SynthesizeBuggy(dropAt int) {
	if dropAt < 2 {
		dropAt = 2
	}
	out := p.Sections[:0]
	for i, s := range p.Sections {
		if i%dropAt != 0 {
			out = append(out, s)
		}
		// Bug: no LoadBearing check.
		_ = s
	}
	p.Sections = out
}
