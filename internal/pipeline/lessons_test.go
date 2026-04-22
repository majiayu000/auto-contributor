package pipeline

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/internal/rules"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

func TestStampRuleValidation_LegacyRuleIDsRemainDistinctPerStage(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Format("2006-01-02")

	engineerRule := &rules.Rule{
		ID:         "legacy-shared-rule",
		Stage:      "engineer",
		Severity:   "medium",
		Confidence: 0.8,
		Source:     "synthesized",
		CreatedAt:  "2024-01-01",
		Body:       "Engineer-stage synthesized rule.",
	}
	reviewerRule := &rules.Rule{
		ID:         "legacy-shared-rule",
		Stage:      "reviewer",
		Severity:   "medium",
		Confidence: 0.8,
		Source:     "synthesized",
		CreatedAt:  "2024-01-01",
		Body:       "Reviewer-stage synthesized rule.",
	}
	writeRule(t, dir, engineerRule)
	writeRule(t, dir, reviewerRule)

	database, err := db.New(filepath.Join(dir, "pipeline.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	now := time.Now()
	for _, event := range []*models.PipelineEvent{
		{
			IssueID:         1,
			Repo:            "majiayu000/auto-contributor",
			IssueNumber:     40,
			Stage:           "engineer",
			Round:           1,
			StartedAt:       now,
			CompletedAt:     &now,
			DurationSeconds: 1,
			Verdict:         "PROCEED",
			Success:         true,
			ExperiencesUsed: `["legacy-shared-rule"]`,
		},
		{
			IssueID:         1,
			Repo:            "majiayu000/auto-contributor",
			IssueNumber:     40,
			Stage:           "reviewer",
			Round:           1,
			StartedAt:       now,
			CompletedAt:     &now,
			DurationSeconds: 1,
			Verdict:         "PROCEED",
			Success:         true,
			ExperiencesUsed: `["legacy-shared-rule"]`,
		},
	} {
		if err := database.RecordEvent(event); err != nil {
			t.Fatalf("RecordEvent(%s): %v", event.Stage, err)
		}
	}

	p := &Pipeline{
		db:         database,
		ruleLoader: rules.NewRuleLoader(dir),
	}
	if err := p.ruleLoader.Load(); err != nil {
		t.Fatalf("RuleLoader.Load: %v", err)
	}

	p.stampRuleValidation(&models.PullRequest{
		IssueID: 1,
		PRURL:   "https://github.com/majiayu000/auto-contributor/pull/40",
	})

	if err := p.ruleLoader.Reload(); err != nil {
		t.Fatalf("RuleLoader.Reload: %v", err)
	}

	for _, tc := range []struct {
		stage string
		rule  *rules.Rule
	}{
		{stage: "engineer", rule: engineerRule},
		{stage: "reviewer", rule: reviewerRule},
	} {
		got := p.ruleLoader.ByStageAndID(tc.stage, tc.rule.ID)
		if got == nil {
			t.Fatalf("rule %s/%s not found after stamp", tc.stage, tc.rule.ID)
		}
		if got.LastValidatedAt != today {
			t.Errorf("%s last_validated_at = %q, want %q", tc.stage, got.LastValidatedAt, today)
		}
	}
}
