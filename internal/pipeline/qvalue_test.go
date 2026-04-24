package pipeline

import "testing"

func TestRewardForOutcome_HostileSpamIsNeutral(t *testing.T) {
	if got := rewardForOutcome(OutcomeHostileSpam); got != 0.5 {
		t.Fatalf("rewardForOutcome(%q) = %v, want 0.5", OutcomeHostileSpam, got)
	}
}
