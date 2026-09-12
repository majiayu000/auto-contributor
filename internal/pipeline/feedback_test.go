package pipeline

import (
	"context"
	"database/sql"
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
		db: database,
		gh: ghclient.New(&config.Config{}),
	}

	err := p.finalizeResponderAction(
		context.Background(),
		pr,
		"owner/repo",
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

func TestHandleOpen_ResponderParseFailureDoesNotStampFeedbackCheck(t *testing.T) {
	installFakeGHForFeedbackHandleOpen(t)

	database := newFeedbackTestDB(t)
	issue, pr := createFeedbackTestPR(t, database)

	workspaceRoot := t.TempDir()
	cfg := &config.Config{
		WorkspaceDir:   workspaceRoot,
		GitHubUsername: "tester",
	}

	// Pre-seed the PR workspace so preparePRWorkspace does not need to clone.
	remoteDir := createBareRepo(t)
	seedDir := filepath.Join(t.TempDir(), "seed")
	runGitCommand(t, "", "init", "--initial-branch=main", seedDir)
	runGitCommand(t, seedDir, "config", "user.name", "Test User")
	runGitCommand(t, seedDir, "config", "user.email", "test@example.com")
	writeFile(t, filepath.Join(seedDir, "tracked.txt"), "main\n")
	runGitCommand(t, seedDir, "add", "tracked.txt")
	runGitCommand(t, seedDir, "commit", "-m", "main")
	runGitCommand(t, seedDir, "remote", "add", "origin", remoteDir)
	runGitCommand(t, seedDir, "push", "origin", "main")
	runGitCommand(t, seedDir, "checkout", "-b", pr.BranchName)
	writeFile(t, filepath.Join(seedDir, "tracked.txt"), "branch\n")
	runGitCommand(t, seedDir, "commit", "-am", "branch")
	runGitCommand(t, seedDir, "push", "origin", pr.BranchName)

	workspacePath := filepath.Join(workspaceRoot, "owner-repo-70")
	runGitCommand(t, "", "clone", remoteDir, workspacePath)
	runGitCommand(t, workspacePath, "remote", "add", "fork", remoteDir)

	promptsDir := t.TempDir()
	writePromptTemplate(t, promptsDir, "responder", `responder {{.PRNumber}}`)
	ps := prompt.NewStore(promptsDir)
	if err := ps.Load(); err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	rl := rules.NewRuleLoader(t.TempDir())
	if err := rl.Load(); err != nil {
		t.Fatalf("load rules: %v", err)
	}

	rt := &stubRuntime{outputs: []stubOutput{
		{output: "not json at all"},
		{output: "still not json"},
	}}
	p := &Pipeline{
		cfg:        cfg,
		db:         database,
		gh:         ghclient.New(cfg),
		prompts:    ps,
		runner:     NewAgentRunner(ps, rt, 0),
		ruleLoader: rl,
	}

	prInfo := &ghclient.PRInfo{
		State:       "OPEN",
		IsDraft:     false,
		HeadRefName: pr.BranchName,
		Reviews: []ghclient.PRReview{
			{
				Author:      "maintainer",
				State:       "CHANGES_REQUESTED",
				Body:        "Please fix the silent skip on responder parse failure.",
				SubmittedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}

	err := p.handleOpen(context.Background(), pr, issue.Repo, prInfo)
	if err == nil {
		t.Fatal("handleOpen error = nil, want responder parse failure")
	}
	if !strings.Contains(err.Error(), "responder parse error") {
		t.Fatalf("handleOpen error = %q, want responder parse error", err)
	}

	status, round, checked := getFeedbackPRState(t, database, pr.ID)
	if status != string(models.PRStatusOpen) {
		t.Fatalf("status = %q, want %q", status, models.PRStatusOpen)
	}
	if round != 0 {
		t.Fatalf("feedback_round = %d, want 0 after responder parse failure", round)
	}
	if checked.Valid {
		t.Fatalf("last_feedback_check_at = %q, want NULL after responder parse failure", checked.String)
	}

	events, err := database.GetEventsByIssue(issue.ID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Stage != "responder" {
			continue
		}
		found = true
		if event.Success {
			t.Fatal("responder event success=true, want false")
		}
		if event.ErrorMessage == "" {
			t.Fatal("responder event missing error_message")
		}
	}
	if !found {
		t.Fatal("expected responder failure event, found none")
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
exit 0
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh script: %v", err)
	}

	t.Setenv("GH_TEST_LOG", logPath)
	if failClose {
		t.Setenv("GH_TEST_FAIL_CLOSE", "1")
	} else {
		t.Setenv("GH_TEST_FAIL_CLOSE", "0")
	}
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func installFakeGHForFeedbackHandleOpen(t *testing.T) {
	t.Helper()

	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "gh")
	script := `#!/bin/sh
case "$*" in
  api\ repos/*/pulls/*/comments|api\ repos/*/issues/*/comments)
    printf '[]\n'
    exit 0
    ;;
  *)
    printf 'unexpected gh args: %s\n' "$*" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh script: %v", err)
	}
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
