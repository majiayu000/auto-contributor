package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/majiayu000/auto-contributor/internal/config"
	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/prompt"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

func TestProcessPRCLAStatus(t *testing.T) {
	request := "Thank you for your submission, please sign the CLA. You have signed the CLA already but the status is still pending? Let us recheck it."
	classicRequest := "Thank you for your submission! We ask that you sign our [Contributor License Agreement](https://example.com/cla). You have signed the CLA already but the status is still pending?"
	bot := func(body string) ghclient.IssueComment {
		return ghclient.IssueComment{Author: "cla-assistant[bot]", Body: body}
	}
	for _, tc := range []struct {
		name     string
		initial  models.PRStatus
		comments []ghclient.IssueComment
		draft    bool
		want     models.PRStatus
		feedback bool
	}{
		{"unsigned thanks and recheck", models.PRStatusOpen, []ghclient.IssueComment{bot(request)}, false, models.PRStatusNeedsAttention, false},
		{"classic unsigned request", models.PRStatusOpen, []ghclient.IssueComment{bot(classicRequest)}, false, models.PRStatusNeedsAttention, false},
		{"still unsigned", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot(request)}, false, models.PRStatusNeedsAttention, false},
		{"thanks is not signed", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot("Thank you for your submission!")}, false, models.PRStatusNeedsAttention, false},
		{"not signed", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot("All contributors have not signed the CLA.")}, false, models.PRStatusNeedsAttention, false},
		{"not all signed", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot("Not all contributors have signed the CLA.")}, false, models.PRStatusNeedsAttention, false},
		{"negative status after old confirmation", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot("All contributors have signed the CLA."), bot("Not all contributors have signed the CLA.")}, false, models.PRStatusNeedsAttention, false},
		{"mixed message still requires signing", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot("All contributors have signed the CLA? If not, please sign the CLA.")}, false, models.PRStatusNeedsAttention, false},
		{"no CLA comment", models.PRStatusNeedsAttention, nil, false, models.PRStatusNeedsAttention, false},
		{"human claim is not confirmation", models.PRStatusNeedsAttention, []ghclient.IssueComment{{Author: "contributor", Body: "All contributors have signed the CLA."}}, false, models.PRStatusNeedsAttention, false},
		{"signed after old request resumes feedback", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot(request), bot("All contributors have signed the CLA.")}, false, models.PRStatusOpen, true},
		{"edited classic confirmation resumes feedback", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot("All committers have signed the CLA.")}, false, models.PRStatusOpen, true},
		{"classic badge confirmation resumes feedback", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot("[![CLA assistant check](https://example.com/badge)](https://example.com/cla) <br/>All committers have signed the CLA.")}, false, models.PRStatusOpen, true},
		{"open with historical request stays open", models.PRStatusOpen, []ghclient.IssueComment{bot(request), bot("All contributors have signed the CLA.")}, false, models.PRStatusOpen, true},
		{"new request overrides old confirmation", models.PRStatusOpen, []ghclient.IssueComment{bot("All contributors have signed the CLA."), bot(request)}, false, models.PRStatusNeedsAttention, false},
		{"draft still waits for signing", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot(request)}, true, models.PRStatusNeedsAttention, false},
		{"signed draft returns to draft handling", models.PRStatusNeedsAttention, []ghclient.IssueComment{bot("All committers have signed the CLA.")}, true, models.PRStatusDraft, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, pr, rt := newCLATestPipeline(t, tc.initial, tc.comments, tc.draft)
			if err := p.ProcessPR(context.Background(), pr); err != nil {
				t.Fatalf("ProcessPR: %v", err)
			}
			status, round, checked := getFeedbackPRState(t, p.db, pr.ID)
			if status != string(tc.want) || pr.Status != tc.want {
				t.Errorf("status = %q (memory %q), want %q", status, pr.Status, tc.want)
			}
			wantCalls := 0
			if tc.feedback {
				wantCalls = 1
			}
			if rt.index != wantCalls || round != wantCalls || checked.Valid != tc.feedback {
				t.Errorf("responder calls/round/checked = %d/%d/%v, want %d/%d/%v", rt.index, round, checked.Valid, wantCalls, wantCalls, tc.feedback)
			}
			if tc.feedback && (len(rt.prompts) == 0 || !strings.Contains(rt.prompts[0], "Please address this maintainer feedback")) {
				t.Errorf("responder did not receive waiting maintainer feedback: %v", rt.prompts)
			}
		})
	}
}

func TestProcessPRCLAErrorsRemainRetryable(t *testing.T) {
	for _, initial := range []models.PRStatus{models.PRStatusOpen, models.PRStatusNeedsAttention} {
		for _, failure := range []string{"fetch", "parse", "status update"} {
			t.Run(string(initial)+"/"+failure, func(t *testing.T) {
				body := "Please sign the CLA."
				if initial == models.PRStatusNeedsAttention {
					body = "All contributors have signed the CLA."
				}
				p, pr, rt := newCLATestPipeline(t, initial, []ghclient.IssueComment{{Author: "cla-assistant[bot]", Body: body}}, false)
				wantError := "get issue comments"
				switch failure {
				case "fetch":
					t.Setenv("GH_TEST_CLA_FAIL", "1")
				case "parse":
					t.Setenv("GH_TEST_CLA_COMMENTS", "invalid JSON")
				case "status update":
					if _, err := p.db.Exec(`CREATE TRIGGER fail_cla_status BEFORE UPDATE OF status ON pull_requests BEGIN SELECT RAISE(FAIL, 'status unavailable'); END`); err != nil {
						t.Fatal(err)
					}
					wantError = "update PR status"
				}
				if err := p.ProcessPR(context.Background(), pr); err == nil || !strings.Contains(err.Error(), wantError) {
					t.Errorf("ProcessPR error = %v, want %q", err, wantError)
				}
				status, round, checked := getFeedbackPRState(t, p.db, pr.ID)
				if status != string(initial) || pr.Status != initial || round != 0 || checked.Valid || rt.index != 0 {
					t.Errorf("failed CLA check changed retry state: %s/%s/%d/%v, calls=%d", status, pr.Status, round, checked.Valid, rt.index)
				}
				prs, err := p.db.GetOpenPRs()
				if err != nil || len(prs) != 1 {
					t.Errorf("GetOpenPRs = %v, %v, want one retryable PR", prs, err)
				}
			})
		}
	}
}

func newCLATestPipeline(t *testing.T, status models.PRStatus, comments []ghclient.IssueComment, draft bool) (*Pipeline, *models.PullRequest, *stubRuntime) {
	t.Helper()
	rt := &stubRuntime{outputs: []stubOutput{{output: `{"action":"no_action"}`}}}
	p, database := newLoopTestPipeline(t, rt)
	promptsDir := t.TempDir()
	writePromptTemplate(t, promptsDir, "responder", `{{.InlineCommentsData}}`)
	p.prompts = prompt.NewStore(promptsDir)
	if err := p.prompts.Load(); err != nil {
		t.Fatal(err)
	}
	p.runner = NewAgentRunner(p.prompts, rt, 0)
	issue, pr := createFeedbackTestPR(t, database)
	pr.Status = status
	pr.CreatedAt = time.Now()
	if err := database.UpdatePRStatus(pr.ID, status); err != nil {
		t.Fatal(err)
	}
	p.cfg = &config.Config{WorkspaceDir: t.TempDir()}
	p.gh = ghclient.New(p.cfg)
	workspace, err := p.createWorkspace(issue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  "pr view "*) printf '%s' "$GH_TEST_CLA_INFO" ;;
  "pr checks "*) printf '%s' '[{"name":"build","state":"PENDING"}]' ;;
  "api repos/owner/repo/issues/42/comments"*)
    if [ "$GH_TEST_CLA_FAIL" = "1" ]; then
      printf 'temporary CLA fetch failure\n' >&2
      exit 1
    fi
    printf '%s' "$GH_TEST_CLA_COMMENTS"
    ;;
  "api repos/owner/repo/pulls/42/comments"*)
    printf '%s' '[{"id":1,"body":"Please address this maintainer feedback","user":{"login":"maintainer"},"created_at":"2026-09-30T00:00:00Z"}]'
    ;;
  "api repos/owner/repo/issues/70/comments"*) printf '%s' '[]' ;;
  *) printf 'unexpected gh arguments: %s\n' "$*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	info, err := json.Marshal(map[string]any{"state": "OPEN", "isDraft": draft, "headRefName": pr.BranchName, "reviews": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	var rawComments []any
	for _, c := range comments {
		rawComments = append(rawComments, map[string]any{"body": c.Body, "user": map[string]string{"login": c.Author}})
	}
	data, err := json.Marshal(rawComments)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TEST_CLA_INFO", string(info))
	t.Setenv("GH_TEST_CLA_COMMENTS", string(data))
	t.Setenv("GH_TEST_CLA_FAIL", "0")
	return p, pr, rt
}
