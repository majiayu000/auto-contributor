package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattn/go-sqlite3"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/prompt"
	"github.com/majiayu000/auto-contributor/internal/rules"
	"github.com/majiayu000/auto-contributor/internal/runtime"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

var _ runtime.Runtime = (*stubRuntime)(nil)

type stubRuntime struct {
	outputs  []stubOutput
	index    int
	policies []runtime.ExecutionPolicy
	prompts  []string
	workDirs []string
}

type stubOutput struct {
	output string
	err    error
}

func (r *stubRuntime) Name() string {
	return "stub"
}

func (r *stubRuntime) Execute(ctx context.Context, workDir string, prompt string, policy runtime.ExecutionPolicy) (string, error) {
	if r.index >= len(r.outputs) {
		return "", errors.New("unexpected runtime call")
	}
	r.policies = append(r.policies, policy)
	r.prompts = append(r.prompts, prompt)
	r.workDirs = append(r.workDirs, workDir)
	result := r.outputs[r.index]
	r.index++
	return result.output, result.err
}

func (r *stubRuntime) ExecuteStdin(ctx context.Context, prompt string, policy runtime.ExecutionPolicy) (string, error) {
	r.policies = append(r.policies, policy)
	return "", errors.New("not implemented")
}

func writePromptTemplate(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0644); err != nil {
		t.Fatalf("write %s prompt: %v", name, err)
	}
}

func installFailingGH(t *testing.T, stderr string) {
	t.Helper()

	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "gh")
	script := "#!/bin/sh\nprintf '%s\\n' " + shellQuote(stderr) + " >&2\nexit 1\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh script: %v", err)
	}
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func newLoopTestPipeline(t *testing.T, rt runtime.Runtime) (*Pipeline, *db.DB) {
	t.Helper()

	database, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	deferCleanup := func() {
		_ = database.Close()
	}
	t.Cleanup(deferCleanup)

	promptsDir := t.TempDir()
	writePromptTemplate(t, promptsDir, "engineer", `engineer {{.IssueTitle}}`)
	writePromptTemplate(t, promptsDir, "reviewer", `reviewer {{.IssueTitle}}`)

	ps := prompt.NewStore(promptsDir)
	if err := ps.Load(); err != nil {
		t.Fatalf("load prompts: %v", err)
	}

	rl := rules.NewRuleLoader(t.TempDir())
	if err := rl.Load(); err != nil {
		t.Fatalf("load rules: %v", err)
	}

	return &Pipeline{
		db:         database,
		prompts:    ps,
		runner:     NewAgentRunner(ps, rt, 0),
		ruleLoader: rl,
		maxReview:  2,
	}, database
}

func TestProcessIssueReturnsPRCountErrorsBeforeScout(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failQuery int
		wantError string
	}{
		{"merged", 1, "count merged PRs"},
		{"open", 2, "count open PRs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, database := newLoopTestPipeline(t, &stubRuntime{})
			p.cfg = &config.Config{MaxPRsPerRepo: 1}
			p.gh = ghclient.New(p.cfg)
			issue := &models.Issue{Repo: "owner/repo", IssueNumber: 103, Title: "PR cap", Status: models.IssueStatusDiscovered}
			if err := database.CreateIssue(issue); err != nil {
				t.Fatalf("create issue: %v", err)
			}
			ghLog := installPRCapTestGH(t)

			// Deny only the selected real count query on the single SQLite connection.
			database.SetMaxOpenConns(1)
			conn, err := database.Conn(context.Background())
			if err != nil {
				t.Fatalf("get SQLite connection: %v", err)
			}
			queryCount := 0
			if err := conn.Raw(func(raw any) error {
				raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(action int, _, _, _ string) int {
					if action == sqlite3.SQLITE_SELECT {
						queryCount++
						if queryCount == tc.failQuery {
							return sqlite3.SQLITE_DENY
						}
					}
					return sqlite3.SQLITE_OK
				})
				return nil
			}); err != nil {
				t.Fatalf("install SQLite authorizer: %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("release SQLite connection: %v", err)
			}

			err = p.ProcessIssue(context.Background(), issue)
			conn, connErr := database.Conn(context.Background())
			if connErr != nil {
				t.Fatalf("get SQLite connection: %v", connErr)
			}
			if err := conn.Raw(func(raw any) error {
				raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(nil)
				return nil
			}); err != nil {
				t.Fatalf("reset SQLite authorizer: %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("release SQLite connection: %v", err)
			}
			var sqliteErr sqlite3.Error
			if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrAuth {
				t.Errorf("ProcessIssue error = %v, want original SQLite authorization error", err)
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("ProcessIssue error = %v, want %q context", err, tc.wantError)
			}
			if queryCount != tc.failQuery {
				t.Errorf("count queries = %d, want %d", queryCount, tc.failQuery)
			}
			if _, err := os.Stat(ghLog); !os.IsNotExist(err) {
				t.Errorf("GitHub called after count failure: log stat error = %v", err)
			}
			stored, err := database.GetIssueByID(issue.ID)
			if err != nil {
				t.Fatalf("get issue: %v", err)
			}
			if stored.Status != models.IssueStatusDiscovered || stored.ErrorMessage != "" {
				t.Errorf("issue status = %q, error = %q, want unchanged discovered issue", stored.Status, stored.ErrorMessage)
			}
			events, err := database.GetEventsByIssue(issue.ID)
			if err != nil {
				t.Fatalf("get events: %v", err)
			}
			if len(events) != 0 {
				t.Errorf("pipeline events = %d, want 0", len(events))
			}
		})
	}
}

func TestProcessIssuePRCapUsesSuccessfulCounts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		maxPR     int
		merged    int
		open      int
		wantScout bool
	}{
		{"default limit", 0, 0, 2, false},
		{"configured limit", 3, 0, 3, false},
		{"merged allowance", 1, 1, 1, true},
		{"merged limit reached", 1, 1, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, database := newLoopTestPipeline(t, &stubRuntime{})
			p.cfg = &config.Config{MaxPRsPerRepo: tc.maxPR}
			p.gh = ghclient.New(p.cfg)
			issue := &models.Issue{Repo: "owner/repo", IssueNumber: 103, Title: "PR cap", Status: models.IssueStatusDiscovered}
			if err := database.CreateIssue(issue); err != nil {
				t.Fatalf("create issue: %v", err)
			}
			for _, count := range []struct {
				status models.PRStatus
				n      int
			}{{models.PRStatusMerged, tc.merged}, {models.PRStatusOpen, tc.open}} {
				for i := 0; i < count.n; i++ {
					pr := &models.PullRequest{IssueID: issue.ID, PRURL: "https://github.com/owner/repo/pull/1", BranchName: "fix/cap", Status: count.status}
					if err := database.CreatePullRequest(pr); err != nil {
						t.Fatalf("create PR: %v", err)
					}
				}
			}
			ghLog := installPRCapTestGH(t)
			err := p.ProcessIssue(context.Background(), issue)
			if tc.wantScout {
				if err == nil || !strings.Contains(err.Error(), "PR cap scout reached") {
					t.Fatalf("ProcessIssue error = %v, want Scout precollection reached", err)
				}
				if _, err := os.Stat(ghLog); err != nil {
					t.Fatalf("Scout did not call GitHub: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("ProcessIssue error = %v, want nil on reached cap", err)
				}
				if _, err := os.Stat(ghLog); !os.IsNotExist(err) {
					t.Fatalf("GitHub called after reached cap: %v", err)
				}
				stored, err := database.GetIssueByID(issue.ID)
				if err != nil {
					t.Fatalf("get issue: %v", err)
				}
				if stored.Status != models.IssueStatusAbandoned || !strings.Contains(stored.ErrorMessage, "rate limit:") {
					t.Fatalf("issue status = %q, error = %q, want rate-limit abandonment", stored.Status, stored.ErrorMessage)
				}
			}
		})
	}
}

func installPRCapTestGH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gh.log")
	script := "#!/bin/sh\necho called >> " + shellQuote(logPath) + "\necho 'PR cap scout reached' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func assertReviewerFailureEvent(t *testing.T, database *db.DB, issueID int64, wantErr string) {
	t.Helper()

	events, err := database.GetEventsByIssue(issueID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	for _, event := range events {
		if event.Stage != "reviewer" {
			continue
		}
		if event.Round != 1 {
			t.Fatalf("got reviewer round=%d, want 1", event.Round)
		}
		if event.Success {
			t.Fatal("got reviewer success=true, want false")
		}
		if event.Verdict != "error" {
			t.Fatalf("got reviewer verdict=%q, want error", event.Verdict)
		}
		if !strings.Contains(event.ErrorMessage, wantErr) {
			t.Fatalf("got reviewer error_message=%q, want %q", event.ErrorMessage, wantErr)
		}
		return
	}
	t.Fatal("expected reviewer failure event, found none")
}

func TestExtractJSON_PlainJSON(t *testing.T) {
	var dest map[string]any
	err := extractJSON(`{"verdict":"PROCEED","score":0.9}`, &dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["verdict"] != "PROCEED" {
		t.Errorf("got verdict=%v, want PROCEED", dest["verdict"])
	}
}

func TestExtractJSON_MarkdownFence(t *testing.T) {
	input := "Here is my analysis:\n\n```json\n{\"verdict\":\"PROCEED\",\"score\":0.9}\n```\n"
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["verdict"] != "PROCEED" {
		t.Errorf("got verdict=%v, want PROCEED", dest["verdict"])
	}
}

func TestExtractJSON_MarkdownFenceUppercase(t *testing.T) {
	input := "```JSON\n{\"ok\":true}\n```"
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["ok"] != true {
		t.Errorf("got ok=%v, want true", dest["ok"])
	}
}

func TestExtractJSON_ProseThenJSON(t *testing.T) {
	// Prose with a brace-like token before the real JSON
	input := "Use map[string]int{} for counting.\n\n{\"verdict\":\"SKIP\"}"
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["verdict"] != "SKIP" {
		t.Errorf("got verdict=%v, want SKIP", dest["verdict"])
	}
}

func TestExtractJSON_LastObjectWins(t *testing.T) {
	// Two JSON objects; the last is the structured output
	input := `Some context {"noise":1} and then the real output {"verdict":"PROCEED","score":0.8}`
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["verdict"] != "PROCEED" {
		t.Errorf("got verdict=%v, want PROCEED", dest["verdict"])
	}
}

func TestExtractJSON_NoJSON(t *testing.T) {
	err := extractJSON("no json here at all", &map[string]any{})
	if err == nil {
		t.Fatal("expected error for input with no JSON")
	}
}

func TestExtractJSON_BracesInStrings(t *testing.T) {
	// Braces inside string values should not confuse the depth counter
	input := `{"key":"value with } brace","ok":true}`
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["ok"] != true {
		t.Errorf("got ok=%v, want true", dest["ok"])
	}
}

func TestExtractObjectAt_Incomplete(t *testing.T) {
	s := extractObjectAt(`{"unclosed":`, 0)
	if s != "" {
		t.Errorf("expected empty string for incomplete object, got %q", s)
	}
}

func TestExtractFromCodeFence_NoFence(t *testing.T) {
	s := extractFromCodeFence("no fences here")
	if s != "" {
		t.Errorf("expected empty string, got %q", s)
	}
}

func TestEngineerReviewLoop_ReviewerParseFailureBlocksAndFailsIssue(t *testing.T) {
	rt := &stubRuntime{outputs: []stubOutput{
		{output: "FIX_COMPLETE"},
		{output: "not json at all"},
		{output: "still not json"},
		{output: `{"verdict":"approve","confidence":1,"issues_found":[],"summary":"must not be reached"}`},
	}}
	p, database := newLoopTestPipeline(t, rt)

	issue := &models.Issue{
		Repo:            "owner/repo",
		IssueNumber:     45,
		Title:           "reviewer parse failure should block",
		Status:          models.IssueStatusDiscovered,
		DifficultyScore: 0.1,
	}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	analyst := &AnalystResult{
		CanFix:       true,
		BaseBranch:   "main",
		CommitFormat: "test",
		BranchName:   "feat/test-45",
		FixPlan: FixPlan{
			Description:  "minimal fix",
			TestStrategy: "go test ./...",
		},
	}

	rounds, _, err := p.engineerReviewLoopWithStats(context.Background(), issue, t.TempDir(), analyst)
	if err == nil {
		t.Fatal("expected reviewer failure error, got nil")
	}
	if rounds != 1 {
		t.Fatalf("got rounds=%d, want 1", rounds)
	}
	if !strings.Contains(err.Error(), "parse reviewer JSON output") {
		t.Fatalf("got error %q, want reviewer parse failure", err)
	}
	if rt.index != 3 {
		t.Fatalf("runtime calls = %d, want 3 (engineer, reviewer, json recovery only)", rt.index)
	}

	stored, err := database.GetIssueByID(issue.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if stored.Status != models.IssueStatusFailed {
		t.Fatalf("got status=%q, want %q", stored.Status, models.IssueStatusFailed)
	}
	if !strings.Contains(stored.ErrorMessage, "reviewer_failed") {
		t.Fatalf("got error_message=%q, want reviewer_failed prefix", stored.ErrorMessage)
	}

	assertReviewerFailureEvent(t, database, issue.ID, "parse reviewer JSON output")
}

func TestRunScoutUsesSemanticRetrieverRuleSelection(t *testing.T) {
	rt := &stubRuntime{outputs: []stubOutput{
		{output: `{"verdict":"PROCEED","reason":"","difficulty":1,"has_competing_pr":false,"suggested_approach":"minimal fix"}`},
	}}
	database, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})

	promptsDir := t.TempDir()
	writePromptTemplate(t, promptsDir, "scout", `rules={{.Rules}}`)

	ps := prompt.NewStore(promptsDir)
	if err := ps.Load(); err != nil {
		t.Fatalf("load prompts: %v", err)
	}

	rulesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rulesDir, "scout"), 0755); err != nil {
		t.Fatalf("mkdir rules dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "scout", "full-load.yaml"), []byte(
		"id: full-load\nstage: scout\nconfidence: 0.9\nbody: full load fallback text\n",
	), 0644); err != nil {
		t.Fatalf("write rule: %v", err)
	}

	rl := rules.NewRuleLoader(rulesDir)
	if err := rl.Load(); err != nil {
		t.Fatalf("load rules: %v", err)
	}

	p := &Pipeline{
		cfg:           &config.Config{WorkspaceDir: t.TempDir()},
		db:            database,
		prompts:       ps,
		runner:        NewAgentRunner(ps, rt, 0),
		ruleLoader:    rl,
		ruleRetriever: stubStageRuleRetriever{ids: []string{"scout/semantic-one", "global/semantic-two"}, prompt: "semantic retrieval text"},
	}

	issue := &models.Issue{
		Repo:        "owner/repo",
		IssueNumber: 7,
		Title:       "panic in parser",
		Body:        "repro details",
		Labels:      "[\"bug\"]",
	}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	if _, err := p.runScout(context.Background(), issue); err != nil {
		t.Fatalf("runScout() failed: %v", err)
	}

	if len(rt.prompts) != 1 {
		t.Fatalf("runtime prompt count = %d, want 1", len(rt.prompts))
	}
	if !strings.Contains(rt.prompts[0], "semantic retrieval text") {
		t.Fatalf("runtime prompt did not include semantic retrieval text:\n%s", rt.prompts[0])
	}
	if strings.Contains(rt.prompts[0], "full load fallback text") {
		t.Fatalf("runtime prompt used full-load rules instead of semantic selection:\n%s", rt.prompts[0])
	}

	events, err := database.GetEventsByIssue(issue.ID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}

	var recorded []string
	if err := json.Unmarshal([]byte(events[0].ExperiencesUsed), &recorded); err != nil {
		t.Fatalf("unmarshal experiences_used: %v", err)
	}
	want := []string{"scout/semantic-one", "global/semantic-two"}
	if strings.Join(recorded, ",") != strings.Join(want, ",") {
		t.Fatalf("recorded rule IDs = %v, want %v", recorded, want)
	}
}

func TestRunScoutFailsBeforeRuntimeWhenPrecollectionFails(t *testing.T) {
	installFailingGH(t, "rate limit exceeded")

	rt := &stubRuntime{outputs: []stubOutput{
		{output: `{"verdict":"PROCEED","reason":"","difficulty":1,"has_competing_pr":false,"suggested_approach":"minimal fix"}`},
	}}
	database, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})

	promptsDir := t.TempDir()
	writePromptTemplate(t, promptsDir, "scout", `scout {{.ScoutData}}`)

	ps := prompt.NewStore(promptsDir)
	if err := ps.Load(); err != nil {
		t.Fatalf("load prompts: %v", err)
	}

	rl := rules.NewRuleLoader(t.TempDir())
	if err := rl.Load(); err != nil {
		t.Fatalf("load rules: %v", err)
	}

	p := &Pipeline{
		cfg:        &config.Config{WorkspaceDir: t.TempDir()},
		db:         database,
		gh:         ghclient.New(&config.Config{}),
		prompts:    ps,
		runner:     NewAgentRunner(ps, rt, 0),
		ruleLoader: rl,
	}

	issue := &models.Issue{
		Repo:        "owner/repo",
		IssueNumber: 7,
		Title:       "panic in parser",
	}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	_, err = p.runScout(context.Background(), issue)
	if err == nil {
		t.Fatal("runScout() error = nil, want scout data collection failure")
	}
	if !strings.Contains(err.Error(), "collect scout data") || !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("runScout() error = %q, want precollection rate-limit failure", err.Error())
	}
	if rt.index != 0 {
		t.Fatalf("runtime calls = %d, want 0", rt.index)
	}

	events, err := database.GetEventsByIssue(issue.ID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].Stage != "scout" || events[0].Success || events[0].Verdict != "error" {
		t.Fatalf("event = %+v, want failed scout error", events[0])
	}
	if !strings.Contains(events[0].ErrorMessage, "collect scout data") {
		t.Fatalf("event error = %q, want collection failure", events[0].ErrorMessage)
	}
}

type stubStageRuleRetriever struct {
	ids    []string
	prompt string
	err    error
}

func (s stubStageRuleRetriever) Retrieve(stage string, issue *models.Issue) ([]string, string, error) {
	return s.ids, s.prompt, s.err
}

func (s stubStageRuleRetriever) Sync() error {
	return nil
}

func TestEngineerReviewLoop_ReviewerRuntimeFailureBlocksAndFailsIssue(t *testing.T) {
	rt := &stubRuntime{outputs: []stubOutput{
		{output: "FIX_COMPLETE"},
		{err: errors.New("reviewer runtime exploded")},
		{output: `{"verdict":"approve","confidence":1,"issues_found":[],"summary":"must not be reached"}`},
	}}
	p, database := newLoopTestPipeline(t, rt)

	issue := &models.Issue{
		Repo:            "owner/repo",
		IssueNumber:     45,
		Title:           "reviewer runtime failure should block",
		Status:          models.IssueStatusDiscovered,
		DifficultyScore: 0.1,
	}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	analyst := &AnalystResult{
		CanFix:       true,
		BaseBranch:   "main",
		CommitFormat: "test",
		BranchName:   "feat/test-45",
		FixPlan: FixPlan{
			Description:  "minimal fix",
			TestStrategy: "go test ./...",
		},
	}

	rounds, _, err := p.engineerReviewLoopWithStats(context.Background(), issue, t.TempDir(), analyst)
	if err == nil {
		t.Fatal("expected reviewer runtime failure error, got nil")
	}
	if rounds != 1 {
		t.Fatalf("got rounds=%d, want 1", rounds)
	}
	if !strings.Contains(err.Error(), "reviewer runtime exploded") {
		t.Fatalf("got error %q, want reviewer runtime failure", err)
	}
	if rt.index != 2 {
		t.Fatalf("runtime calls = %d, want 2 (engineer and failing reviewer only)", rt.index)
	}

	stored, err := database.GetIssueByID(issue.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if stored.Status != models.IssueStatusFailed {
		t.Fatalf("got status=%q, want %q", stored.Status, models.IssueStatusFailed)
	}
	if !strings.Contains(stored.ErrorMessage, "reviewer_failed") {
		t.Fatalf("got error_message=%q, want reviewer_failed prefix", stored.ErrorMessage)
	}

	assertReviewerFailureEvent(t, database, issue.ID, "reviewer runtime exploded")
}
