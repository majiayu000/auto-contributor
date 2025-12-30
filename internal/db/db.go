package db

import (
	"database/sql"
	"fmt"
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
		merged_at DATETIME,
		closed_at DATETIME,
		review_comment_count INTEGER DEFAULT 0,
		first_review_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (issue_id) REFERENCES issues(id)
	);

	CREATE INDEX IF NOT EXISTS idx_prs_status ON pull_requests(status);
	CREATE INDEX IF NOT EXISTS idx_prs_merged ON pull_requests(merged_at);

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
		prompt_tokens INTEGER DEFAULT 0,
		completion_tokens INTEGER DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		lines_added INTEGER DEFAULT 0,
		lines_deleted INTEGER DEFAULT 0,
		FOREIGN KEY (issue_id) REFERENCES issues(id)
	);

	CREATE INDEX IF NOT EXISTS idx_attempts_issue ON solve_attempts(issue_id);
	CREATE INDEX IF NOT EXISTS idx_attempts_started ON solve_attempts(started_at);
	CREATE INDEX IF NOT EXISTS idx_attempts_success ON solve_attempts(success);

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

	CREATE TABLE IF NOT EXISTS repo_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		repo TEXT NOT NULL UNIQUE,
		language TEXT,
		stars INTEGER DEFAULT 0,
		success_count INTEGER DEFAULT 0,
		attempt_count INTEGER DEFAULT 0,
		prs_created INTEGER DEFAULT 0,
		prs_merged INTEGER DEFAULT 0,
		prs_closed INTEGER DEFAULT 0,
		avg_duration_seconds REAL DEFAULT 0,
		avg_time_to_merge REAL DEFAULT 0,
		total_tokens_used INTEGER DEFAULT 0,
		total_lines_changed INTEGER DEFAULT 0,
		has_contributing INTEGER DEFAULT 0,
		has_claude_md INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_repo_metrics_repo ON repo_metrics(repo);
	CREATE INDEX IF NOT EXISTS idx_repo_metrics_language ON repo_metrics(language);

	CREATE TABLE IF NOT EXISTS blacklist (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		repo TEXT NOT NULL UNIQUE,
		reason TEXT,
		added_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_blacklist_repo ON blacklist(repo);
	`

	_, err := db.Exec(schema)
	if err != nil {
		return err
	}

	// Migrations for existing DBs
	db.Exec("ALTER TABLE issues ADD COLUMN retry_count INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE pull_requests ADD COLUMN merged_at DATETIME")
	db.Exec("ALTER TABLE pull_requests ADD COLUMN closed_at DATETIME")
	db.Exec("ALTER TABLE pull_requests ADD COLUMN review_comment_count INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE pull_requests ADD COLUMN first_review_at DATETIME")
	db.Exec("ALTER TABLE solve_attempts ADD COLUMN prompt_tokens INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE solve_attempts ADD COLUMN completion_tokens INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE solve_attempts ADD COLUMN total_tokens INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE solve_attempts ADD COLUMN lines_added INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE solve_attempts ADD COLUMN lines_deleted INTEGER DEFAULT 0")

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
// Excludes blacklisted repositories
func (db *DB) ClaimNextPendingIssue(workerID int) (*models.Issue, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Find the highest-scored pending issue that's not blacklisted
	row := tx.QueryRow(`
		SELECT i.id, i.repo, i.issue_number, i.title, i.body, i.labels, i.language, i.difficulty_score, i.status, i.retry_count, i.discovered_at, i.updated_at
		FROM issues i
		LEFT JOIN blacklist b ON i.repo = b.repo
		WHERE i.status = 'pending' AND b.repo IS NULL
		ORDER BY i.difficulty_score DESC
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

// GetPRMetrics returns PR-related aggregate statistics
func (db *DB) GetPRMetrics() (*models.PRMetrics, error) {
	metrics := &models.PRMetrics{}

	// Get PR counts by status
	db.QueryRow(`SELECT COUNT(*) FROM pull_requests`).Scan(&metrics.TotalPRs)
	db.QueryRow(`SELECT COUNT(*) FROM pull_requests WHERE status = 'open'`).Scan(&metrics.OpenPRs)
	db.QueryRow(`SELECT COUNT(*) FROM pull_requests WHERE status = 'merged'`).Scan(&metrics.MergedPRs)
	db.QueryRow(`SELECT COUNT(*) FROM pull_requests WHERE status = 'closed'`).Scan(&metrics.ClosedPRs)

	// Calculate merge rate
	if metrics.TotalPRs > 0 {
		metrics.MergeRate = float64(metrics.MergedPRs) / float64(metrics.TotalPRs)
	}

	// Average time to merge (in hours)
	var avgMerge sql.NullFloat64
	db.QueryRow(`
		SELECT AVG((julianday(merged_at) - julianday(created_at)) * 24)
		FROM pull_requests
		WHERE merged_at IS NOT NULL
	`).Scan(&avgMerge)
	if avgMerge.Valid {
		metrics.AvgTimeToMerge = avgMerge.Float64
	}

	// Average time to first review (in hours)
	var avgReview sql.NullFloat64
	db.QueryRow(`
		SELECT AVG((julianday(first_review_at) - julianday(created_at)) * 24)
		FROM pull_requests
		WHERE first_review_at IS NOT NULL
	`).Scan(&avgReview)
	if avgReview.Valid {
		metrics.AvgTimeToFirstReview = avgReview.Float64
	}

	// Average review comments
	var avgComments sql.NullFloat64
	db.QueryRow(`SELECT AVG(review_comment_count) FROM pull_requests`).Scan(&avgComments)
	if avgComments.Valid {
		metrics.AvgReviewComments = avgComments.Float64
	}

	return metrics, nil
}

// GetLanguageMetrics returns metrics grouped by programming language
func (db *DB) GetLanguageMetrics() ([]*models.LanguageMetrics, error) {
	rows, err := db.Query(`
		SELECT
			i.language,
			COUNT(DISTINCT sa.id) as attempt_count,
			SUM(CASE WHEN sa.success = 1 THEN 1 ELSE 0 END) as success_count,
			AVG(sa.duration_seconds) as avg_duration,
			SUM(sa.total_tokens) as total_tokens,
			COUNT(DISTINCT pr.id) as prs_created,
			SUM(CASE WHEN pr.status = 'merged' THEN 1 ELSE 0 END) as prs_merged
		FROM issues i
		LEFT JOIN solve_attempts sa ON i.id = sa.issue_id
		LEFT JOIN pull_requests pr ON i.id = pr.issue_id
		WHERE i.language IS NOT NULL AND i.language != ''
		GROUP BY i.language
		ORDER BY attempt_count DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []*models.LanguageMetrics
	for rows.Next() {
		m := &models.LanguageMetrics{}
		var avgDuration, totalTokens sql.NullFloat64
		var prsCreated, prsMerged sql.NullInt64
		err := rows.Scan(&m.Language, &m.AttemptCount, &m.SuccessCount, &avgDuration, &totalTokens, &prsCreated, &prsMerged)
		if err != nil {
			continue
		}
		if avgDuration.Valid {
			m.AvgDurationSeconds = avgDuration.Float64
		}
		if totalTokens.Valid {
			m.TotalTokensUsed = int(totalTokens.Float64)
		}
		if prsCreated.Valid {
			m.PRsCreated = int(prsCreated.Int64)
		}
		if prsMerged.Valid {
			m.PRsMerged = int(prsMerged.Int64)
		}
		if m.AttemptCount > 0 {
			m.SuccessRate = float64(m.SuccessCount) / float64(m.AttemptCount)
		}
		if m.PRsCreated > 0 {
			m.MergeRate = float64(m.PRsMerged) / float64(m.PRsCreated)
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetRepoMetrics returns metrics for a specific repository or all repos
func (db *DB) GetRepoMetrics(repo string) ([]*models.RepoMetrics, error) {
	query := `
		SELECT
			i.repo,
			i.language,
			COUNT(DISTINCT sa.id) as attempt_count,
			SUM(CASE WHEN sa.success = 1 THEN 1 ELSE 0 END) as success_count,
			AVG(sa.duration_seconds) as avg_duration,
			SUM(sa.total_tokens) as total_tokens,
			SUM(sa.lines_added + sa.lines_deleted) as total_lines,
			COUNT(DISTINCT pr.id) as prs_created,
			SUM(CASE WHEN pr.status = 'merged' THEN 1 ELSE 0 END) as prs_merged,
			SUM(CASE WHEN pr.status = 'closed' THEN 1 ELSE 0 END) as prs_closed
		FROM issues i
		LEFT JOIN solve_attempts sa ON i.id = sa.issue_id
		LEFT JOIN pull_requests pr ON i.id = pr.issue_id
		%s
		GROUP BY i.repo
		ORDER BY attempt_count DESC
	`

	whereClause := ""
	var rows *sql.Rows
	var err error
	if repo != "" {
		whereClause = "WHERE i.repo = ?"
		rows, err = db.Query(fmt.Sprintf(query, whereClause), repo)
	} else {
		rows, err = db.Query(fmt.Sprintf(query, whereClause))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []*models.RepoMetrics
	for rows.Next() {
		m := &models.RepoMetrics{}
		var avgDuration, totalTokens, totalLines sql.NullFloat64
		var prsCreated, prsMerged, prsClosed sql.NullInt64
		err := rows.Scan(&m.Repo, &m.Language, &m.AttemptCount, &m.SuccessCount, &avgDuration, &totalTokens, &totalLines, &prsCreated, &prsMerged, &prsClosed)
		if err != nil {
			continue
		}
		if avgDuration.Valid {
			m.AvgDurationSeconds = avgDuration.Float64
		}
		if totalTokens.Valid {
			m.TotalTokensUsed = int(totalTokens.Float64)
		}
		if totalLines.Valid {
			m.TotalLinesChanged = int(totalLines.Float64)
		}
		if prsCreated.Valid {
			m.PRsCreated = int(prsCreated.Int64)
		}
		if prsMerged.Valid {
			m.PRsMerged = int(prsMerged.Int64)
		}
		if prsClosed.Valid {
			m.PRsClosed = int(prsClosed.Int64)
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetFailureReasonStats returns failure counts grouped by reason
func (db *DB) GetFailureReasonStats() (map[string]int, error) {
	rows, err := db.Query(`
		SELECT failure_reason, COUNT(*) as count
		FROM solve_attempts
		WHERE success = 0 AND failure_reason IS NOT NULL AND failure_reason != ''
		GROUP BY failure_reason
		ORDER BY count DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[string]int)
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			continue
		}
		stats[reason] = count
	}

	return stats, nil
}

// UpdatePRStatus updates a PR's status and related timestamps
func (db *DB) UpdatePRStatus(prID int64, status models.PRStatus) error {
	now := time.Now()
	switch status {
	case models.PRStatusMerged:
		_, err := db.Exec(`
			UPDATE pull_requests SET status = ?, merged_at = ?, updated_at = ?
			WHERE id = ?
		`, status, now, now, prID)
		return err
	case models.PRStatusClosed:
		_, err := db.Exec(`
			UPDATE pull_requests SET status = ?, closed_at = ?, updated_at = ?
			WHERE id = ?
		`, status, now, now, prID)
		return err
	default:
		_, err := db.Exec(`
			UPDATE pull_requests SET status = ?, updated_at = ?
			WHERE id = ?
		`, status, now, prID)
		return err
	}
}

// GetTokenUsageStats returns token usage statistics
func (db *DB) GetTokenUsageStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	var totalPrompt, totalCompletion, totalTokens sql.NullInt64
	var avgPerAttempt sql.NullFloat64
	db.QueryRow(`
		SELECT SUM(prompt_tokens), SUM(completion_tokens), SUM(total_tokens), AVG(total_tokens)
		FROM solve_attempts
		WHERE total_tokens > 0
	`).Scan(&totalPrompt, &totalCompletion, &totalTokens, &avgPerAttempt)

	if totalPrompt.Valid {
		stats["total_prompt_tokens"] = totalPrompt.Int64
	}
	if totalCompletion.Valid {
		stats["total_completion_tokens"] = totalCompletion.Int64
	}
	if totalTokens.Valid {
		stats["total_tokens"] = totalTokens.Int64
	}
	if avgPerAttempt.Valid {
		stats["avg_tokens_per_attempt"] = avgPerAttempt.Float64
	}

	// Token usage by success/failure
	var successTokens, failTokens sql.NullFloat64
	db.QueryRow(`SELECT AVG(total_tokens) FROM solve_attempts WHERE success = 1 AND total_tokens > 0`).Scan(&successTokens)
	db.QueryRow(`SELECT AVG(total_tokens) FROM solve_attempts WHERE success = 0 AND total_tokens > 0`).Scan(&failTokens)
	if successTokens.Valid {
		stats["avg_tokens_successful"] = successTokens.Float64
	}
	if failTokens.Valid {
		stats["avg_tokens_failed"] = failTokens.Float64
	}

	return stats, nil
}

// AddToBlacklist adds a repository to the blacklist
func (db *DB) AddToBlacklist(repo, reason string) error {
	_, err := db.Exec(`
		INSERT INTO blacklist (repo, reason) VALUES (?, ?)
		ON CONFLICT(repo) DO UPDATE SET reason = excluded.reason
	`, repo, reason)
	return err
}

// RemoveFromBlacklist removes a repository from the blacklist
func (db *DB) RemoveFromBlacklist(repo string) error {
	_, err := db.Exec(`DELETE FROM blacklist WHERE repo = ?`, repo)
	return err
}

// IsBlacklisted checks if a repository is blacklisted
func (db *DB) IsBlacklisted(repo string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM blacklist WHERE repo = ?`, repo).Scan(&count)
	return count > 0, err
}

// GetBlacklist returns all blacklisted repositories
func (db *DB) GetBlacklist() ([]*models.BlacklistEntry, error) {
	rows, err := db.Query(`SELECT id, repo, reason, added_at FROM blacklist ORDER BY added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*models.BlacklistEntry
	for rows.Next() {
		entry := &models.BlacklistEntry{}
		var reason sql.NullString
		if err := rows.Scan(&entry.ID, &entry.Repo, &reason, &entry.AddedAt); err != nil {
			continue
		}
		if reason.Valid {
			entry.Reason = reason.String
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
