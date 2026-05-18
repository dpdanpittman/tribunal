package converge

import (
	"fmt"
	"strings"

	"github.com/dpdanpittman/tribunal/internal/dispatch"
)

// PanelRotator selects the adversary panel for round N given history.
// A converging loop only converges if the panel composition meaningfully
// changes between rounds — same panel = either re-finds the same defects
// (no progress) or passes when it shouldn't (false convergence on the
// original blind spot). See docs/convergence.md "panel rotation".
type PanelRotator interface {
	// NextPanel returns the composition for the round AFTER the history.
	// Round numbers in PanelComposition.Round are 1-indexed: NextPanel
	// returns Round=len(history)+1.
	NextPanel(history []RoundResult, cfg dispatch.Config) (PanelComposition, error)
}

// SelectRotator returns the named rotator. M1 ships:
//
//   - "focus-shuffle"  — permute each member's Focus axis per round.
//     Local-only; works even when only one provider is configured.
//   - "composite"      — composite:focus,model_tier rotation. The v0.4.1
//     default; produces meaningful inter-round diversity for the
//     intra-Claude default panel.
//
// Unknown names error out so a typo doesn't silently fall back to a
// rotator the operator didn't ask for.
func SelectRotator(spec string) (PanelRotator, error) {
	if spec == "" || spec == "default" {
		return &CompositeRotator{Axes: []string{"focus", "model_tier"}}, nil
	}
	if spec == "focus-shuffle" {
		return &FocusShuffleRotator{}, nil
	}
	if strings.HasPrefix(spec, "composite:") {
		axes := strings.Split(strings.TrimPrefix(spec, "composite:"), ",")
		for i := range axes {
			axes[i] = strings.TrimSpace(axes[i])
		}
		return &CompositeRotator{Axes: axes}, nil
	}
	return nil, fmt.Errorf("converge: unknown rotation strategy %q (known: focus-shuffle, composite:axis1,axis2,...)", spec)
}

// FocusShuffleRotator cycles each member's Focus axis through a fixed
// permutation per round. Member i in round R uses focus
// focuses[(baseIndexOf(member.Focus) + R) % len(focuses)].
//
// The base panel composition (providers, models, temperatures, labels)
// stays constant; only the Focus assignment varies. This is the cheapest
// rotation and works in any environment.
type FocusShuffleRotator struct{}

// focusCycle is the default rotation order. Members configured with a
// focus outside this list keep their original focus (no rotation).
var focusCycle = []string{"spec", "impl", "temporal", "security", "performance"}

func (r *FocusShuffleRotator) NextPanel(history []RoundResult, cfg dispatch.Config) (PanelComposition, error) {
	round := len(history) + 1
	base := cfg.DefaultPanel
	if len(base) == 0 {
		return PanelComposition{}, fmt.Errorf("converge: default_panel is empty (configure tribunal.yaml)")
	}
	members := make([]dispatch.PanelMember, len(base))
	for i, m := range base {
		members[i] = m
		idx := indexOf(focusCycle, m.Focus)
		if idx < 0 {
			continue
		}
		members[i].Focus = focusCycle[(idx+round-1)%len(focusCycle)]
	}
	return PanelComposition{
		Round:        round,
		Members:      members,
		RotationAxis: "focus",
	}, nil
}

// CompositeRotator combines multiple atomic rotation axes by applying
// each in sequence. Only "focus" is an active variation in M1; the
// "model_tier" axis stays as a documented placeholder that doesn't
// actually swap models (M2 will, once we have a model pool config).
type CompositeRotator struct {
	Axes []string
}

func (r *CompositeRotator) NextPanel(history []RoundResult, cfg dispatch.Config) (PanelComposition, error) {
	round := len(history) + 1
	base := cfg.DefaultPanel
	if len(base) == 0 {
		return PanelComposition{}, fmt.Errorf("converge: default_panel is empty (configure tribunal.yaml)")
	}
	members := make([]dispatch.PanelMember, len(base))
	copy(members, base)
	for _, axis := range r.Axes {
		switch axis {
		case "focus":
			for i, m := range members {
				idx := indexOf(focusCycle, m.Focus)
				if idx < 0 {
					continue
				}
				members[i].Focus = focusCycle[(idx+round-1)%len(focusCycle)]
			}
		case "model_tier":
			// Placeholder for M2: requires a configured model pool per
			// tier so we can swap opus↔sonnet↔haiku slots across rounds.
			// M1 documents the axis but doesn't actually swap models —
			// the rotation that ACTUALLY varies behavior across rounds
			// is the focus axis, which works without extra config.
		default:
			return PanelComposition{}, fmt.Errorf("converge: composite rotator: unknown axis %q", axis)
		}
	}
	return PanelComposition{
		Round:        round,
		Members:      members,
		RotationAxis: "composite:" + strings.Join(r.Axes, ","),
	}, nil
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
