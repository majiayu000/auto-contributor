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

	got := ClassifyOutcome(prInfo, nil, &models.PullRequest{})
	if got != OutcomeHostileSpam {
		t.Fatalf("ClassifyOutcome() = %q, want %q", got, OutcomeHostileSpam)
	}
}
