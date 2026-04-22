package db

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

func TestCreatePullRequestPopulatesIDAndSupportsUpdateSQLite(t *testing.T) {
	db := newSQLiteTestDB(t)
	testCreatePullRequestPopulatesIDAndSupportsUpdate(t, db)
}

func TestCreatePullRequestPopulatesIDAndSupportsUpdatePostgres(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	db, err := NewWithURL(databaseURL, filepath.Join(t.TempDir(), "unused.db"))
	if err != nil {
		t.Fatalf("create postgres test db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	testCreatePullRequestPopulatesIDAndSupportsUpdate(t, db)
}

func testCreatePullRequestPopulatesIDAndSupportsUpdate(t *testing.T, db *DB) {
	uniqueSuffix := time.Now().UnixNano()
	repo := fmt.Sprintf("owner/repo-%d", uniqueSuffix)
	issueNumber := int(uniqueSuffix % 1000000)
	prNumber := issueNumber
	branchName := fmt.Sprintf("feat/issue-%d", uniqueSuffix)
	prURL := fmt.Sprintf("https://example.com/%s/pull/%d", repo, prNumber)

	t.Helper()

	if db.IsPostgres() {
		t.Cleanup(func() {
			if _, err := db.Exec("DELETE FROM pull_requests WHERE pr_url = $1", prURL); err != nil {
				t.Fatalf("cleanup pull_requests: %v", err)
			}
			if _, err := db.Exec("DELETE FROM issues WHERE repo = $1 AND issue_number = $2", repo, issueNumber); err != nil {
				t.Fatalf("cleanup issues: %v", err)
			}
		})
	}

	issue := &models.Issue{
		Repo:            repo,
		IssueNumber:     issueNumber,
		Title:           "Populate PR primary key",
		Body:            "Regression coverage for PR inserts",
		Labels:          "bug",
		Language:        "Go",
		DifficultyScore: 0.5,
		Status:          models.IssueStatusDiscovered,
	}
	if err := db.CreateIssue(issue); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if issue.ID <= 0 {
		t.Fatalf("issue ID = %d, want > 0", issue.ID)
	}

	pr := &models.PullRequest{
		IssueID:    issue.ID,
		PRURL:      prURL,
		PRNumber:   prNumber,
		BranchName: branchName,
		Status:     models.PRStatusDraft,
		CIStatus:   "pending",
	}
	if err := db.CreatePullRequest(pr); err != nil {
		t.Fatalf("create pull request: %v", err)
	}
	if pr.ID <= 0 {
		t.Fatalf("pull request ID = %d, want > 0", pr.ID)
	}

	if err := db.UpdatePRStatus(pr.ID, models.PRStatusOpen); err != nil {
		t.Fatalf("update PR status: %v", err)
	}

	stored, err := db.getPRByID(pr.ID)
	if err != nil {
		t.Fatalf("get PR by ID: %v", err)
	}
	if stored.Status != models.PRStatusOpen {
		t.Fatalf("stored status = %q, want %q", stored.Status, models.PRStatusOpen)
	}
}

func newSQLiteTestDB(t *testing.T) *DB {
	t.Helper()

	db, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create sqlite test db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
