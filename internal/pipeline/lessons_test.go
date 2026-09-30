package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/rules"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

func TestProcessPRCommentFetchFailureRemainsRetryable(t *testing.T) {
	for _, tc := range []struct {
		name, state, author, body, label string
		status                           models.PRStatus
		age                              time.Duration
	}{
		{"closed auto", "CLOSED", "majiayu000", "Closing due to extended inactivity.", OutcomeAutoClosed, models.PRStatusOpen, time.Hour},
		{"closed rejection", "CLOSED", "maintainer", "These changes are out of scope and should be removed.", OutcomeRejectedScope, models.PRStatusOpen, time.Hour},
		{"merged", "MERGED", "maintainer", "Thanks for addressing the feedback.", OutcomeMerged, models.PRStatusOpen, time.Hour},
		{"stale auto-close", "OPEN", "majiayu000", "Closing due to extended inactivity.", OutcomeAutoClosed, models.PRStatusOpen, 31 * 24 * time.Hour},
		{"CI auto-close", "OPEN", "majiayu000", "Closing: CI failures remain unresolved after multiple attempts.", OutcomeAutoClosed, models.PRStatusDraft, 8 * 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/bin/sh
case "$*" in
  "pr view "*) printf '%s' "$GH_TEST_PR_INFO" ;;
  "pr checks "*) printf '%s' '[{"name":"build","state":"FAILURE"}]' ;;
  "pr close "*) exit 0 ;;
  "api repos/owner/repo/issues/42/comments"*)
    if [ "$GH_TEST_FAIL_ISSUE_COMMENTS" = "1" ]; then
      printf 'temporary comment fetch failure\n' >&2
      exit 1
    fi
    printf '%s' "$GH_TEST_ISSUE_COMMENTS"
    ;;
  "api repos/owner/repo/pulls/42/comments"*) printf '%s' '[]' ;;
  *) printf 'unexpected arguments: %s\n' "$*" >&2; exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			setInfo := func(state string) {
				data, err := json.Marshal(map[string]any{"state": state, "isDraft": tc.status == models.PRStatusDraft, "reviews": []any{}})
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("GH_TEST_PR_INFO", string(data))
			}
			setInfo(tc.state)
			data, err := json.Marshal([]any{map[string]any{"body": tc.body, "user": map[string]string{"login": tc.author}}})
			if err != nil {
				t.Fatal(err)
			}
			commentsJSON := string(data)
			t.Setenv("GH_TEST_ISSUE_COMMENTS", commentsJSON)
			t.Setenv("GH_TEST_FAIL_ISSUE_COMMENTS", "1")

			database := newFeedbackTestDB(t)
			issue, pr := createFeedbackTestPR(t, database)
			pr.Status = tc.status
			pr.CreatedAt = time.Now().Add(-tc.age)
			pr.FeedbackRound = 2
			if err := database.UpdatePRStatus(pr.ID, pr.Status); err != nil {
				t.Fatal(err)
			}
			if err := database.SaveTrajectory(&models.Trajectory{IssueID: issue.ID, PRNumber: pr.PRNumber, Repo: issue.Repo, IssueNumber: issue.IssueNumber}); err != nil {
				t.Fatal(err)
			}
			if err := database.RecordEvent(&models.PipelineEvent{IssueID: issue.ID, Repo: issue.Repo, IssueNumber: issue.IssueNumber, Stage: "engineer", Round: 1, StartedAt: time.Now(), ExperiencesUsed: `["engineer/comment-retry"]`}); err != nil {
				t.Fatal(err)
			}
			rulesDir := t.TempDir()
			writeRule(t, rulesDir, &rules.Rule{ID: "comment-retry", Stage: "engineer", Severity: "medium", Confidence: 0.8, Source: "synthesized", QValue: 0.5, Body: "Retain retries until feedback is available."})
			cfg := &config.Config{WorkspaceDir: t.TempDir()}
			workspace := filepath.Join(cfg.WorkspaceDir, "owner-repo-70")
			if err := os.Mkdir(workspace, 0755); err != nil {
				t.Fatal(err)
			}
			p := &Pipeline{cfg: cfg, db: database, gh: ghclient.New(cfg), ruleLoader: rules.NewRuleLoader(rulesDir)}
			if err := p.ruleLoader.Load(); err != nil {
				t.Fatal(err)
			}

			for _, failure := range []string{"command", "malformed JSON"} {
				if failure == "malformed JSON" {
					t.Setenv("GH_TEST_FAIL_ISSUE_COMMENTS", "0")
					t.Setenv("GH_TEST_ISSUE_COMMENTS", "invalid JSON")
				}
				if err := p.ProcessPR(context.Background(), pr); err == nil || !strings.Contains(err.Error(), "issue comments") {
					t.Errorf("%s: ProcessPR error = %v, want issue-comment fetch error", failure, err)
				}
				status, _, _ := getFeedbackPRState(t, database, pr.ID)
				if status != string(tc.status) {
					t.Errorf("status = %q, want retryable %q", status, tc.status)
				}
				openPRs, err := database.GetOpenPRs()
				if err != nil || len(openPRs) != 1 {
					t.Errorf("GetOpenPRs = %v, %v, want one retryable PR", openPRs, err)
				}
				events, err := database.GetEventsByIssue(issue.ID)
				if err != nil || len(events) != 1 || events[0].OutcomeLabel != "" {
					t.Errorf("events = %v, %v, want no outcome label", events, err)
				}
				var label sql.NullString
				if err := database.QueryRow("SELECT outcome_label FROM trajectories WHERE issue_id = ? AND pr_number = ?", issue.ID, pr.PRNumber).Scan(&label); err != nil {
					t.Fatal(err)
				}
				if label.String != "" {
					t.Errorf("trajectory label = %q, want no outcome", label.String)
				}
				if rule := loadRule(t, p.ruleLoader, "comment-retry"); rule.QValue != 0.5 || rule.RetrievalCount != 0 || rule.LastValidatedAt != "" {
					t.Errorf("rule was updated after failed fetch: %+v", rule)
				}
				if _, err := os.Stat(workspace); err != nil {
					t.Errorf("workspace removed after failed fetch: %v", err)
				}
				if tc.state == "OPEN" {
					setInfo("CLOSED") // The remote auto-close succeeded; the next poll sees CLOSED.
				}
			}

			t.Setenv("GH_TEST_ISSUE_COMMENTS", commentsJSON)
			if err := p.ProcessPR(context.Background(), pr); err != nil {
				t.Fatalf("successful retry: %v", err)
			}
			openPRs, err := database.GetOpenPRs()
			if err != nil || len(openPRs) != 0 {
				t.Errorf("GetOpenPRs after retry = %v, %v, want no open PRs", openPRs, err)
			}
			events, err := database.GetEventsByIssue(issue.ID)
			if err != nil || len(events) != 1 || events[0].OutcomeLabel != tc.label {
				t.Errorf("events after retry = %v, %v, want label %q", events, err, tc.label)
			}
			var label string
			var success bool
			if err := database.QueryRow("SELECT outcome_label, success FROM trajectories WHERE issue_id = ? AND pr_number = ?", issue.ID, pr.PRNumber).Scan(&label, &success); err != nil {
				t.Fatal(err)
			}
			if label != tc.label || success != (tc.label == OutcomeMerged) {
				t.Errorf("trajectory = %q, %v, want %q, %v", label, success, tc.label, tc.label == OutcomeMerged)
			}
			rule := loadRule(t, p.ruleLoader, "comment-retry")
			wantQ := 0.5 + qAlpha*(rewardForOutcome(tc.label)-0.5)
			if math.Abs(rule.QValue-wantQ) > 1e-9 || rule.RetrievalCount != 1 {
				t.Errorf("Q-value/count = %v/%d, want %v/1", rule.QValue, rule.RetrievalCount, wantQ)
			}
			profile, err := database.GetRepoProfile(issue.Repo)
			if err != nil || profile == nil || profile.TotalPRsSubmitted != 1 {
				t.Errorf("profile = %+v, %v, want outcome counted once", profile, err)
			}
		})
	}
}

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

func TestStampRuleValidation_LegacyRuleIDsStampOnlyMatchingStageAndGlobal(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Format("2006-01-02")

	for _, rule := range []*rules.Rule{
		{
			ID:         "legacy-shared-rule",
			Stage:      "engineer",
			Severity:   "medium",
			Confidence: 0.8,
			Source:     "synthesized",
			CreatedAt:  "2024-01-01",
			Body:       "Engineer-stage synthesized rule.",
		},
		{
			ID:         "legacy-shared-rule",
			Stage:      "reviewer",
			Severity:   "medium",
			Confidence: 0.8,
			Source:     "synthesized",
			CreatedAt:  "2024-01-01",
			Body:       "Reviewer-stage synthesized rule.",
		},
		{
			ID:         "legacy-shared-rule",
			Stage:      "global",
			Severity:   "medium",
			Confidence: 0.8,
			Source:     "synthesized",
			CreatedAt:  "2024-01-01",
			Body:       "Global synthesized rule.",
		},
	} {
		writeRule(t, dir, rule)
	}

	database, err := db.New(filepath.Join(dir, "pipeline.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	now := time.Now()
	if err := database.RecordEvent(&models.PipelineEvent{
		IssueID:         1,
		Repo:            "majiayu000/auto-contributor",
		IssueNumber:     41,
		Stage:           "engineer",
		Round:           1,
		StartedAt:       now,
		CompletedAt:     &now,
		DurationSeconds: 1,
		Verdict:         "PROCEED",
		Success:         true,
		ExperiencesUsed: `["legacy-shared-rule"]`,
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
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
		PRURL:   "https://github.com/majiayu000/auto-contributor/pull/41",
	})

	if err := p.ruleLoader.Reload(); err != nil {
		t.Fatalf("RuleLoader.Reload: %v", err)
	}

	for _, stage := range []string{"engineer", "global"} {
		got := p.ruleLoader.ByStageAndID(stage, "legacy-shared-rule")
		if got == nil {
			t.Fatalf("rule %s/legacy-shared-rule not found after stamp", stage)
		}
		if got.LastValidatedAt != today {
			t.Errorf("%s last_validated_at = %q, want %q", stage, got.LastValidatedAt, today)
		}
	}

	reviewer := p.ruleLoader.ByStageAndID("reviewer", "legacy-shared-rule")
	if reviewer == nil {
		t.Fatal("rule reviewer/legacy-shared-rule not found after stamp")
	}
	if reviewer.LastValidatedAt != "" {
		t.Errorf("reviewer last_validated_at = %q, want empty", reviewer.LastValidatedAt)
	}
}
