package db

import (
	"fmt"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// CreatePullRequest inserts a new pull request
func (db *DB) CreatePullRequest(pr *models.PullRequest) error {
	query := fmt.Sprintf(`
		INSERT INTO pull_requests (issue_id, pr_url, pr_number, branch_name, status, ci_status)
		VALUES (%s)
	`, db.placeholders(6))

	args := []interface{}{pr.IssueID, pr.PRURL, pr.PRNumber, pr.BranchName, pr.Status, pr.CIStatus}
	if db.IsPostgres() {
		if err := db.QueryRow(query+" RETURNING id", args...).Scan(&pr.ID); err != nil {
			return fmt.Errorf("insert pull request: %w", err)
		}
		if pr.ID <= 0 {
			return fmt.Errorf("insert pull request: invalid id %d", pr.ID)
		}
		return nil
	}

	result, err := db.Exec(query, args...)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("resolve pull request id: %w", err)
	}
	if id <= 0 {
		return fmt.Errorf("resolve pull request id: invalid id %d", id)
	}

	pr.ID = id
	return nil
}

// CountOpenPRsByRepo returns the number of open/draft PRs for a given repo.
func (db *DB) CountOpenPRsByRepo(repo string) (int, error) {
	var count int
	query := fmt.Sprintf(`
		SELECT COUNT(*) FROM pull_requests p
		JOIN issues i ON p.issue_id = i.id
		WHERE i.repo = %s AND p.status IN ('open', 'draft')
	`, db.placeholder(1))
	err := db.QueryRow(query, repo).Scan(&count)
	return count, err
}

// CountMergedPRsByRepo returns the number of merged PRs for a given repo.
func (db *DB) CountMergedPRsByRepo(repo string) (int, error) {
	var count int
	query := fmt.Sprintf(`
		SELECT COUNT(*) FROM pull_requests p
		JOIN issues i ON p.issue_id = i.id
		WHERE i.repo = %s AND p.status = 'merged'
	`, db.placeholder(1))
	err := db.QueryRow(query, repo).Scan(&count)
	return count, err
}

// GetOpenPRs retrieves all open pull requests
func (db *DB) GetOpenPRs() ([]*models.PullRequest, error) {
	rows, err := db.Query(`
		SELECT id, issue_id, pr_url, pr_number, branch_name, status, ci_status, retry_count,
			   review_comment_count, first_review_at, last_feedback_check_at, feedback_round,
			   created_at, updated_at
		FROM pull_requests
		WHERE status IN ('open', 'draft', 'needs_attention')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prs []*models.PullRequest
	for rows.Next() {
		pr := &models.PullRequest{}
		err := rows.Scan(
			&pr.ID, &pr.IssueID, &pr.PRURL, &pr.PRNumber, &pr.BranchName,
			&pr.Status, &pr.CIStatus, &pr.RetryCount,
			&pr.ReviewCommentCount, &pr.FirstReviewAt, &pr.LastFeedbackCheckAt, &pr.FeedbackRound,
			&pr.CreatedAt, &pr.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		prs = append(prs, pr)
	}

	return prs, nil
}

// GetNeedsAttentionPRs retrieves PRs that require manual action (e.g. CLA signing).
func (db *DB) GetNeedsAttentionPRs() ([]*models.PullRequest, error) {
	rows, err := db.Query(`
		SELECT id, issue_id, pr_url, pr_number, branch_name, status, ci_status, retry_count,
			   review_comment_count, first_review_at, last_feedback_check_at, feedback_round,
			   created_at, updated_at
		FROM pull_requests
		WHERE status = 'needs_attention'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prs []*models.PullRequest
	for rows.Next() {
		pr := &models.PullRequest{}
		err := rows.Scan(
			&pr.ID, &pr.IssueID, &pr.PRURL, &pr.PRNumber, &pr.BranchName,
			&pr.Status, &pr.CIStatus, &pr.RetryCount,
			&pr.ReviewCommentCount, &pr.FirstReviewAt, &pr.LastFeedbackCheckAt, &pr.FeedbackRound,
			&pr.CreatedAt, &pr.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

// GetSlowRepos returns repos that have open PRs with no feedback for 7+ days.
func (db *DB) GetSlowRepos() ([]string, error) {
	var timeExpr string
	if db.IsPostgres() {
		timeExpr = "NOW() - INTERVAL '7 days'"
	} else {
		timeExpr = "datetime('now', '-7 days')"
	}
	rows, err := db.Query(fmt.Sprintf(`
		SELECT DISTINCT i.repo
		FROM pull_requests pr
		JOIN issues i ON pr.issue_id = i.id
		WHERE pr.status IN ('open', 'draft')
		  AND pr.review_comment_count = 0
		  AND pr.created_at < %s
	`, timeExpr))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []string
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, nil
}

// UpdatePRFeedbackCheck updates the last feedback check timestamp and round.
func (db *DB) UpdatePRFeedbackCheck(prID int64, round int) error {
	query := fmt.Sprintf(`
		UPDATE pull_requests SET last_feedback_check_at = CURRENT_TIMESTAMP, feedback_round = %s, updated_at = CURRENT_TIMESTAMP
		WHERE id = %s
	`, db.placeholder(1), db.placeholder(2))
	_, err := db.Exec(query, round, prID)
	return err
}

// UpdatePRBranchName stores the latest GitHub head branch for a tracked PR.
func (db *DB) UpdatePRBranchName(prID int64, branchName string) error {
	query := fmt.Sprintf(`
		UPDATE pull_requests SET branch_name = %s, updated_at = CURRENT_TIMESTAMP
		WHERE id = %s
	`, db.placeholder(1), db.placeholder(2))
	_, err := db.Exec(query, branchName, prID)
	return err
}

// UpdatePRReviewStats updates review_comment_count and first_review_at.
func (db *DB) UpdatePRReviewStats(prID int64, commentCount int, firstReviewAt *time.Time) error {
	query := fmt.Sprintf(`
		UPDATE pull_requests SET review_comment_count = %s, first_review_at = COALESCE(first_review_at, %s), updated_at = CURRENT_TIMESTAMP
		WHERE id = %s
	`, db.placeholder(1), db.placeholder(2), db.placeholder(3))
	_, err := db.Exec(query, commentCount, firstReviewAt, prID)
	return err
}

// EnsurePRWithIssue creates Issue + PR records if they don't already exist in the DB.
// Used by feedback-loop to sync GitHub-discovered PRs into the local database.
func (db *DB) EnsurePRWithIssue(repo string, prNumber int, prURL, branchName, title, body string) (*models.PullRequest, error) {
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		return nil, fmt.Errorf("branch name is required for synced PR %s#%d", repo, prNumber)
	}

	var prID int64
	query := fmt.Sprintf(`SELECT id FROM pull_requests WHERE pr_url = %s`, db.placeholder(1))
	err := db.QueryRow(query, prURL).Scan(&prID)
	if err == nil {
		if err := db.UpdatePRBranchName(prID, branchName); err != nil {
			return nil, fmt.Errorf("update PR branch name: %w", err)
		}
		return db.getPRByID(prID)
	}

	issueNumber := -prNumber

	issue := &models.Issue{
		Repo:            repo,
		IssueNumber:     issueNumber,
		Title:           title,
		Body:            body,
		Status:          models.IssueStatusCompleted,
		DifficultyScore: 0.5,
		DiscoveredAt:    time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := db.CreateIssue(issue); err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	if issue.ID == 0 {
		q := fmt.Sprintf(`SELECT id FROM issues WHERE repo = %s AND issue_number = %s`,
			db.placeholder(1), db.placeholder(2))
		db.QueryRow(q, repo, issueNumber).Scan(&issue.ID)
	}
	if issue.ID == 0 {
		return nil, fmt.Errorf("failed to resolve issue ID for %s#%d", repo, prNumber)
	}

	pr := &models.PullRequest{
		IssueID:    issue.ID,
		PRURL:      prURL,
		PRNumber:   prNumber,
		BranchName: branchName,
		Status:     models.PRStatusOpen,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := db.CreatePullRequest(pr); err != nil {
		return nil, fmt.Errorf("create PR: %w", err)
	}

	return pr, nil
}

func (db *DB) getPRByID(id int64) (*models.PullRequest, error) {
	query := fmt.Sprintf(`
		SELECT id, issue_id, pr_url, pr_number, branch_name, status, ci_status, retry_count,
			   review_comment_count, first_review_at, last_feedback_check_at, feedback_round,
			   created_at, updated_at
		FROM pull_requests WHERE id = %s
	`, db.placeholder(1))

	pr := &models.PullRequest{}
	err := db.QueryRow(query, id).Scan(
		&pr.ID, &pr.IssueID, &pr.PRURL, &pr.PRNumber, &pr.BranchName,
		&pr.Status, &pr.CIStatus, &pr.RetryCount,
		&pr.ReviewCommentCount, &pr.FirstReviewAt, &pr.LastFeedbackCheckAt, &pr.FeedbackRound,
		&pr.CreatedAt, &pr.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return pr, nil
}

// UpdatePRStatus updates a PR's status and related timestamps
func (db *DB) UpdatePRStatus(prID int64, status models.PRStatus) error {
	now := time.Now()
	switch status {
	case models.PRStatusMerged:
		q := fmt.Sprintf(`UPDATE pull_requests SET status = %s, merged_at = %s, updated_at = %s WHERE id = %s`,
			db.placeholder(1), db.placeholder(2), db.placeholder(3), db.placeholder(4))
		_, err := db.Exec(q, status, now, now, prID)
		return err
	case models.PRStatusClosed:
		q := fmt.Sprintf(`UPDATE pull_requests SET status = %s, closed_at = %s, updated_at = %s WHERE id = %s`,
			db.placeholder(1), db.placeholder(2), db.placeholder(3), db.placeholder(4))
		_, err := db.Exec(q, status, now, now, prID)
		return err
	default:
		q := fmt.Sprintf(`UPDATE pull_requests SET status = %s, updated_at = %s WHERE id = %s`,
			db.placeholder(1), db.placeholder(2), db.placeholder(3))
		_, err := db.Exec(q, status, now, prID)
		return err
	}
}
