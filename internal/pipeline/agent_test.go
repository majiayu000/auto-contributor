package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/internal/prompt"
	"github.com/majiayu000/auto-contributor/internal/rules"
	"github.com/majiayu000/auto-contributor/internal/runtime"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

var _ runtime.Runtime = (*stubRuntime)(nil)

type stubRuntime struct {
	outputs []stubOutput
	index   int
}

type stubOutput struct {
	output string
	err    error
}

func (r *stubRuntime) Name() string {
	return "stub"
}

func (r *stubRuntime) Execute(ctx context.Context, workDir string, prompt string) (string, error) {
	if r.index >= len(r.outputs) {
		return "", errors.New("unexpected runtime call")
	}
	result := r.outputs[r.index]
	r.index++
	return result.output, result.err
}

func (r *stubRuntime) ExecuteStdin(ctx context.Context, prompt string) (string, error) {
	return "", errors.New("not implemented")
}

func writePromptTemplate(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0644); err != nil {
		t.Fatalf("write %s prompt: %v", name, err)
	}
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

func TestEngineerReviewLoop_ReviewerRuntimeFailureBlocksAndFailsIssue(t *testing.T) {
	rt := &stubRuntime{outputs: []stubOutput{
		{output: "FIX_COMPLETE"},
		{err: errors.New("reviewer runtime exploded")},
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
