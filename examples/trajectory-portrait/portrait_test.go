package portrait

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"

	"github.com/dpdanpittman/tribunal/trajectory"
)

// loadBearingProperty constructs the Property derived from a temporal-
// lens finding: "no Synthesize pass on the portrait should ever
// decrease the count of load-bearing sections."
//
// useBuggy switches the SUT's Synthesize op between the safe and
// buggy implementations so the two tests below can share the same
// Property scaffolding.
func loadBearingProperty(useBuggy bool, findingID string) trajectory.Property {
	var seenLoadBearing int

	return trajectory.Property{
		FindingID:   findingID,
		Name:        "portrait-synthesize-preserves-load-bearing",
		Description: "After every Synthesize pass, the count of load-bearing sections must never drop below the historical maximum reached during the trajectory.",
		SetUp: func(t *rapid.T) trajectory.SUT {
			p := New()
			// Initial state: a small mix of load-bearing and regular
			// sections. Rapid will explore growth from here.
			initialLB := rapid.IntRange(1, 3).Draw(t, "initial-load-bearing")
			initialReg := rapid.IntRange(0, 3).Draw(t, "initial-regular")
			for i := 0; i < initialLB; i++ {
				p.Add(Section{Name: fmt.Sprintf("LB-%d", i), LoadBearing: true})
			}
			for i := 0; i < initialReg; i++ {
				p.Add(Section{Name: fmt.Sprintf("REG-%d", i)})
			}
			seenLoadBearing = p.LoadBearingCount()
			return p
		},
		Operations: map[string]func(*rapid.T, trajectory.SUT){
			"add-load-bearing": func(t *rapid.T, sut trajectory.SUT) {
				p := sut.(*Portrait)
				name := fmt.Sprintf("LB-%d", len(p.Sections))
				p.Add(Section{Name: name, LoadBearing: true})
				if got := p.LoadBearingCount(); got > seenLoadBearing {
					seenLoadBearing = got
				}
			},
			"add-regular": func(t *rapid.T, sut trajectory.SUT) {
				p := sut.(*Portrait)
				name := fmt.Sprintf("REG-%d", len(p.Sections))
				p.Add(Section{Name: name})
			},
			"edit": func(t *rapid.T, sut trajectory.SUT) {
				p := sut.(*Portrait)
				if len(p.Sections) == 0 {
					t.Skip("no sections to edit")
				}
				idx := rapid.IntRange(0, len(p.Sections)-1).Draw(t, "edit-idx")
				p.Edit(p.Sections[idx].Name, "edited-content")
			},
			"synthesize": func(t *rapid.T, sut trajectory.SUT) {
				p := sut.(*Portrait)
				dropAt := rapid.IntRange(2, 5).Draw(t, "drop-at")
				if useBuggy {
					p.SynthesizeBuggy(dropAt)
				} else {
					p.SynthesizeSafe(dropAt)
				}
			},
		},
		Invariants: []trajectory.Invariant{
			{
				Name: "load-bearing-monotone",
				Check: func(t *rapid.T, sut trajectory.SUT) {
					p := sut.(*Portrait)
					got := p.LoadBearingCount()
					if got < seenLoadBearing {
						t.Fatalf(
							"load-bearing count dropped from %d (historical max) to %d — synthesize pass deleted a load-bearing section. sections now: %v",
							seenLoadBearing, got, sectionNames(p),
						)
					}
				},
			},
		},
	}
}

// TestPortraitProperty_Safe runs the property against SynthesizeSafe.
// Expected: passes. The safe implementation respects the LoadBearing
// marker; the invariant holds across any random trajectory rapid
// generates.
func TestPortraitProperty_Safe(t *testing.T) {
	prop := loadBearingProperty(false, "F-temporal-001")
	trajectory.Run(t, prop)
}

// TestPortraitProperty_Buggy_DemonstratesShrink runs the property
// against SynthesizeBuggy. The buggy implementation ignores the
// LoadBearing marker and will, with enough operations, delete a
// load-bearing section — at which point the invariant trips and rapid
// shrinks the trajectory to a minimal counterexample. The test is
// skipped in CI so the suite stays green; un-skip to see the
// counterexample.
//
// Counterexample shape (typical, post-shrink): one add-load-bearing
// followed by one synthesize with dropAt=2, which drops the section
// at index 0 (the load-bearing one).
func TestPortraitProperty_Buggy_DemonstratesShrink(t *testing.T) {
	t.Skip("educational — un-skip to watch rapid shrink the buggy counterexample")
	prop := loadBearingProperty(true, "F-temporal-001")
	trajectory.Run(t, prop)
}

func sectionNames(p *Portrait) []string {
	out := make([]string, len(p.Sections))
	for i, s := range p.Sections {
		marker := ""
		if s.LoadBearing {
			marker = " [LB]"
		}
		out[i] = s.Name + marker
	}
	return out
}
