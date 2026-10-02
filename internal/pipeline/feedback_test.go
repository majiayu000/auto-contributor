package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/prompt"
	"github.com/majiayu000/auto-contributor/internal/rules"
	"github.com/majiayu000/auto-contributor/pkg/models"
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
			name:        "pending does not promote even with metadata failure present",
			ci:          &ghclient.CIResult{Status: "pending", CodeFailures: false, FailedChecks: []string{"DCO"}},
			wantPromote: false,
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

func TestFinalizeResponderActionCloseClosesRemoteBeforeLocalStatus(t *testing.T) {
	logPath := installFakeGH(t, false)
	database := newFeedbackTestDB(t)
	_, pr := createFeedbackTestPR(t, database)
	p := &Pipeline{
		cfg: &config.Config{WorkspaceDir: t.TempDir()},
		db:  database,
		gh:  ghclient.New(&config.Config{}),
	}

	err := p.finalizeResponderAction(
		context.Background(),
		pr,
		"owner/repo",
		&ghclient.PRInfo{State: "OPEN"},
		nil, nil,
		FeedbackResult{Action: "close"},
		pr.FeedbackRound+1,
	)
	if err != nil {
		t.Fatalf("finalizeResponderAction: %v", err)
	}

	status, round, checked := getFeedbackPRState(t, database, pr.ID)
	if status != string(models.PRStatusClosed) {
		t.Fatalf("status = %q, want %q", status, models.PRStatusClosed)
	}
	if round != 1 {
		t.Fatalf("feedback_round = %d, want 1", round)
	}
	if !checked.Valid {
		t.Fatal("last_feedback_check_at is NULL, want timestamp after remote close")
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake gh log: %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "pr close 42 -R owner/repo -c Closing because maintainer feedback explicitly asked to close or abandon this PR.") {
		t.Fatalf("fake gh log = %q, want remote close command with close comment", logText)
	}
}

func TestFinalizeResponderActionCloseFailureLeavesPRRetryable(t *testing.T) {
	installFakeGH(t, true)
	database := newFeedbackTestDB(t)
	_, pr := createFeedbackTestPR(t, database)
	p := &Pipeline{
		db: database,
		gh: ghclient.New(&config.Config{}),
	}

	err := p.finalizeResponderAction(
		context.Background(),
		pr,
		"owner/repo",
		&ghclient.PRInfo{State: "OPEN"},
		nil, nil,
		FeedbackResult{Action: "close"},
		pr.FeedbackRound+1,
	)
	if err == nil {
		t.Fatal("finalizeResponderAction error = nil, want remote close failure")
	}

	status, round, checked := getFeedbackPRState(t, database, pr.ID)
	if status != string(models.PRStatusOpen) {
		t.Fatalf("status = %q, want %q after remote close failure", status, models.PRStatusOpen)
	}
	if round != 0 {
		t.Fatalf("feedback_round = %d, want 0 after remote close failure", round)
	}
	if checked.Valid {
		t.Fatalf("last_feedback_check_at = %q, want NULL after remote close failure", checked.String)
	}
}

func TestExecuteResponderActionCloseFailureSkipsReplies(t *testing.T) {
	logPath := installFakeGH(t, true)
	database := newFeedbackTestDB(t)
	_, pr := createFeedbackTestPR(t, database)
	p := &Pipeline{
		db: database,
		gh: ghclient.New(&config.Config{}),
	}

	err := p.executeResponderAction(
		context.Background(),
		pr,
		"owner/repo",
		&ghclient.PRInfo{State: "OPEN"},
		nil, nil,
		FeedbackResult{
			Action: "close",
			Replies: []FeedbackReply{
				{CommentID: 123, Body: "Acknowledged; closing as requested."},
			},
		},
		pr.FeedbackRound+1,
	)
	if err == nil {
		t.Fatal("executeResponderAction error = nil, want remote close failure")
	}

	status, round, checked := getFeedbackPRState(t, database, pr.ID)
	if status != string(models.PRStatusOpen) {
		t.Fatalf("status = %q, want %q after remote close failure", status, models.PRStatusOpen)
	}
	if round != 0 {
		t.Fatalf("feedback_round = %d, want 0 after remote close failure", round)
	}
	if checked.Valid {
		t.Fatalf("last_feedback_check_at = %q, want NULL after remote close failure", checked.String)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake gh log: %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "pr close 42 -R owner/repo") {
		t.Fatalf("fake gh log = %q, want remote close attempt", logText)
	}
	if strings.Contains(logText, "comments/123/replies") {
		t.Fatalf("fake gh log = %q, reply was posted before remote close succeeded", logText)
	}
}

func TestResponderCloseLearnsOutcomeAndCleansWorkspace(t *testing.T) {
	installFakeGH(t, false)
	database := newFeedbackTestDB(t)
	issue, pr := createFeedbackTestPR(t, database)
	p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)

	prInfo := &ghclient.PRInfo{State: "OPEN", Reviews: []ghclient.PRReview{
		{Author: "reviewer", State: "CHANGES_REQUESTED", Body: "Please add a test for the incorrect close behavior."},
	}}
	issueComments := []ghclient.IssueComment{{Author: "maintainer", Body: "Please close this PR; these changes are out of scope."}}
	if err := p.executeResponderAction(context.Background(), pr, issue.Repo, prInfo, nil, issueComments, FeedbackResult{Action: "close"}, 1); err != nil {
		t.Fatalf("executeResponderAction: %v", err)
	}

	profile, err := database.GetRepoProfile(issue.Repo)
	if err != nil {
		t.Fatalf("GetRepoProfile: %v", err)
	}
	if profile == nil || profile.TotalPRsSubmitted != 1 || profile.TotalRejected != 1 || profile.TotalMerged != 0 || profile.MergeRate != 0 {
		t.Errorf("repo profile = %+v, want one rejected PR", profile)
	}
	if profile != nil && (profile.AvgResponseTimeHours == nil || *profile.AvgResponseTimeHours < 1.9 || *profile.AvgResponseTimeHours > 2.1) {
		t.Errorf("response time = %v, want approximately two hours", profile.AvgResponseTimeHours)
	}

	events, err := database.GetEventsByIssue(issue.ID)
	if err != nil {
		t.Fatalf("GetEventsByIssue: %v", err)
	}
	if len(events) != 1 || events[0].OutcomeLabel != OutcomeRejectedScope {
		t.Errorf("events = %+v, want rejected_scope label", events)
	}
	trajectories, err := database.GetRecentTrajectories(1)
	if err != nil {
		t.Fatalf("GetRecentTrajectories: %v", err)
	}
	if len(trajectories) != 1 || trajectories[0].OutcomeLabel != OutcomeRejectedScope || trajectories[0].Success {
		t.Errorf("trajectories = %+v, want rejected_scope and success=false", trajectories)
	}
	lessons, err := database.CountLessonsByPR(pr.ID)
	if err != nil {
		t.Fatalf("CountLessonsByPR: %v", err)
	}
	if lessons != 2 {
		t.Errorf("lessons = %d, want both cached review and maintainer scope lessons", lessons)
	}
	rule := loadRule(t, p.ruleLoader, "close-learning")
	if rule.QValue < 0.449 || rule.QValue > 0.451 || rule.RetrievalCount != 1 || rule.SuccessCount != 0 {
		t.Errorf("rule = %+v, want Q=0.45 and one unsuccessful retrieval", rule)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Errorf("workspace stat error = %v, want removed workspace", err)
	}
}

func TestResponderCloseDBFailureRemainsRetryable(t *testing.T) {
	for _, column := range []string{"outcome_recorded", "status"} {
		t.Run(column, func(t *testing.T) {
			logPath := installFakeGH(t, false)
			database := newFeedbackTestDB(t)
			issue, pr := createFeedbackTestPR(t, database)
			p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)
			if _, err := database.Exec("CREATE TRIGGER fail_close_write BEFORE UPDATE OF " + column + " ON pull_requests BEGIN SELECT RAISE(ABORT, 'close write failed'); END"); err != nil {
				t.Fatalf("create failure trigger: %v", err)
			}

			err := p.executeResponderAction(context.Background(), pr, issue.Repo, &ghclient.PRInfo{State: "OPEN"}, nil, nil, FeedbackResult{Action: "close"}, 1)
			if err == nil || !strings.Contains(err.Error(), "close write failed") {
				t.Fatalf("executeResponderAction error = %v, want database write failure", err)
			}
			status, round, checked := getFeedbackPRState(t, database, pr.ID)
			if status != string(models.PRStatusOpen) || round != 0 || checked.Valid {
				t.Errorf("PR state = %s, %d, %v, want open, 0, unchecked", status, round, checked)
			}
			openPRs, err := database.GetOpenPRs()
			if err != nil || len(openPRs) != 1 {
				t.Fatalf("GetOpenPRs = %v, %v, want PR available for retry", openPRs, err)
			}
			if _, err := os.Stat(workspace); err != nil {
				t.Errorf("workspace stat: %v, want retained workspace before retry", err)
			}
			logData, err := os.ReadFile(logPath)
			if err != nil || !strings.Contains(string(logData), "pr close 42 -R owner/repo") {
				t.Fatalf("fake gh log = %q, %v, want successful remote close before DB failure", logData, err)
			}

			if _, err := database.Exec("DROP TRIGGER fail_close_write"); err != nil {
				t.Fatalf("drop failure trigger: %v", err)
			}
			for _, env := range []string{"GH_TEST_FAIL_ISSUE_COMMENTS", "GH_TEST_FAIL_REVIEW_COMMENTS"} {
				t.Setenv(env, "1")
				if err := p.ProcessPR(context.Background(), openPRs[0]); err == nil || !strings.Contains(err.Error(), "comments") {
					t.Errorf("ProcessPR error = %v, want terminal feedback fetch failure", err)
				}
				status, round, checked = getFeedbackPRState(t, database, pr.ID)
				if status != string(models.PRStatusOpen) || round != 0 || checked.Valid {
					t.Errorf("PR state = %s, %d, %v, want open, 0, unchecked", status, round, checked)
				}
				if _, err := os.Stat(workspace); err != nil {
					t.Errorf("workspace stat: %v, want retained workspace after feedback fetch failure", err)
				}
				wantCount := 0
				if column == "status" {
					// Feedback learning succeeded before the failed terminal write.
					wantCount = 1
				}
				if rule := loadRule(t, p.ruleLoader, "close-learning"); rule.RetrievalCount != wantCount {
					t.Errorf("retrieval count = %d, want %d without another reward from failed fetch", rule.RetrievalCount, wantCount)
				}
				t.Setenv(env, "0")
			}
			if err := p.ProcessPR(context.Background(), openPRs[0]); err != nil {
				t.Fatalf("retry ProcessPR: %v", err)
			}
			profile, err := database.GetRepoProfile(issue.Repo)
			if err != nil || profile == nil || profile.TotalRejected != 1 || profile.TotalPRsSubmitted != 1 {
				t.Errorf("retried profile = %+v, %v, want one rejected PR without double counting", profile, err)
			}
			status, _, _ = getFeedbackPRState(t, database, pr.ID)
			if status != string(models.PRStatusClosed) {
				t.Errorf("retried PR status = %q, want closed", status)
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Errorf("retried workspace stat error = %v, want removed workspace", err)
			}
			rule := loadRule(t, p.ruleLoader, "close-learning")
			wantCount := 1
			if rule.RetrievalCount != wantCount {
				t.Errorf("retried retrieval count = %d, want %d", rule.RetrievalCount, wantCount)
			}
		})
	}
}

func TestResponderCloseUsesPolledFeedback(t *testing.T) {
	for _, tc := range []struct {
		name          string
		issueComments string
		inline        string
		wantLabel     string
		wantLessons   int
	}{
		{"issue scope", `[{"user":{"login":"maintainer"},"body":"Please close this PR; these changes are out of scope."}]`, `[]`, OutcomeRejectedScope, 1},
		{"inline duplicate", `[]`, `[{"user":{"login":"maintainer"},"body":"Please close this duplicate PR; the issue is already addressed.","path":"main.go"}]`, OutcomeRejectedDupe, 1},
		{"inline scope", `[]`, `[{"user":{"login":"maintainer"},"body":"Please close this PR; these changes are out of scope.","path":"main.go"}]`, OutcomeRejectedScope, 1},
		{"inline quality", `[]`, `[{"user":{"login":"maintainer"},"body":"Please close this PR; this logic is incorrect and broken.","path":"main.go"}]`, OutcomeRejectedQuality, 1},
		{"contributor reply", `[]`, `[{"user":{"login":"maintainer"},"body":"Please close this PR; naming style violates conventions.","path":"main.go"},{"user":{"login":"CONTRIBUTOR"},"body":"fixed the incorrect logic","path":"main.go"}]`, OutcomeRejectedStyle, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := installFakeGH(t, false)
			t.Setenv("GH_TEST_ISSUE_COMMENTS", tc.issueComments)
			t.Setenv("GH_TEST_REVIEW_COMMENTS", tc.inline)
			if tc.name == "issue scope" {
				t.Setenv("GH_TEST_FAIL_COMMENTS_AFTER_CLOSE", "1")
			}
			database := newFeedbackTestDB(t)
			issue, pr := createFeedbackTestPR(t, database)
			p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)
			p.cfg.GitHubUsername = "contributor"
			rt := prepareResponderCloseTest(t, p, pr, workspace)

			if err := p.handleOpen(context.Background(), pr, issue.Repo, &ghclient.PRInfo{State: "OPEN"}); err != nil {
				t.Fatalf("handleOpen: %v", err)
			}
			if rt.index != 1 || !strings.Contains(rt.prompts[0], "Please close") {
				t.Fatalf("responder calls = %d, prompts = %v, want maintainer close feedback", rt.index, rt.prompts)
			}
			events, err := database.GetEventsByIssue(issue.ID)
			if err != nil {
				t.Fatalf("GetEventsByIssue: %v", err)
			}
			for _, event := range events {
				if event.OutcomeLabel != tc.wantLabel {
					t.Errorf("outcome label = %q, want %q", event.OutcomeLabel, tc.wantLabel)
				}
			}
			trajectories, err := database.GetRecentTrajectories(1)
			if err != nil || len(trajectories) != 1 || trajectories[0].OutcomeLabel != tc.wantLabel || trajectories[0].Success {
				t.Errorf("trajectories = %+v, %v, want %q and success=false", trajectories, err, tc.wantLabel)
			}
			lessons, err := database.CountLessonsByPR(pr.ID)
			if err != nil || lessons != tc.wantLessons {
				t.Errorf("lessons = %d, %v, want %d cached feedback lessons", lessons, err, tc.wantLessons)
			}
			rule := loadRule(t, p.ruleLoader, "close-learning")
			if rule.QValue < 0.449 || rule.QValue > 0.451 || rule.RetrievalCount != 1 {
				t.Errorf("rule = %+v, want Q=0.45 and one retrieval", rule)
			}
			status, round, checked := getFeedbackPRState(t, database, pr.ID)
			if status != string(models.PRStatusClosed) || round != 1 || !checked.Valid {
				t.Errorf("PR state = %s, %d, %v, want closed, 1, checked", status, round, checked)
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Errorf("workspace stat error = %v, want removed workspace", err)
			}
			logData, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read fake gh log: %v", err)
			}
			for _, endpoint := range []string{"repos/owner/repo/issues/42/comments", "repos/owner/repo/pulls/42/comments"} {
				if count := strings.Count(string(logData), endpoint); count != 1 {
					t.Errorf("%s fetches = %d, want one fetch before close", endpoint, count)
				}
			}
		})
	}
}

func TestProcessPRLaterInlineCommentPages(t *testing.T) {
	for _, state := range []string{"OPEN", "CLOSED"} {
		for _, commentType := range []string{"inline", "issue"} {
			t.Run(state+"/"+commentType, func(t *testing.T) {
				installFakeGH(t, false)
				t.Setenv("GH_TEST_ISSUE_COMMENTS", `[]`)
				t.Setenv("GH_TEST_REVIEW_COMMENTS", `[{"user":{"login":"CONTRIBUTOR"},"body":"fixed the incorrect logic","path":"main.go"}]`)
				t.Setenv("GH_TEST_REVIEW_PAGE_2", `[{"user":{"login":"maintainer"},"body":"Please close this PR; these changes are out of scope.","path":"later.go"}]`)
				failEnv := "GH_TEST_FAIL_REVIEW_PAGE_2"
				if commentType == "issue" {
					t.Setenv("GH_TEST_REVIEW_COMMENTS", `[]`)
					t.Setenv("GH_TEST_REVIEW_PAGE_2", `[]`)
					t.Setenv("GH_TEST_ISSUE_COMMENTS", `[{"user":{"login":"CONTRIBUTOR"},"body":"fixed the incorrect logic"}]`)
					t.Setenv("GH_TEST_ISSUE_PAGE_2", `[{"user":{"login":"maintainer"},"body":"Please close this PR; these changes are out of scope."}]`)
					failEnv = "GH_TEST_FAIL_ISSUE_PAGE_2"
				}
				t.Setenv(failEnv, "1")
				database := newFeedbackTestDB(t)
				issue, pr := createFeedbackTestPR(t, database)
				p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)
				p.cfg.GitHubUsername = "contributor"
				var rt *stubRuntime
				if state == "OPEN" {
					rt = prepareResponderCloseTest(t, p, pr, workspace)
				}
				process := func() error {
					if state == "OPEN" {
						return p.handleOpen(context.Background(), pr, issue.Repo, &ghclient.PRInfo{State: state})
					}
					return p.ProcessPR(context.Background(), pr)
				}
				if err := process(); err == nil || !strings.Contains(err.Error(), "page 2 unavailable") {
					t.Fatalf("later page failure = %v, want fetch error", err)
				}
				openPRs, err := database.GetOpenPRs()
				if err != nil || len(openPRs) != 1 {
					t.Fatalf("open PRs = %v, %v, want retryable PR", openPRs, err)
				}
				events, err := database.GetEventsByIssue(issue.ID)
				if err != nil || len(events) != 1 || events[0].OutcomeLabel != "" {
					t.Fatalf("events = %v, %v, want no partial label", events, err)
				}
				if rule := loadRule(t, p.ruleLoader, "close-learning"); rule.QValue != 0.5 || rule.RetrievalCount != 0 {
					t.Fatalf("partial feedback rewarded rule: %+v", rule)
				}
				if _, err := os.Stat(workspace); err != nil {
					t.Fatalf("workspace lost before retry: %v", err)
				}
				if rt != nil && rt.index != 0 {
					t.Fatal("responder ran with partial feedback")
				}
				t.Setenv(failEnv, "0")
				if err := process(); err != nil {
					t.Fatalf("retry: %v", err)
				}
				if rt != nil && (rt.index != 1 || !strings.Contains(rt.prompts[0], "out of scope")) {
					t.Fatalf("responder did not receive later page: %+v", rt)
				}
				events, err = database.GetEventsByIssue(issue.ID)
				if err != nil || len(events) == 0 {
					t.Fatal(err)
				}
				for _, event := range events {
					if event.OutcomeLabel != OutcomeRejectedScope {
						t.Errorf("label = %q, want later maintainer scope over contributor reply", event.OutcomeLabel)
					}
				}
				if rule := loadRule(t, p.ruleLoader, "close-learning"); rule.QValue != 0.45 || rule.RetrievalCount != 1 {
					t.Errorf("reward = %+v, want scope reward exactly once", rule)
				}
				openPRs, err = database.GetOpenPRs()
				if err != nil || len(openPRs) != 0 {
					t.Errorf("open PRs after retry = %v, %v", openPRs, err)
				}
			})
		}
	}
}

func TestProcessPRSkipsMalformedParticipation(t *testing.T) {
	for _, state := range []string{"CLOSED", "MERGED"} {
		for _, malformed := range []string{`[invalid`, `["responder/corrupt-only",1]`} {
			t.Run(state+"/"+malformed, func(t *testing.T) {
				installFakeGH(t, false)
				t.Setenv("GH_TEST_PR_INFO", `{"state":"`+state+`"}`)
				t.Setenv("GH_TEST_ISSUE_COMMENTS", `[]`)
				database := newFeedbackTestDB(t)
				issue, pr := createFeedbackTestPR(t, database)
				p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)
				writeRule(t, p.ruleLoader.RulesDir(), &rules.Rule{ID: "corrupt-only", Stage: "responder", Severity: "medium", Confidence: 0.8, Source: "synthesized", QValue: 0.5, Body: "Do not learn from an undecodable participation record."})
				if err := p.ruleLoader.Reload(); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec("UPDATE pipeline_events SET experiences_used = ? WHERE issue_id = ?", malformed, issue.ID); err != nil {
					t.Fatal(err)
				}
				if err := database.RecordEvent(&models.PipelineEvent{IssueID: issue.ID, Repo: issue.Repo, IssueNumber: issue.IssueNumber, Stage: "responder", StartedAt: time.Now(), ExperiencesUsed: `["responder/close-learning"]`}); err != nil {
					t.Fatal(err)
				}
				if err := p.ProcessPR(context.Background(), pr); err != nil {
					t.Fatalf("terminal processing with malformed and valid events: %v", err)
				}
				wantQ, wantSuccess, wantStamp := 0.47, 0, ""
				wantStatus := models.PRStatusClosed
				if state == "MERGED" {
					wantQ, wantSuccess, wantStamp = 0.55, 1, time.Now().Format("2006-01-02")
					wantStatus = models.PRStatusMerged
				}
				valid := loadRule(t, p.ruleLoader, "close-learning")
				if valid.QValue < wantQ-0.001 || valid.QValue > wantQ+0.001 || valid.RetrievalCount != 1 || valid.SuccessCount != wantSuccess || valid.LastValidatedAt != wantStamp {
					t.Errorf("valid event rule = %+v, want reward %v success %d stamp %q", valid, wantQ, wantSuccess, wantStamp)
				}
				invalid := loadRule(t, p.ruleLoader, "corrupt-only")
				if invalid.QValue != 0.5 || invalid.RetrievalCount != 0 || invalid.SuccessCount != 0 || invalid.LastValidatedAt != "" {
					t.Errorf("malformed event changed rule: %+v", invalid)
				}
				status, _, _ := getFeedbackPRState(t, database, pr.ID)
				if status != string(wantStatus) {
					t.Errorf("status = %q, want %q", status, wantStatus)
				}
				openPRs, err := database.GetOpenPRs()
				if err != nil || len(openPRs) != 0 {
					t.Errorf("open PRs = %v, %v, want completed PR", openPRs, err)
				}
				if _, err := os.Stat(workspace); !os.IsNotExist(err) {
					t.Errorf("workspace retained after completion: %v", err)
				}
			})
		}
	}
}

func TestResponderCloseUsesGitHubCreationTime(t *testing.T) {
	installFakeGH(t, false)
	database := newFeedbackTestDB(t)
	issue, pr := createFeedbackTestPR(t, database)
	p, _ := newResponderLearningTestPipeline(t, database, issue, pr)
	created := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	prInfo := &ghclient.PRInfo{State: "OPEN", CreatedAt: created.Format(time.RFC3339)}
	beforeClose := time.Now()
	if err := p.executeResponderAction(context.Background(), pr, issue.Repo, prInfo, nil, nil, FeedbackResult{Action: "close"}, 1); err != nil {
		t.Fatalf("executeResponderAction: %v", err)
	}
	profile, err := database.GetRepoProfile(issue.Repo)
	if err != nil || profile == nil || profile.AvgResponseTimeHours == nil {
		t.Fatalf("GetRepoProfile = %+v, %v, want response time", profile, err)
	}
	if got := *profile.AvgResponseTimeHours; got < beforeClose.Add(-time.Second).Sub(created).Hours() || got > time.Since(created).Hours() {
		t.Errorf("response time = %v, want GitHub creation to just-completed close (~72 hours)", got)
	}
}

func TestResponderCloseLearningFailureRemainsRetryable(t *testing.T) {
	for _, failure := range []string{"event label", "trajectory", "second lesson", "second rule", "terminal status"} {
		t.Run(failure, func(t *testing.T) {
			logPath := installFakeGH(t, false)
			database := newFeedbackTestDB(t)
			issue, pr := createFeedbackTestPR(t, database)
			p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)
			p.cfg.GitHubUsername = "contributor"
			writeRule(t, p.ruleLoader.RulesDir(), &rules.Rule{ID: "second-learning", Stage: "engineer", Severity: "medium", Confidence: 0.8, QValue: 0.5, Body: "Keep learning retryable."})
			if err := p.ruleLoader.Reload(); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE pipeline_events SET experiences_used = '["responder/close-learning","engineer/second-learning"]' WHERE issue_id = ?`, issue.ID); err != nil {
				t.Fatal(err)
			}
			trigger := ""
			switch failure {
			case "event label":
				trigger = "BEFORE UPDATE OF outcome_label ON pipeline_events"
			case "trajectory":
				trigger = "BEFORE UPDATE OF outcome_label ON trajectories"
			case "second lesson":
				trigger = "BEFORE INSERT ON review_lessons WHEN NEW.reviewer = 'second-reviewer'"
			case "terminal status":
				trigger = "BEFORE UPDATE OF status ON pull_requests"
			}
			if trigger != "" {
				if _, err := database.Exec("CREATE TRIGGER fail_learning " + trigger + " BEGIN SELECT RAISE(ABORT, 'learning unavailable'); END"); err != nil {
					t.Fatal(err)
				}
			}
			rulePath := filepath.Join(p.ruleLoader.RulesDir(), "engineer", "second-learning.yaml")
			if failure == "second rule" {
				if err := os.Rename(rulePath, rulePath+".saved"); err != nil {
					t.Fatal(err)
				}
			}
			inline := []ghclient.PRReviewComment{
				{Author: "first-reviewer", Body: "Please keep the fix within the issue scope.", Path: "main.go"},
				{Author: "second-reviewer", Body: "Please add a test for this close behavior.", Path: "main_test.go"},
			}
			inlineJSON := `[{"user":{"login":"first-reviewer"},"body":"Please keep the fix within the issue scope.","path":"main.go"},{"user":{"login":"second-reviewer"},"body":"Please add a test for this close behavior.","path":"main_test.go"}]`
			t.Setenv("GH_TEST_REVIEW_COMMENTS", inlineJSON)
			t.Setenv("GH_TEST_ISSUE_COMMENTS", `[{"user":{"login":"contributor"},"body":"Closing because maintainer feedback explicitly asked to close or abandon this PR."}]`)
			err := p.executeResponderAction(context.Background(), pr, issue.Repo, &ghclient.PRInfo{State: "OPEN"}, inline, nil, FeedbackResult{Action: "close"}, 1)
			if err == nil {
				t.Errorf("close returned nil after %s failure", failure)
			}
			openPRs, err := database.GetOpenPRs()
			if err != nil || len(openPRs) != 1 {
				t.Errorf("GetOpenPRs = %v, %v, want one retryable PR", openPRs, err)
			}
			if _, err := os.Stat(workspace); err != nil {
				t.Errorf("workspace removed before learning completed: %v", err)
			}
			wantCount, wantQ := 0, 0.5
			if failure == "second rule" || failure == "terminal status" {
				// Preserve the current per-file reward writes: an earlier Q-value
				// can persist before a later rule or SQL terminal write fails.
				wantCount, wantQ = 1, 0.45
			}
			if rule := loadRule(t, p.ruleLoader, "close-learning"); rule.QValue < wantQ-0.001 || rule.QValue > wantQ+0.001 || rule.RetrievalCount != wantCount {
				t.Errorf("reward after failure = %+v, want Q=%v count=%d", rule, wantQ, wantCount)
			}
			if trigger != "" {
				if _, err := database.Exec("DROP TRIGGER fail_learning"); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "second rule" {
				if err := os.Rename(rulePath+".saved", rulePath); err != nil {
					t.Fatal(err)
				}
			}
			// Reload a fresh Pipeline to exercise retry from persisted data.
			p.ruleLoader = rules.NewRuleLoader(p.ruleLoader.RulesDir())
			if err := p.ruleLoader.Load(); err != nil {
				t.Fatal(err)
			}
			if err := p.ProcessPR(context.Background(), pr); err != nil {
				t.Fatalf("CLOSED retry: %v", err)
			}
			if count, err := database.CountLessonsByPR(pr.ID); err != nil || count != 2 {
				t.Errorf("lessons after retry = %d, %v, want both lessons exactly once", count, err)
			}
			for _, id := range []string{"close-learning", "second-learning"} {
				rule := loadRule(t, p.ruleLoader, id)
				wantCount, wantQ := 1, 0.45
				if rule.RetrievalCount != wantCount || rule.QValue < wantQ-0.001 || rule.QValue > wantQ+0.001 {
					t.Errorf("%s after retry = %+v, want Q=%v count=%d", id, rule, wantQ, wantCount)
				}
			}
			profile, err := database.GetRepoProfile(issue.Repo)
			if err != nil || profile == nil || profile.TotalPRsSubmitted != 1 || profile.TotalRejected != 1 {
				t.Errorf("profile after retry = %+v, %v, want one outcome", profile, err)
			}
			openPRs, err = database.GetOpenPRs()
			if err != nil || len(openPRs) != 0 {
				t.Errorf("GetOpenPRs after retry = %v, %v, want completed PR", openPRs, err)
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Errorf("workspace retained after successful retry: %v", err)
			}
			logData, err := os.ReadFile(logPath)
			if err != nil || strings.Count(string(logData), "pr close 42") != 1 {
				t.Errorf("remote close calls = %q, %v, want exactly one", logData, err)
			}
		})
	}
}

func TestResponderCloseGeneratedReasonMatchesClosedRetry(t *testing.T) {
	for _, contributor := range []string{"contributor", "contributor-bot"} {
		t.Run(contributor, func(t *testing.T) {
			installFakeGH(t, false)
			database := newFeedbackTestDB(t)
			issue, pr := createFeedbackTestPR(t, database)
			p, _ := newResponderLearningTestPipeline(t, database, issue, pr)
			p.cfg.GitHubUsername = contributor
			comments := []ghclient.IssueComment{{Author: "maintainer", Body: "please abandon this PR"}}
			if err := p.executeResponderAction(context.Background(), pr, issue.Repo, &ghclient.PRInfo{State: "OPEN"}, nil, comments, FeedbackResult{Action: "close"}, 1); err != nil {
				t.Fatal(err)
			}
			events, err := database.GetEventsByIssue(issue.ID)
			if err != nil || len(events) != 1 || events[0].OutcomeLabel != OutcomeRejectedUnwant {
				t.Errorf("first close = %v, %v, want rejected_unwanted", events, err)
			}
			comments = append(comments, ghclient.IssueComment{Author: contributor, Body: "Closing because maintainer feedback explicitly asked to close or abandon this PR."})
			if label := ClassifyOutcome(&ghclient.PRInfo{State: "CLOSED"}, comments, nil, pr, contributor); label != OutcomeRejectedUnwant {
				t.Errorf("CLOSED retry classification = %q, want rejected_unwanted", label)
			}
		})
	}
}

func TestResponderCloseFeedbackFetchFailureRemainsRetryable(t *testing.T) {
	logPath := installFakeGH(t, false)
	t.Setenv("GH_TEST_FAIL_ISSUE_COMMENTS", "1")
	database := newFeedbackTestDB(t)
	issue, pr := createFeedbackTestPR(t, database)
	p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)
	rt := prepareResponderCloseTest(t, p, pr, workspace)
	prInfo := &ghclient.PRInfo{State: "OPEN", Reviews: []ghclient.PRReview{
		{Author: "maintainer", State: "CHANGES_REQUESTED", Body: "Please close this PR; these changes are out of scope."},
	}}
	err := p.handleOpen(context.Background(), pr, issue.Repo, prInfo)
	if err == nil || !strings.Contains(err.Error(), "issue comments") {
		t.Errorf("handleOpen error = %v, want feedback fetch failure", err)
	}
	status, round, checked := getFeedbackPRState(t, database, pr.ID)
	if status != string(models.PRStatusOpen) || round != 0 || checked.Valid {
		t.Errorf("PR state = %s, %d, %v, want open, 0, unchecked", status, round, checked)
	}
	if rt.index != 0 {
		t.Errorf("responder calls = %d, want no calls with incomplete feedback", rt.index)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil || strings.Contains(string(logData), "pr close") {
		t.Errorf("fake gh log = %q, %v, want no remote close", logData, err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Errorf("workspace stat: %v, want retained workspace for retry", err)
	}

	t.Setenv("GH_TEST_FAIL_ISSUE_COMMENTS", "0")
	if err := p.handleOpen(context.Background(), pr, issue.Repo, prInfo); err != nil {
		t.Fatalf("retry handleOpen: %v", err)
	}
	profile, err := database.GetRepoProfile(issue.Repo)
	if err != nil || profile == nil || profile.TotalRejected != 1 || profile.TotalPRsSubmitted != 1 {
		t.Errorf("retried profile = %+v, %v, want one rejected PR", profile, err)
	}
}

func prepareResponderCloseTest(t *testing.T, p *Pipeline, pr *models.PullRequest, workspace string) *stubRuntime {
	t.Helper()
	runGitCommand(t, "", "init", "--initial-branch="+pr.BranchName, workspace)
	runGitCommand(t, workspace, "config", "user.name", "Test User")
	runGitCommand(t, workspace, "config", "user.email", "test@example.com")
	runGitCommand(t, workspace, "commit", "--allow-empty", "-m", "initial")
	remote := createBareRepo(t)
	runGitCommand(t, workspace, "remote", "add", "fork", remote)
	runGitCommand(t, workspace, "push", "fork", pr.BranchName)
	promptsDir := t.TempDir()
	writePromptTemplate(t, promptsDir, "responder", `{{.ReviewsData}} {{.InlineCommentsData}} {{.IssueCommentsData}}`)
	p.prompts = prompt.NewStore(promptsDir)
	if err := p.prompts.Load(); err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	rt := &stubRuntime{outputs: []stubOutput{{output: `{"action":"close"}`}}}
	p.runner = NewAgentRunner(p.prompts, rt, 0)
	return rt
}

func newResponderLearningTestPipeline(t *testing.T, database *db.DB, issue *models.Issue, pr *models.PullRequest) (*Pipeline, string) {
	t.Helper()
	rulesDir := t.TempDir()
	writeRule(t, rulesDir, &rules.Rule{
		ID: "close-learning", Stage: "responder", Severity: "medium", Confidence: 0.8,
		Source: "synthesized", QValue: 0.5, Body: "Respect maintainer scope feedback.",
	})
	pr.CreatedAt = time.Now().Add(-2 * time.Hour)
	if err := database.RecordEvent(&models.PipelineEvent{
		IssueID: issue.ID, Repo: issue.Repo, IssueNumber: issue.IssueNumber,
		Stage: "responder", StartedAt: time.Now(), ExperiencesUsed: `["responder/close-learning"]`,
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := database.SaveTrajectory(&models.Trajectory{
		IssueID: issue.ID, PRNumber: pr.PRNumber, Repo: issue.Repo, IssueNumber: issue.IssueNumber,
	}); err != nil {
		t.Fatalf("SaveTrajectory: %v", err)
	}
	cfg := &config.Config{WorkspaceDir: t.TempDir()}
	workspace := filepath.Join(cfg.WorkspaceDir, "owner-repo-70")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	p := &Pipeline{cfg: cfg, db: database, gh: ghclient.New(cfg), ruleLoader: rules.NewRuleLoader(rulesDir)}
	if err := p.ruleLoader.Load(); err != nil {
		t.Fatalf("load rules: %v", err)
	}
	return p, workspace
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

func installFakeGH(t *testing.T, failClose bool) string {
	t.Helper()

	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "gh.log")
	scriptPath := filepath.Join(tempDir, "gh")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$GH_TEST_LOG"
if [ "$GH_TEST_FAIL_CLOSE" = "1" ]; then
  printf 'close failed\n' >&2
  exit 1
fi
case "$*" in
  "pr close "*) : > "$GH_TEST_CLOSED" ;;
  "pr view "*)
    if [ -n "$GH_TEST_PR_INFO" ]; then
      printf '%s\n' "$GH_TEST_PR_INFO"
    else
      printf '%s\n' '{"state":"CLOSED","headRefName":"fix/close-action"}'
    fi ;;
  *"repos/owner/repo/issues/42/comments"*|*"repos/owner/repo/issues/43/comments"*)
    if [ "$GH_TEST_FAIL_ISSUE_COMMENTS" = "1" ] || { [ "$GH_TEST_FAIL_COMMENTS_AFTER_CLOSE" = "1" ] && [ -f "$GH_TEST_CLOSED" ]; }; then
      printf 'issue comments unavailable\n' >&2
      exit 1
    fi
    issue_comments=${GH_TEST_ISSUE_COMMENTS:-'[{"user":{"login":"maintainer"},"body":"Please close this PR; these changes are out of scope."}]'}
    case "$*" in
      *"--paginate --slurp"*)
        if [ "$GH_TEST_FAIL_ISSUE_PAGE_2" = "1" ]; then
          printf '[%s]\n' "$issue_comments"
          printf 'issue page 2 unavailable\n' >&2
          exit 1
        fi
        printf '[%s,%s]\n' "$issue_comments" "${GH_TEST_ISSUE_PAGE_2:-[]}" ;;
      *) printf '%s\n' "$issue_comments" ;;
    esac ;;
  *"repos/owner/repo/pulls/42/comments"*|*"repos/owner/repo/pulls/43/comments"*)
    if [ "$GH_TEST_FAIL_REVIEW_COMMENTS" = "1" ] || { [ "$GH_TEST_FAIL_COMMENTS_AFTER_CLOSE" = "1" ] && [ -f "$GH_TEST_CLOSED" ]; }; then
      printf 'inline comments unavailable\n' >&2
      exit 1
    fi
    case "$*" in
      *"--paginate --slurp"*)
        if [ "$GH_TEST_FAIL_REVIEW_PAGE_2" = "1" ]; then
          printf '%s\n' "[${GH_TEST_REVIEW_COMMENTS:-[]}]"
          printf 'review page 2 unavailable\n' >&2
          exit 1
        fi
        printf '%s\n' "[${GH_TEST_REVIEW_COMMENTS:-[]},${GH_TEST_REVIEW_PAGE_2:-[]}]" ;;
      *) printf '%s\n' "${GH_TEST_REVIEW_COMMENTS:-[]}" ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh script: %v", err)
	}

	t.Setenv("GH_TEST_LOG", logPath)
	t.Setenv("GH_TEST_CLOSED", filepath.Join(tempDir, "closed"))
	if failClose {
		t.Setenv("GH_TEST_FAIL_CLOSE", "1")
	} else {
		t.Setenv("GH_TEST_FAIL_CLOSE", "0")
	}
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func newFeedbackTestDB(t *testing.T) *db.DB {
	t.Helper()

	database, err := db.New(filepath.Join(t.TempDir(), "feedback.db"))
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	return database
}

func createFeedbackTestPR(t *testing.T, database *db.DB) (*models.Issue, *models.PullRequest) {
	t.Helper()

	issue := &models.Issue{
		Repo:            "owner/repo",
		IssueNumber:     70,
		Title:           "Close action regression",
		Body:            "close action should close remote PR first",
		Labels:          "review",
		Language:        "Go",
		DifficultyScore: 0.5,
		Status:          models.IssueStatusCompleted,
	}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	pr := &models.PullRequest{
		IssueID:    issue.ID,
		PRURL:      "https://github.com/owner/repo/pull/42",
		PRNumber:   42,
		BranchName: "fix/close-action",
		Status:     models.PRStatusOpen,
		CIStatus:   "success",
	}
	if err := database.CreatePullRequest(pr); err != nil {
		t.Fatalf("create pull request: %v", err)
	}
	return issue, pr
}

func getFeedbackPRState(t *testing.T, database *db.DB, prID int64) (string, int, sql.NullString) {
	t.Helper()

	var status string
	var round int
	var checked sql.NullString
	err := database.QueryRow(
		"SELECT status, feedback_round, last_feedback_check_at FROM pull_requests WHERE id = ?",
		prID,
	).Scan(&status, &round, &checked)
	if err != nil {
		t.Fatalf("query PR state: %v", err)
	}
	return status, round, checked
}

func TestProcessPRRewardRetryAfterAnotherPR(t *testing.T) {
	installFakeGH(t, false)
	t.Setenv("GH_TEST_PR_INFO", `{"state":"CLOSED"}`)
	t.Setenv("GH_TEST_ISSUE_COMMENTS", `[{"user":{"login":"maintainer"},"body":"Please close this PR; these changes are out of scope."}]`)
	database := newFeedbackTestDB(t)
	issueA, prA := createFeedbackTestPR(t, database)
	pA, _ := newResponderLearningTestPipeline(t, database, issueA, prA)

	issueB := &models.Issue{Repo: issueA.Repo, IssueNumber: 71, Title: "Another contribution", Status: models.IssueStatusCompleted}
	if err := database.CreateIssue(issueB); err != nil {
		t.Fatal(err)
	}
	prB := &models.PullRequest{IssueID: issueB.ID, PRNumber: 43, PRURL: "https://github.com/owner/repo/pull/43", BranchName: "fix/another", Status: models.PRStatusOpen}
	if err := database.CreatePullRequest(prB); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordEvent(&models.PipelineEvent{IssueID: issueB.ID, Repo: issueB.Repo, IssueNumber: issueB.IssueNumber, Stage: "responder", StartedAt: time.Now(), ExperiencesUsed: `["responder/close-learning","close-learning"]`}); err != nil {
		t.Fatal(err)
	}
	// B starts with a separate, stale loader so its reward must use A's persisted values.
	pB := *pA
	pB.ruleLoader = rules.NewRuleLoader(pA.ruleLoader.RulesDir())
	if err := pB.ruleLoader.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(fmt.Sprintf("CREATE TRIGGER fail_a_status BEFORE UPDATE OF status ON pull_requests WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, 'terminal write unavailable'); END", prA.ID)); err != nil {
		t.Fatal(err)
	}
	if err := pA.ProcessPR(context.Background(), prA); err == nil || !strings.Contains(err.Error(), "terminal write unavailable") {
		t.Fatalf("A error = %v, want final SQL failure after reward", err)
	}
	if rule := loadRule(t, pA.ruleLoader, "close-learning"); rule.QValue != 0.45 || rule.RetrievalCount != 1 || rule.SuccessCount != 0 {
		t.Fatalf("A reward = %+v, want Q=0.45, retrieval=1, success=0", rule)
	}
	t.Setenv("GH_TEST_PR_INFO", `{"state":"MERGED"}`)
	if err := pB.ProcessPR(context.Background(), prB); err != nil {
		t.Fatalf("B: %v", err)
	}
	if rule := loadRule(t, pB.ruleLoader, "close-learning"); rule.QValue < 0.5049 || rule.QValue > 0.5051 || rule.RetrievalCount != 2 || rule.SuccessCount != 1 {
		t.Errorf("B reward = %+v, want Q=0.505, retrieval=2, success=1", rule)
	}
	if _, err := database.Exec("DROP TRIGGER fail_a_status"); err != nil {
		t.Fatal(err)
	}
	// Restart A after B. Replaying A must neither reward it again nor lose B's reward.
	pA.ruleLoader = rules.NewRuleLoader(pA.ruleLoader.RulesDir())
	if err := pA.ruleLoader.Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TEST_PR_INFO", `{"state":"CLOSED"}`)
	if err := pA.ProcessPR(context.Background(), prA); err != nil {
		t.Fatalf("A retry: %v", err)
	}
	if rule := loadRule(t, pA.ruleLoader, "close-learning"); rule.QValue < 0.5049 || rule.QValue > 0.5051 || rule.RetrievalCount != 2 || rule.SuccessCount != 1 {
		t.Errorf("A/B/A reward = %+v, want Q=0.505, retrieval=2, success=1", rule)
	}
	if open, err := database.GetOpenPRs(); err != nil || len(open) != 0 {
		t.Errorf("open PRs = %v, %v, want both terminal transitions complete", open, err)
	}
	if profile, err := database.GetRepoProfile(issueA.Repo); err != nil || profile == nil || profile.TotalPRsSubmitted != 2 || profile.TotalRejected != 1 || profile.TotalMerged != 1 {
		t.Errorf("profile = %+v, %v, want one rejected and one merged outcome", profile, err)
	}
}

func TestProcessPRMergedIgnoresUnusedIssueCommentFailures(t *testing.T) {
	for _, failure := range []string{"command", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			logPath := installFakeGH(t, false)
			t.Setenv("GH_TEST_PR_INFO", `{"state":"MERGED"}`)
			if failure == "command" {
				t.Setenv("GH_TEST_FAIL_ISSUE_COMMENTS", "1")
			} else {
				t.Setenv("GH_TEST_ISSUE_COMMENTS", `[invalid`)
			}
			t.Setenv("GH_TEST_REVIEW_COMMENTS", `[{"user":{"login":"maintainer"},"body":"Avoid this incorrect logic.","path":"main.go","diff_hunk":"@@"}]`)
			database := newFeedbackTestDB(t)
			issue, pr := createFeedbackTestPR(t, database)
			p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)
			if err := p.ProcessPR(context.Background(), pr); err != nil {
				t.Fatalf("merged PR with unused issue comment %s failure: %v", failure, err)
			}
			status, _, _ := getFeedbackPRState(t, database, pr.ID)
			if status != string(models.PRStatusMerged) {
				t.Errorf("status = %q, want merged", status)
			}
			if rule := loadRule(t, p.ruleLoader, "close-learning"); rule.QValue != 0.55 || rule.RetrievalCount != 1 || rule.SuccessCount != 1 {
				t.Errorf("rule = %+v, want merged reward once", rule)
			}
			if count, err := database.CountLessonsByPR(pr.ID); err != nil || count != 1 {
				t.Errorf("inline lessons = %d, %v, want one", count, err)
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Errorf("workspace retained after merged learning: %v", err)
			}
			data, err := os.ReadFile(logPath)
			if err != nil || strings.Contains(string(data), "/issues/42/comments") || !strings.Contains(string(data), "/pulls/42/comments") {
				t.Errorf("GitHub calls = %q, %v, want review comments only", data, err)
			}
		})
	}
}

func TestProcessPRRewardRetryWithChangedOutcome(t *testing.T) {
	installFakeGH(t, false)
	t.Setenv("GH_TEST_PR_INFO", `{"state":"CLOSED"}`)
	t.Setenv("GH_TEST_ISSUE_COMMENTS", `[]`)
	database := newFeedbackTestDB(t)
	issue, pr := createFeedbackTestPR(t, database)
	p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)
	if _, err := database.Exec("CREATE TRIGGER fail_status BEFORE UPDATE OF status ON pull_requests BEGIN SELECT RAISE(ABORT, 'terminal write unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessPR(context.Background(), pr); err == nil || !strings.Contains(err.Error(), "terminal write unavailable") {
		t.Fatalf("first error = %v, want final SQL failure after unknown reward", err)
	}
	if rule := loadRule(t, p.ruleLoader, "close-learning"); rule.QValue != 0.47 || rule.RetrievalCount != 1 {
		t.Fatalf("first rule = %+v, want unknown reward once", rule)
	}
	// A maintainer supplies new feedback before the retry. Restart from disk.
	t.Setenv("GH_TEST_ISSUE_COMMENTS", `[{"user":{"login":"maintainer"},"body":"The logic is incorrect."}]`)
	p.ruleLoader = rules.NewRuleLoader(p.ruleLoader.RulesDir())
	if err := p.ruleLoader.Load(); err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessPR(context.Background(), pr); err == nil || !strings.Contains(err.Error(), "terminal write unavailable") {
		t.Fatalf("changed outcome error = %v, want final SQL failure after new reward", err)
	}
	if rule := loadRule(t, p.ruleLoader, "close-learning"); rule.QValue < 0.4229 || rule.QValue > 0.4231 || rule.RetrievalCount != 2 || rule.SuccessCount != 0 {
		t.Errorf("changed rule = %+v, want Q=0.423, retrieval=2, success=0", rule)
	}
	events, err := database.GetEventsByIssue(issue.ID)
	if err != nil || len(events) != 1 || events[0].OutcomeLabel != OutcomeRejectedQuality {
		t.Errorf("events = %+v, %v, want changed quality label", events, err)
	}
	trajectories, err := database.GetRecentTrajectories(1)
	if err != nil || len(trajectories) != 1 || trajectories[0].OutcomeLabel != OutcomeRejectedQuality || trajectories[0].Success {
		t.Errorf("trajectories = %+v, %v, want changed quality label and success=false", trajectories, err)
	}
	if open, err := database.GetOpenPRs(); err != nil || len(open) != 1 {
		t.Errorf("open PRs = %v, %v, want retryable PR", open, err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Errorf("workspace removed before completion: %v", err)
	}
	// Returning to an already-rewarded outcome must not replay its old reward.
	t.Setenv("GH_TEST_ISSUE_COMMENTS", `[]`)
	if err := p.ProcessPR(context.Background(), pr); err == nil || !strings.Contains(err.Error(), "terminal write unavailable") {
		t.Fatalf("old outcome retry error = %v, want final SQL failure", err)
	}
	if rule := loadRule(t, p.ruleLoader, "close-learning"); rule.QValue < 0.4229 || rule.QValue > 0.4231 || rule.RetrievalCount != 2 {
		t.Errorf("old outcome replay changed rewards: %+v", rule)
	}
	t.Setenv("GH_TEST_ISSUE_COMMENTS", `[{"user":{"login":"maintainer"},"body":"The logic is incorrect."}]`)
	if _, err := database.Exec("DROP TRIGGER fail_status"); err != nil {
		t.Fatal(err)
	}
	p.ruleLoader = rules.NewRuleLoader(p.ruleLoader.RulesDir())
	if err := p.ruleLoader.Load(); err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessPR(context.Background(), pr); err != nil {
		t.Fatalf("same outcome retry: %v", err)
	}
	if rule := loadRule(t, p.ruleLoader, "close-learning"); rule.QValue < 0.4229 || rule.QValue > 0.4231 || rule.RetrievalCount != 2 || rule.SuccessCount != 0 {
		t.Errorf("repeated rule = %+v, want changed reward retained exactly once", rule)
	}
	if profile, err := database.GetRepoProfile(issue.Repo); err != nil || profile == nil || profile.TotalPRsSubmitted != 1 || profile.TotalRejected != 1 {
		t.Errorf("profile = %+v, %v, want one rejected PR", profile, err)
	}
	if open, err := database.GetOpenPRs(); err != nil || len(open) != 0 {
		t.Errorf("open PRs = %v, %v, want completed transition", open, err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Errorf("workspace retained after completion: %v", err)
	}
}

func TestProcessPRRetryCapturesNewLessonsAtomically(t *testing.T) {
	for _, state := range []string{"CLOSED", "MERGED"} {
		t.Run(state, func(t *testing.T) {
			installFakeGH(t, false)
			t.Setenv("GH_TEST_PR_INFO", `{"state":"`+state+`"}`)
			t.Setenv("GH_TEST_ISSUE_COMMENTS", `[]`)
			original := `{"user":{"login":"maintainer"},"body":"Please add a test for this behavior.","path":"main.go"}`
			t.Setenv("GH_TEST_REVIEW_COMMENTS", `[`+original+`]`)
			database := newFeedbackTestDB(t)
			issue, pr := createFeedbackTestPR(t, database)
			p, workspace := newResponderLearningTestPipeline(t, database, issue, pr)
			if _, err := database.Exec("CREATE TRIGGER fail_status BEFORE UPDATE OF status ON pull_requests BEGIN SELECT RAISE(ABORT, 'terminal write unavailable'); END"); err != nil {
				t.Fatal(err)
			}
			if err := p.ProcessPR(context.Background(), pr); err == nil || !strings.Contains(err.Error(), "terminal write unavailable") {
				t.Fatalf("first processing error = %v, want final SQL failure", err)
			}
			if count, err := database.CountLessonsByPR(pr.ID); err != nil || count != 1 {
				t.Fatalf("initial lessons = %d, %v, want one saved before SQL failure", count, err)
			}
			// The same source text on a new file is a distinct lesson, and feedback
			// arriving on an issue or inline thread must survive the next retry.
			newPath := `{"user":{"login":"maintainer"},"body":"Please add a test for this behavior.","path":"other.go"}`
			newFeedback := `{"user":{"login":"new-reviewer"},"body":"Please correct this incorrect logic.","path":"new.go"}`
			if state == "CLOSED" {
				t.Setenv("GH_TEST_REVIEW_COMMENTS", `[`+original+`,`+newPath+`]`)
				t.Setenv("GH_TEST_ISSUE_COMMENTS", `[`+newFeedback+`]`)
			} else {
				t.Setenv("GH_TEST_REVIEW_COMMENTS", `[`+original+`,`+newPath+`,`+newFeedback+`]`)
			}
			if _, err := database.Exec("CREATE TRIGGER fail_lesson BEFORE INSERT ON review_lessons WHEN NEW.reviewer = 'new-reviewer' BEGIN SELECT RAISE(ABORT, 'new lesson unavailable'); END"); err != nil {
				t.Fatal(err)
			}
			p.ruleLoader = rules.NewRuleLoader(p.ruleLoader.RulesDir())
			if err := p.ruleLoader.Load(); err != nil {
				t.Fatal(err)
			}
			if err := p.ProcessPR(context.Background(), pr); err == nil || !strings.Contains(err.Error(), "new lesson unavailable") {
				t.Errorf("new feedback error = %v, want lesson write failure before status", err)
			}
			if count, err := database.CountLessonsByPR(pr.ID); err != nil || count != 1 {
				t.Errorf("failed batch lessons = %d, %v, want original retained and new batch rolled back", count, err)
			}
			if _, err := database.Exec("DROP TRIGGER fail_lesson"); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := p.ProcessPR(context.Background(), pr); err == nil || !strings.Contains(err.Error(), "terminal write unavailable") {
					t.Errorf("retry %d error = %v, want SQL failure after lessons saved", attempt, err)
				}
				if count, err := database.CountLessonsByPR(pr.ID); err != nil || count != 3 {
					t.Errorf("retry %d lessons = %d, %v, want all three exactly once", attempt, count, err)
				}
			}
			if open, err := database.GetOpenPRs(); err != nil || len(open) != 1 {
				t.Errorf("open PRs = %v, %v, want retryable PR", open, err)
			}
			if _, err := os.Stat(workspace); err != nil {
				t.Errorf("workspace removed before completion: %v", err)
			}
			if _, err := database.Exec("DROP TRIGGER fail_status"); err != nil {
				t.Fatal(err)
			}
			if err := p.ProcessPR(context.Background(), pr); err != nil {
				t.Fatalf("final retry: %v", err)
			}
			if count, err := database.CountLessonsByPR(pr.ID); err != nil || count != 3 {
				t.Errorf("final lessons = %d, %v, want all three exactly once", count, err)
			}
			if open, err := database.GetOpenPRs(); err != nil || len(open) != 0 {
				t.Errorf("open PRs = %v, %v, want completed transition", open, err)
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Errorf("workspace retained after completion: %v", err)
			}
		})
	}
}
