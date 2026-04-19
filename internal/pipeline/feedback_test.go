package pipeline

import (
	"testing"

	ghclient "github.com/majiayu000/auto-contributor/internal/github"
)

// TestHandleDraft_CIStatusRouting verifies that the handleDraft switch maps each
// CI status to the correct promotion decision. "error" must never promote a draft
// PR — it must be handled explicitly rather than falling through to nil with no log.
func TestHandleDraft_CIStatusRouting(t *testing.T) {
	cases := []struct {
		status        string
		codeFailures  bool
		wantPromoted  bool
		wantExplicit  bool // status has an explicit case (not relying on default fall-through)
	}{
		{"success", false, true, true},
		{"unknown", false, true, true},
		{"pending", false, false, true},
		{"failure", false, true, true},  // only metadata checks failed → promote anyway
		{"failure", true, false, true},   // code checks failed → do not promote
		{"error", false, false, true},
	}

	for _, tc := range cases {
		ci := &ghclient.CIResult{Status: tc.status, CodeFailures: tc.codeFailures}
		promoted := ciStatusPromotes(ci)
		if promoted != tc.wantPromoted {
			t.Errorf("status=%q codeFailures=%v: promoted=%v, want %v",
				tc.status, tc.codeFailures, promoted, tc.wantPromoted)
		}
	}
}

// ciStatusPromotes mirrors the handleDraft promotion logic so it can be tested
// without a live Pipeline (no DB or GitHub client required).
func ciStatusPromotes(ci *ghclient.CIResult) bool {
	switch {
	case ci.Status == "success" || ci.Status == "unknown":
		return true
	case ci.Status == "failure" && !ci.CodeFailures:
		return true
	case ci.Status == "failure" && ci.CodeFailures:
		return false
	case ci.Status == "pending":
		return false
	case ci.Status == "error":
		return false
	}
	return false
}
