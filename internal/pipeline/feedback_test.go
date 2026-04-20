package pipeline

import (
	"testing"
	"time"

	ghclient "github.com/majiayu000/auto-contributor/internal/github"
)

// TestShouldPromoteDraft verifies the promotion decision for every CI status,
// including the critical contract: "unknown" must never trigger promotion.
func TestShouldPromoteDraft(t *testing.T) {
	cases := []struct {
		name        string
		ci          *ghclient.CIResult
		wantPromote bool
	}{
		{
			name:        "unknown must not promote (parse error path)",
			ci:          &ghclient.CIResult{Status: "unknown"},
			wantPromote: false,
		},
		{
			name:        "success promotes",
			ci:          &ghclient.CIResult{Status: "success"},
			wantPromote: true,
		},
		{
			name:        "pending does not promote",
			ci:          &ghclient.CIResult{Status: "pending"},
			wantPromote: false,
		},
		{
			name:        "metadata-only failure promotes",
			ci:          &ghclient.CIResult{Status: "failure", CodeFailures: false, FailedChecks: []string{"DCO"}},
			wantPromote: true,
		},
		{
			name:        "code failure does not promote",
			ci:          &ghclient.CIResult{Status: "failure", CodeFailures: true, FailedChecks: []string{"build"}},
			wantPromote: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldPromoteDraft(tc.ci)
			if got != tc.wantPromote {
				t.Errorf("shouldPromoteDraft(%+v) = %v, want %v", tc.ci, got, tc.wantPromote)
			}
		})
	}
}

func TestRepoFromPRURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://github.com/owner/repo/pull/42", "owner/repo"},
		{"https://github.com/org/project/pull/1", "org/project"},
		{"not-a-url", ""},
		{"https://gitlab.com/owner/repo/pull/1", ""},
	}
	for _, tc := range cases {
		got := repoFromPRURL(tc.url)
		if got != tc.want {
			t.Errorf("repoFromPRURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestPRResponseHours_BothTimestamps(t *testing.T) {
	created := "2024-01-01T00:00:00Z"
	terminal := "2024-01-01T02:00:00Z"
	h := prResponseHours(created, terminal, time.Now())
	if h != 2.0 {
		t.Errorf("prResponseHours = %v, want 2.0", h)
	}
}

func TestPRResponseHours_Fallback(t *testing.T) {
	fallback := time.Now().Add(-3 * time.Hour)
	h := prResponseHours("", "", fallback)
	if h < 2.9 || h > 3.1 {
		t.Errorf("prResponseHours fallback = %v, want ~3.0", h)
	}
}
