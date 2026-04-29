package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/majiayu000/auto-contributor/internal/config"
	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

func TestPreparePRWorkspaceChecksOutTrackedBranch(t *testing.T) {
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

	runGitCommand(t, seedDir, "checkout", "-b", "fix/bug-42")
	writeFile(t, filepath.Join(seedDir, "tracked.txt"), "branch\n")
	runGitCommand(t, seedDir, "commit", "-am", "branch")
	runGitCommand(t, seedDir, "push", "origin", "fix/bug-42")

	p := newWorkspaceTestPipeline(t)
	issue := &models.Issue{Repo: "owner/repo", IssueNumber: 60}
	pr := &models.PullRequest{
		PRURL:      "https://github.com/owner/repo/pull/42",
		BranchName: "fix/bug-42",
	}

	workspace, err := p.createWorkspace(issue)
	if err != nil {
		t.Fatalf("createWorkspace: %v", err)
	}
	runGitCommand(t, "", "clone", remoteDir, workspace)
	runGitCommand(t, workspace, "remote", "add", "fork", remoteDir)

	got, err := p.preparePRWorkspace(context.Background(), issue.Repo, pr, issue)
	if err != nil {
		t.Fatalf("preparePRWorkspace: %v", err)
	}
	if got != workspace {
		t.Fatalf("workspace = %q, want %q", got, workspace)
	}

	head := strings.TrimSpace(runGitCommand(t, workspace, "rev-parse", "--abbrev-ref", "HEAD"))
	if head != "fix/bug-42" {
		t.Fatalf("HEAD branch = %q, want %q", head, "fix/bug-42")
	}

	content, err := os.ReadFile(filepath.Join(workspace, "tracked.txt"))
	if err != nil {
		t.Fatalf("read tracked file: %v", err)
	}
	if string(content) != "branch\n" {
		t.Fatalf("tracked.txt = %q, want %q", string(content), "branch\n")
	}
}

func TestPreparePRWorkspaceRequiresBranchName(t *testing.T) {
	p := newWorkspaceTestPipeline(t)
	issue := &models.Issue{Repo: "owner/repo", IssueNumber: 60}
	pr := &models.PullRequest{PRURL: "https://github.com/owner/repo/pull/42"}

	_, err := p.preparePRWorkspace(context.Background(), issue.Repo, pr, issue)
	if err == nil {
		t.Fatal("preparePRWorkspace error = nil, want missing branch error")
	}
	if !strings.Contains(err.Error(), "missing head branch") {
		t.Fatalf("preparePRWorkspace error = %q, want missing head branch", err)
	}
}

func newWorkspaceTestPipeline(t *testing.T) *Pipeline {
	t.Helper()
	cfg := &config.Config{
		WorkspaceDir:   t.TempDir(),
		GitHubUsername: "tester",
	}
	return &Pipeline{
		cfg: cfg,
		gh:  ghclient.New(cfg),
	}
}

func createBareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	runGitCommand(t, "", "init", "--bare", dir)
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return string(output)
}
