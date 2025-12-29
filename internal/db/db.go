package db

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// DB wraps the database connection
type DB struct {
	*sql.DB
}

// New creates a new database connection
func New(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, err
	}

	db := &DB{sqlDB}
	if err := db.Migrate(); err != nil {
		return nil, err
	}

	return db, nil
}

// Migrate creates the database schema
func (db *DB) Migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS issues (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		repo TEXT NOT NULL,
		issue_number INTEGER NOT NULL,
		title TEXT NOT NULL,
		body TEXT,
		labels TEXT,
		language TEXT,
		difficulty_score REAL DEFAULT 0.5,
		status TEXT DEFAULT 'discovered',
		error_message TEXT,
		retry_count INTEGER DEFAULT 0,
		discovered_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(repo, issue_number)
	);

	CREATE INDEX IF NOT EXISTS idx_issues_status ON issues(status);
	CREATE INDEX IF NOT EXISTS idx_issues_repo ON issues(repo);

	CREATE TABLE IF NOT EXISTS pull_requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		issue_id INTEGER NOT NULL,
		pr_url TEXT NOT NULL,
		pr_number INTEGER,
		branch_name TEXT NOT NULL,
		status TEXT DEFAULT 'open',
		ci_status TEXT DEFAULT 'pending',
		retry_count INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (issue_id) REFERENCES issues(id)
	);

	CREATE INDEX IF NOT EXISTS idx_prs_status ON pull_requests(status);

	CREATE TABLE IF NOT EXISTS solve_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		issue_id INTEGER NOT NULL,
		attempt_number INTEGER DEFAULT 1,
		started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		completed_at DATETIME,
		duration_seconds REAL,
		prompt_version TEXT DEFAULT 'v1',
		model_used TEXT DEFAULT 'claude-sonnet-4',
		files_changed TEXT,
		claude_output_preview TEXT,
		fix_complete_marker INTEGER DEFAULT 0,
		claude_tests_passed INTEGER,
		is_complex INTEGER,
		can_test_locally INTEGER,
		complexity_reasons TEXT,
		external_test_passed INTEGER,
		test_framework TEXT,
		test_duration_seconds REAL,
		test_output_preview TEXT,
		success INTEGER DEFAULT 0,
		failure_reason TEXT,
		error_details TEXT,
		FOREIGN KEY (issue_id) REFERENCES issues(id)
	);

	CREATE INDEX IF NOT EXISTS idx_attempts_issue ON solve_attempts(issue_id);
	CREATE INDEX IF NOT EXISTS idx_attempts_started ON solve_attempts(started_at);

	CREATE TABLE IF NOT EXISTS issue_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		issue_id INTEGER NOT NULL UNIQUE,
		estimated_difficulty REAL DEFAULT 0.5,
		actual_difficulty REAL,
		repo_stars INTEGER,
		repo_language TEXT,
		repo_has_contributing INTEGER DEFAULT 0,
		repo_has_claude_md INTEGER DEFAULT 0,
		repo_test_framework TEXT,
		issue_body_length INTEGER DEFAULT 0,
		issue_has_code_blocks INTEGER DEFAULT 0,
		issue_has_stack_trace INTEGER DEFAULT 0,
		issue_labels_count INTEGER DEFAULT 0,
		total_attempts INTEGER DEFAULT 0,
		successful_attempts INTEGER DEFAULT 0,
		total_time_spent_seconds REAL DEFAULT 0,
		first_attempt_success INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (issue_id) REFERENCES issues(id)
	);

	CREATE TABLE IF NOT EXISTS daily_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date TEXT NOT NULL UNIQUE,
		issues_discovered INTEGER DEFAULT 0,
		issues_attempted INTEGER DEFAULT 0,
		issues_solved INTEGER DEFAULT 0,
		prs_created INTEGER DEFAULT 0,
		prs_merged INTEGER DEFAULT 0,
		prs_closed INTEGER DEFAULT 0,
		avg_solve_time_seconds REAL,
		avg_attempts_per_issue REAL,
		first_attempt_success_rate REAL,
		overall_success_rate REAL,
		stats_by_language TEXT,
		stats_by_repo TEXT,
		failure_reasons_count TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_daily_date ON daily_stats(date);
	`

	_, err := db.Exec(schema)
	if err != nil {
		return err
	}

	// Add retry_count column if it doesn't exist (migration for existing DBs)
	db.Exec("ALTER TABLE issues ADD COLUMN retry_count INTEGER DEFAULT 0")

	return nil
}

// CreateIssue inserts a new issue
func (db *DB) CreateIssue(issue *models.Issue) error {
	result, err := db.Exec(`
		INSERT INTO issues (repo, issue_number, title, body, labels, language, difficulty_score, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo, issue_number) DO UPDATE SET
			title = excluded.title,
			body = excluded.body,
			labels = excluded.labels,
			updated_at = CURRENT_TIMESTAMP
	`, issue.Repo, issue.IssueNumber, issue.Title, issue.Body, issue.Labels, issue.Language, issue.DifficultyScore, issue.Status)

	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	issue.ID = id
	return nil
}

// GetIssuesByStatus retrieves issues by status
func (db *DB) GetIssuesByStatus(status models.IssueStatus, limit int) ([]*models.Issue, error) {
	rows, err := db.Query(`
		SELECT id, repo, issue_number, title, body, labels, language, difficulty_score, status, error_message, discovered_at, updated_at
		FROM issues
		WHERE status = ?
		ORDER BY difficulty_score ASC
		LIMIT ?
	`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var issues []*models.Issue
	for rows.Next() {
		issue := &models.Issue{}
		err := rows.Scan(
			&issue.ID, &issue.Repo, &issue.IssueNumber, &issue.Title, &issue.Body,
			&issue.Labels, &issue.Language, &issue.DifficultyScore, &issue.Status,
			&issue.ErrorMessage, &issue.DiscoveredAt, &issue.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}

	return issues, nil
}

// UpdateIssueStatus updates the status of an issue
func (db *DB) UpdateIssueStatus(id int64, status models.IssueStatus, errorMsg string) error {
	_, err := db.Exec(`
		UPDATE issues SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, status, errorMsg, id)
	return err
}

// IncrementIssueRetryCount increments the retry count for an issue
func (db *DB) IncrementIssueRetryCount(id int64) error {
	_, err := db.Exec(`
		UPDATE issues SET retry_count = retry_count + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, id)
	return err
}

// CreatePullRequest inserts a new pull request
func (db *DB) CreatePullRequest(pr *models.PullRequest) error {
	result, err := db.Exec(`
		INSERT INTO pull_requests (issue_id, pr_url, pr_number, branch_name, status, ci_status)
		VALUES (?, ?, ?, ?, ?, ?)
	`, pr.IssueID, pr.PRURL, pr.PRNumber, pr.BranchName, pr.Status, pr.CIStatus)

	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	pr.ID = id
	return nil
}

// GetOpenPRs retrieves all open pull requests
func (db *DB) GetOpenPRs() ([]*models.PullRequest, error) {
	rows, err := db.Query(`
		SELECT id, issue_id, pr_url, pr_number, branch_name, status, ci_status, retry_count, created_at, updated_at
		FROM pull_requests
		WHERE status = 'open'
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
			&pr.Status, &pr.CIStatus, &pr.RetryCount, &pr.CreatedAt, &pr.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		prs = append(prs, pr)
	}

	return prs, nil
}

// CreateSolveAttempt inserts a new solve attempt
func (db *DB) CreateSolveAttempt(attempt *models.SolveAttempt) error {
	result, err := db.Exec(`
		INSERT INTO solve_attempts (issue_id, attempt_number, started_at, prompt_version, model_used)
		VALUES (?, ?, ?, ?, ?)
	`, attempt.IssueID, attempt.AttemptNumber, attempt.StartedAt, attempt.PromptVersion, attempt.ModelUsed)

	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	attempt.ID = id
	return nil
}

// UpdateSolveAttempt updates a solve attempt with results
func (db *DB) UpdateSolveAttempt(attempt *models.SolveAttempt) error {
	_, err := db.Exec(`
		UPDATE solve_attempts SET
			completed_at = ?,
			duration_seconds = ?,
			files_changed = ?,
			claude_output_preview = ?,
			fix_complete_marker = ?,
			claude_tests_passed = ?,
			is_complex = ?,
			can_test_locally = ?,
			complexity_reasons = ?,
			external_test_passed = ?,
			test_framework = ?,
			test_duration_seconds = ?,
			test_output_preview = ?,
			success = ?,
			failure_reason = ?,
			error_details = ?
		WHERE id = ?
	`,
		attempt.CompletedAt,
		attempt.DurationSeconds,
		attempt.FilesChanged,
		attempt.ClaudeOutputPreview,
		attempt.FixCompleteMarker,
		attempt.ClaudeTestsPassed,
		attempt.IsComplex,
		attempt.CanTestLocally,
		attempt.ComplexityReasons,
		attempt.ExternalTestPassed,
		attempt.TestFramework,
		attempt.TestDurationSeconds,
		attempt.TestOutputPreview,
		attempt.Success,
		attempt.FailureReason,
		attempt.ErrorDetails,
		attempt.ID,
	)
	return err
}

// GetAttemptCount returns the number of attempts for an issue
func (db *DB) GetAttemptCount(issueID int64) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM solve_attempts WHERE issue_id = ?`, issueID).Scan(&count)
	return count, err
}

// ClaimNextPendingIssue atomically claims the highest-scored pending issue for a worker
// Returns nil if no pending issues are available
func (db *DB) ClaimNextPendingIssue(workerID int) (*models.Issue, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Find the highest-scored pending issue
	row := tx.QueryRow(`
		SELECT id, repo, issue_number, title, body, labels, language, difficulty_score, status, retry_count, discovered_at, updated_at
		FROM issues
		WHERE status = 'pending'
		ORDER BY difficulty_score DESC
		LIMIT 1
	`)

	issue := &models.Issue{}
	err = row.Scan(
		&issue.ID, &issue.Repo, &issue.IssueNumber, &issue.Title, &issue.Body,
		&issue.Labels, &issue.Language, &issue.DifficultyScore, &issue.Status,
		&issue.RetryCount, &issue.DiscoveredAt, &issue.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No pending issues
		}
		return nil, err
	}

	// Claim it by updating status to processing
	_, err = tx.Exec(`
		UPDATE issues SET status = 'processing', updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'pending'
	`, issue.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	issue.Status = models.IssueStatusProcessing
	return issue, nil
}

// GetPendingIssueCount returns the number of pending issues
func (db *DB) GetPendingIssueCount() (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM issues WHERE status = 'pending'`).Scan(&count)
	return count, err
}

// MarkIssueCompleted marks an issue as completed with PR info
func (db *DB) MarkIssueCompleted(issueID int64, prURL string) error {
	_, err := db.Exec(`
		UPDATE issues SET status = 'completed', updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, issueID)
	return err
}

// MarkIssueFailed marks an issue as failed, optionally for retry
func (db *DB) MarkIssueFailed(issueID int64, errorMsg string, canRetry bool) error {
	status := "failed"
	if canRetry {
		status = "pending" // Put back in queue for retry
	}
	_, err := db.Exec(`
		UPDATE issues SET status = ?, error_message = ?, retry_count = retry_count + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, status, errorMsg, issueID)
	return err
}

// GetStats returns summary statistics
func (db *DB) GetStats(days int) (map[string]interface{}, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	stats := make(map[string]interface{})

	// Total attempts
	var totalAttempts, successfulAttempts int
	var avgDuration sql.NullFloat64

	db.QueryRow(`
		SELECT COUNT(*), SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), AVG(duration_seconds)
		FROM solve_attempts
		WHERE started_at >= ?
	`, cutoff).Scan(&totalAttempts, &successfulAttempts, &avgDuration)

	stats["total_attempts"] = totalAttempts
	stats["successful_attempts"] = successfulAttempts
	if totalAttempts > 0 {
		stats["success_rate"] = float64(successfulAttempts) / float64(totalAttempts)
	} else {
		stats["success_rate"] = 0.0
	}
	if avgDuration.Valid {
		stats["avg_duration_seconds"] = avgDuration.Float64
	} else {
		stats["avg_duration_seconds"] = 0.0
	}

	return stats, nil
}
