// Package agent defines Tribunal agent identity, roles, and keypair management.
package agent

import "fmt"

// Role is the responsibility an agent has in the Tribunal workflow. Each role
// has a single, narrow purpose and corresponds to a markdown agent definition
// at agents/tribunal-<role>.md.
type Role string

const (
	RoleProjectManager Role = "project-manager"
	RoleArchitect      Role = "architect"
	RoleImplementer    Role = "implementer"
	RoleReviewerArch   Role = "reviewer-arch"
	RoleReviewerSec    Role = "reviewer-sec"
	RoleReviewerPerf   Role = "reviewer-perf"
	RoleAdversary      Role = "adversary"
	RoleClassifier     Role = "classifier"
	RoleQA             Role = "qa"
)

// AllRoles enumerates every valid role. Used by CLI validators.
func AllRoles() []Role {
	return []Role{
		RoleProjectManager,
		RoleArchitect,
		RoleImplementer,
		RoleReviewerArch,
		RoleReviewerSec,
		RoleReviewerPerf,
		RoleAdversary,
		RoleClassifier,
		RoleQA,
	}
}

// ParseRole returns the canonical Role for the given string or an error if
// the value isn't a known role.
func ParseRole(s string) (Role, error) {
	for _, r := range AllRoles() {
		if string(r) == s {
			return r, nil
		}
	}
	return "", fmt.Errorf("unknown role %q", s)
}

// CanResolveFindings reports whether agents with this role can act as
// resolvers on findings (i.e., mark them true_positive / false_positive).
// Only PMs and QA can resolve.
func (r Role) CanResolveFindings() bool {
	return r == RoleProjectManager || r == RoleQA
}

// IsReviewer reports whether this role is one of the lens-parallel
// reviewers.
func (r Role) IsReviewer() bool {
	return r == RoleReviewerArch || r == RoleReviewerSec || r == RoleReviewerPerf
}
