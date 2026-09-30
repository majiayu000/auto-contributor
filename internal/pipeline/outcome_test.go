package pipeline

import (
	"testing"

	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

func TestClassifyOutcome_HostileSpamLockReason(t *testing.T) {
	prInfo := &ghclient.PRInfo{
		State:      "CLOSED",
		LockReason: "SPAM",
	}

	got := ClassifyOutcome(prInfo, nil, nil, &models.PullRequest{}, "")
	if got != OutcomeHostileSpam {
		t.Fatalf("ClassifyOutcome() = %q, want %q", got, OutcomeHostileSpam)
	}
}

func TestClassifyOutcome_HostileSpamLockReasonLowercase(t *testing.T) {
	prInfo := &ghclient.PRInfo{
		State:      "CLOSED",
		LockReason: "spam",
	}

	got := ClassifyOutcome(prInfo, nil, nil, &models.PullRequest{}, "")
	if got != OutcomeHostileSpam {
		t.Fatalf("ClassifyOutcome() = %q, want %q", got, OutcomeHostileSpam)
	}
}

func TestClassifyOutcome_IgnoresInlineBots(t *testing.T) {
	prInfo := &ghclient.PRInfo{State: "CLOSED", Reviews: []ghclient.PRReview{
		{Author: "maintainer", Body: "These changes are out of scope."},
	}}
	comments := []ghclient.PRReviewComment{{Author: "review-bot", Body: "duplicate"}}
	if got := ClassifyOutcome(prInfo, nil, comments, &models.PullRequest{}, ""); got != OutcomeRejectedScope {
		t.Fatalf("ClassifyOutcome() = %q, want %q from human feedback", got, OutcomeRejectedScope)
	}
}
