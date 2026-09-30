package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	outputs   []stubOutput
	index     int
	policies  []runtime.ExecutionPolicy
	prompts   []string
	workDirs  []string
	onExecute func(workDir string)
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
	if r.onExecute != nil {
		r.onExecute(workDir)
	}
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

func TestRunScoutUsesIsolatedWorkDir(t *testing.T) {
	runtimeErr := errors.New("scout runtime failed")
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "runtime failure", err: runtimeErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspaceDir := t.TempDir()
			rt := &stubRuntime{
				outputs: []stubOutput{{output: `{"verdict":"PROCEED"}`, err: tc.err}},
				onExecute: func(workDir string) {
					if workDir == "" || workDir == workspaceDir || strings.HasPrefix(workDir, workspaceDir+string(os.PathSeparator)) {
						t.Fatalf("scout workdir %q must be outside WorkspaceDir %q", workDir, workspaceDir)
					}
					entries, err := os.ReadDir(workDir)
					if err != nil {
						t.Fatalf("read scout workdir during execution: %v", err)
					}
					if len(entries) != 0 {
						t.Fatalf("scout workdir contains %d entries, want empty", len(entries))
					}
				},
			}
			p, database := newLoopTestPipeline(t, rt)
			p.cfg = &config.Config{WorkspaceDir: workspaceDir}
			promptsDir := t.TempDir()
			writePromptTemplate(t, promptsDir, "scout", `scout {{.IssueData}}`)
			p.prompts = prompt.NewStore(promptsDir)
			if err := p.prompts.Load(); err != nil {
				t.Fatalf("load scout prompt: %v", err)
			}
			p.runner = NewAgentRunner(p.prompts, rt, 0)
			issue := &models.Issue{Repo: "owner/repo", IssueNumber: 98, Title: "untrusted issue"}
			if err := database.CreateIssue(issue); err != nil {
				t.Fatalf("create issue: %v", err)
			}

			result, err := p.runScout(context.Background(), issue)
			if !errors.Is(err, tc.err) {
				t.Fatalf("runScout error = %v, want %v", err, tc.err)
			}
			if tc.err == nil && (result == nil || result.Verdict != "PROCEED") {
				t.Fatalf("runScout result = %+v, want PROCEED", result)
			}
			if tc.err != nil && result != nil {
				t.Fatalf("runScout result = %+v, want nil on failure", result)
			}
			if len(rt.workDirs) != 1 || len(rt.policies) != 1 || rt.policies[0] != runtime.ExecutionPolicyUntrusted {
				t.Fatalf("runtime calls = %v, policies = %v, want one untrusted call", rt.workDirs, rt.policies)
			}
			if _, err := os.Stat(rt.workDirs[0]); !os.IsNotExist(err) {
				t.Fatalf("scout workdir should be removed after return, stat error = %v", err)
			}
		})
	}
}

func TestRunScoutFailsBeforeRuntimeWhenWorkDirCreationFails(t *testing.T) {
	rt := &stubRuntime{}
	p, database := newLoopTestPipeline(t, rt)
	p.cfg = &config.Config{WorkspaceDir: t.TempDir()}
	issue := &models.Issue{Repo: "owner/repo", IssueNumber: 98, Title: "untrusted issue"}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	result, err := p.runScout(context.Background(), issue)
	if result != nil || err == nil || !strings.Contains(err.Error(), "create scout workdir") {
		t.Fatalf("runScout = (%+v, %v), want workdir creation failure", result, err)
	}
	if rt.index != 0 {
		t.Fatalf("runtime calls = %d, want 0", rt.index)
	}
	events, err := database.GetEventsByIssue(issue.ID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events) != 1 || events[0].Stage != "scout" || events[0].Success || events[0].Verdict != "error" || !strings.Contains(events[0].ErrorMessage, "create scout workdir") {
		t.Fatalf("events = %+v, want recorded scout workdir creation failure", events)
	}
}

func TestRunScoutRejectsTempDirInsideWorkspace(t *testing.T) {
	for _, location := range []string{"equal", "nested", "symlink"} {
		t.Run(location, func(t *testing.T) {
			workspaceDir := t.TempDir()
			tempBase := workspaceDir
			if location == "nested" {
				tempBase = filepath.Join(workspaceDir, "tmp")
				if err := os.Mkdir(tempBase, 0700); err != nil {
					t.Fatalf("create nested temp base: %v", err)
				}
			}
			if location == "symlink" {
				tempBase = filepath.Join(t.TempDir(), "workspace-link")
				if err := os.Symlink(workspaceDir, tempBase); err != nil {
					t.Fatalf("link temp base to workspace: %v", err)
				}
			}
			rt := &stubRuntime{outputs: []stubOutput{{output: `{"verdict":"PROCEED"}`}}}
			p, database := newLoopTestPipeline(t, rt)
			p.cfg = &config.Config{WorkspaceDir: workspaceDir}
			promptsDir := t.TempDir()
			writePromptTemplate(t, promptsDir, "scout", `scout {{.IssueData}}`)
			p.prompts = prompt.NewStore(promptsDir)
			if err := p.prompts.Load(); err != nil {
				t.Fatalf("load scout prompt: %v", err)
			}
			p.runner = NewAgentRunner(p.prompts, rt, 0)
			issue := &models.Issue{Repo: "owner/repo", IssueNumber: 98, Title: "untrusted issue"}
			if err := database.CreateIssue(issue); err != nil {
				t.Fatalf("create issue: %v", err)
			}
			t.Setenv("TMPDIR", tempBase)

			result, err := p.runScout(context.Background(), issue)
			if result != nil || err == nil || !strings.Contains(err.Error(), "outside WorkspaceDir") {
				t.Fatalf("runScout = (%+v, %v), want isolation failure", result, err)
			}
			if rt.index != 0 {
				t.Fatalf("runtime calls = %d, want 0", rt.index)
			}
			entries, err := os.ReadDir(tempBase)
			if err != nil {
				t.Fatalf("read temp base: %v", err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "auto-contributor-scout-") {
					t.Fatalf("scout workdir was not removed: %s", entry.Name())
				}
			}
			events, err := database.GetEventsByIssue(issue.ID)
			if err != nil {
				t.Fatalf("get events: %v", err)
			}
			if len(events) != 1 || events[0].Success || events[0].Verdict != "error" || !strings.Contains(events[0].ErrorMessage, "outside WorkspaceDir") {
				t.Fatalf("events = %+v, want recorded scout isolation failure", events)
			}
		})
	}
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
