package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// DBType represents the database type
type DBType string

const (
	DBTypeSQLite   DBType = "sqlite"
	DBTypePostgres DBType = "postgres"
)

// DB wraps the database connection
type DB struct {
	*sql.DB
	dbType DBType
}

// New creates a new database connection
// If databaseURL is provided (postgres://...), uses PostgreSQL
// Otherwise falls back to SQLite with the given path
func New(path string) (*DB, error) {
	return NewWithURL("", path)
}

// NewWithURL creates a new database connection with optional URL
func NewWithURL(databaseURL, sqlitePath string) (*DB, error) {
	var sqlDB *sql.DB
	var err error
	var dbType DBType

	if databaseURL != "" && (strings.HasPrefix(databaseURL, "postgres") || strings.Contains(databaseURL, "host=")) {
		// PostgreSQL
		sqlDB, err = sql.Open("postgres", databaseURL)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
		}
		dbType = DBTypePostgres

		// Test connection
		if err := sqlDB.Ping(); err != nil {
			return nil, fmt.Errorf("failed to ping PostgreSQL: %w", err)
		}
	} else {
		// SQLite (default)
		sqlDB, err = sql.Open("sqlite3", sqlitePath+"?_foreign_keys=on")
		if err != nil {
			return nil, err
		}
		dbType = DBTypeSQLite
	}

	db := &DB{sqlDB, dbType}
	if err := db.Migrate(); err != nil {
		return nil, err
	}

	return db, nil
}

// IsPostgres returns true if using PostgreSQL
func (db *DB) IsPostgres() bool {
	return db.dbType == DBTypePostgres
}

// placeholder returns the correct placeholder for the given index (1-based)
// PostgreSQL uses $1, $2, etc. SQLite uses ?
func (db *DB) placeholder(index int) string {
	if db.IsPostgres() {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

// placeholders returns a comma-separated list of placeholders
func (db *DB) placeholders(count int) string {
	parts := make([]string, count)
	for i := 0; i < count; i++ {
		parts[i] = db.placeholder(i + 1)
	}
	return strings.Join(parts, ", ")
}

func (db *DB) repoLikeWhereClause(column, repoFilter string, placeholderIndex int) (string, []any) {
	if repoFilter == "" {
		return "", nil
	}
	return fmt.Sprintf(" WHERE %s LIKE %s", column, db.placeholder(placeholderIndex)), []any{repoFilter}
}

// Migrate creates the database schema
func (db *DB) Migrate() error {
	var schema string

	if db.IsPostgres() {
		schema = db.postgresSchema()
	} else {
		schema = db.sqliteSchema()
	}

	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	// Column-level migrations intentionally ignore errors (column may already exist).
	db.runMigrations()
	db.MigrateLessons()
	if err := db.MigrateEvents(); err != nil {
		return fmt.Errorf("migrate events: %w", err)
	}
	// Table-level migration: propagate errors so startup fails fast on DDL failure.
	if err := db.MigrateRepoProfiles(); err != nil {
		return err
	}
	if err := db.MigrateTrajectories(); err != nil {
		return err
	}

	return nil
}

func (db *DB) sqliteSchema() string {
	return `
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
		last_feedback_check_at DATETIME,
		feedback_round INTEGER DEFAULT 0,
		outcome_recorded INTEGER DEFAULT 0,
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

	CREATE TABLE IF NOT EXISTS repo_profiles (
		repo TEXT PRIMARY KEY,
		total_prs_submitted INTEGER DEFAULT 0,
		total_merged INTEGER DEFAULT 0,
		total_rejected INTEGER DEFAULT 0,
		merge_rate REAL DEFAULT 0,
		avg_response_time_hours REAL,
		requires_cla INTEGER DEFAULT 0,
		requires_assignment INTEGER DEFAULT 0,
		preferred_pr_size TEXT,
		blacklisted INTEGER DEFAULT 0,
		blacklist_reason TEXT,
		cooldown_until DATETIME,
		strategy_notes TEXT,
		last_interaction DATETIME,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`
}

func (db *DB) postgresSchema() string {
	return `
	CREATE TABLE IF NOT EXISTS issues (
		id SERIAL PRIMARY KEY,
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
		discovered_at TIMESTAMP DEFAULT NOW(),
		updated_at TIMESTAMP DEFAULT NOW(),
		UNIQUE(repo, issue_number)
	);

	CREATE INDEX IF NOT EXISTS idx_issues_status ON issues(status);
	CREATE INDEX IF NOT EXISTS idx_issues_repo ON issues(repo);

	CREATE TABLE IF NOT EXISTS pull_requests (
		id SERIAL PRIMARY KEY,
		issue_id INTEGER NOT NULL REFERENCES issues(id),
		pr_url TEXT NOT NULL,
		pr_number INTEGER,
		branch_name TEXT NOT NULL,
		status TEXT DEFAULT 'open',
		ci_status TEXT DEFAULT 'pending',
		retry_count INTEGER DEFAULT 0,
		merged_at TIMESTAMP,
		closed_at TIMESTAMP,
		review_comment_count INTEGER DEFAULT 0,
		first_review_at TIMESTAMP,
		last_feedback_check_at TIMESTAMP,
		feedback_round INTEGER DEFAULT 0,
		outcome_recorded INTEGER DEFAULT 0,
		created_at TIMESTAMP DEFAULT NOW(),
		updated_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_prs_status ON pull_requests(status);
	CREATE INDEX IF NOT EXISTS idx_prs_merged ON pull_requests(merged_at);

	CREATE TABLE IF NOT EXISTS solve_attempts (
		id SERIAL PRIMARY KEY,
		issue_id INTEGER NOT NULL REFERENCES issues(id),
		attempt_number INTEGER DEFAULT 1,
		started_at TIMESTAMP DEFAULT NOW(),
		completed_at TIMESTAMP,
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
		lines_deleted INTEGER DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_attempts_issue ON solve_attempts(issue_id);
	CREATE INDEX IF NOT EXISTS idx_attempts_started ON solve_attempts(started_at);
	CREATE INDEX IF NOT EXISTS idx_attempts_success ON solve_attempts(success);

	CREATE TABLE IF NOT EXISTS issue_metrics (
		id SERIAL PRIMARY KEY,
		issue_id INTEGER NOT NULL UNIQUE REFERENCES issues(id),
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
		created_at TIMESTAMP DEFAULT NOW(),
		updated_at TIMESTAMP DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS daily_stats (
		id SERIAL PRIMARY KEY,
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
		created_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_daily_date ON daily_stats(date);

	CREATE TABLE IF NOT EXISTS repo_metrics (
		id SERIAL PRIMARY KEY,
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
		created_at TIMESTAMP DEFAULT NOW(),
		updated_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_repo_metrics_repo ON repo_metrics(repo);
	CREATE INDEX IF NOT EXISTS idx_repo_metrics_language ON repo_metrics(language);

	CREATE TABLE IF NOT EXISTS blacklist (
		id SERIAL PRIMARY KEY,
		repo TEXT NOT NULL UNIQUE,
		reason TEXT,
		added_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_blacklist_repo ON blacklist(repo);

	CREATE TABLE IF NOT EXISTS repo_profiles (
		repo TEXT PRIMARY KEY,
		total_prs_submitted INTEGER DEFAULT 0,
		total_merged INTEGER DEFAULT 0,
		total_rejected INTEGER DEFAULT 0,
		merge_rate REAL DEFAULT 0,
		avg_response_time_hours REAL,
		requires_cla BOOLEAN DEFAULT FALSE,
		requires_assignment BOOLEAN DEFAULT FALSE,
		preferred_pr_size TEXT,
		blacklisted BOOLEAN DEFAULT FALSE,
		blacklist_reason TEXT,
		cooldown_until TIMESTAMP,
		strategy_notes TEXT,
		last_interaction TIMESTAMP,
		updated_at TIMESTAMP DEFAULT NOW()
	);
	`
}

func (db *DB) runMigrations() {
	// Migrations for existing DBs (ignore errors for columns that already exist)
	if db.IsPostgres() {
		db.Exec("ALTER TABLE issues ADD COLUMN IF NOT EXISTS retry_count INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS merged_at TIMESTAMP")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS closed_at TIMESTAMP")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS review_comment_count INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS first_review_at TIMESTAMP")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS last_feedback_check_at TIMESTAMP")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS feedback_round INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS outcome_recorded INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS prompt_tokens INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS completion_tokens INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS total_tokens INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS lines_added INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS lines_deleted INTEGER DEFAULT 0")
	} else {
		// SQLite doesn't support IF NOT EXISTS for ALTER TABLE
		db.Exec("ALTER TABLE issues ADD COLUMN retry_count INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN merged_at DATETIME")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN closed_at DATETIME")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN review_comment_count INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN first_review_at DATETIME")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN last_feedback_check_at DATETIME")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN feedback_round INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE pull_requests ADD COLUMN outcome_recorded INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN prompt_tokens INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN completion_tokens INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN total_tokens INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN lines_added INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE solve_attempts ADD COLUMN lines_deleted INTEGER DEFAULT 0")
	}
}

// CreateIssue inserts a new issue
func (db *DB) CreateIssue(issue *models.Issue) error {
	var query string
	if db.IsPostgres() {
		query = `
			INSERT INTO issues (repo, issue_number, title, body, labels, language, difficulty_score, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT(repo, issue_number) DO UPDATE SET
				title = excluded.title,
				body = excluded.body,
				labels = excluded.labels,
				updated_at = CURRENT_TIMESTAMP
		`
	} else {
		query = `
			INSERT INTO issues (repo, issue_number, title, body, labels, language, difficulty_score, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(repo, issue_number) DO UPDATE SET
				title = excluded.title,
				body = excluded.body,
				labels = excluded.labels,
				updated_at = CURRENT_TIMESTAMP
		`
	}

	result, err := db.Exec(query, issue.Repo, issue.IssueNumber, issue.Title, issue.Body, issue.Labels, issue.Language, issue.DifficultyScore, issue.Status)

	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	if id > 0 {
		issue.ID = id
		return nil
	}

	// ON CONFLICT update: LastInsertId is 0, query the real ID
	lookupQuery := fmt.Sprintf(
		`SELECT id FROM issues WHERE repo = %s AND issue_number = %s`,
		db.placeholder(1), db.placeholder(2),
	)
	err = db.QueryRow(lookupQuery, issue.Repo, issue.IssueNumber).Scan(&issue.ID)
	return err
}

// GetIssuesByStatus retrieves issues by status
func (db *DB) GetIssuesByStatus(status models.IssueStatus, limit int) ([]*models.Issue, error) {
	query := fmt.Sprintf(`
		SELECT id, repo, issue_number, title, body, labels, language, difficulty_score, status, error_message, discovered_at, updated_at
		FROM issues
		WHERE status = %s
		ORDER BY difficulty_score ASC
		LIMIT %s
	`, db.placeholder(1), db.placeholder(2))
	rows, err := db.Query(query, status, limit)
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
	query := fmt.Sprintf(`
		UPDATE issues SET status = %s, error_message = %s, updated_at = CURRENT_TIMESTAMP
		WHERE id = %s
	`, db.placeholder(1), db.placeholder(2), db.placeholder(3))
	_, err := db.Exec(query, status, errorMsg, id)
	return err
}

// IncrementIssueRetryCount increments the retry count for an issue
func (db *DB) IncrementIssueRetryCount(id int64) error {
	query := fmt.Sprintf(`
		UPDATE issues SET retry_count = retry_count + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = %s
	`, db.placeholder(1))
	_, err := db.Exec(query, id)
	return err
}

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

// CreateSolveAttempt inserts a new solve attempt
func (db *DB) CreateSolveAttempt(attempt *models.SolveAttempt) error {
	query := fmt.Sprintf(`
		INSERT INTO solve_attempts (issue_id, attempt_number, started_at, prompt_version, model_used)
		VALUES (%s)
	`, db.placeholders(5))

	result, err := db.Exec(query, attempt.IssueID, attempt.AttemptNumber, attempt.StartedAt, attempt.PromptVersion, attempt.ModelUsed)

	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	attempt.ID = id
	return nil
}

// UpdateSolveAttempt updates a solve attempt with results
func (db *DB) UpdateSolveAttempt(attempt *models.SolveAttempt) error {
	query := fmt.Sprintf(`
		UPDATE solve_attempts SET
			completed_at = %s,
			duration_seconds = %s,
			files_changed = %s,
			claude_output_preview = %s,
			fix_complete_marker = %s,
			claude_tests_passed = %s,
			is_complex = %s,
			can_test_locally = %s,
			complexity_reasons = %s,
			external_test_passed = %s,
			test_framework = %s,
			test_duration_seconds = %s,
			test_output_preview = %s,
			success = %s,
			failure_reason = %s,
			error_details = %s
		WHERE id = %s
	`,
		db.placeholder(1), db.placeholder(2), db.placeholder(3), db.placeholder(4),
		db.placeholder(5), db.placeholder(6), db.placeholder(7), db.placeholder(8),
		db.placeholder(9), db.placeholder(10), db.placeholder(11), db.placeholder(12),
		db.placeholder(13), db.placeholder(14), db.placeholder(15), db.placeholder(16),
		db.placeholder(17),
	)
	_, err := db.Exec(query,
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
	query := fmt.Sprintf(`SELECT COUNT(*) FROM solve_attempts WHERE issue_id = %s`, db.placeholder(1))
	err := db.QueryRow(query, issueID).Scan(&count)
	return count, err
}

// GetIssueByID retrieves an issue by its primary key.
func (db *DB) GetIssueByID(id int64) (*models.Issue, error) {
	query := fmt.Sprintf(`
		SELECT id, repo, issue_number, title, COALESCE(body,''), COALESCE(labels,''), COALESCE(language,''),
			   difficulty_score, status, COALESCE(error_message,''), retry_count, discovered_at, updated_at
		FROM issues WHERE id = %s
	`, db.placeholder(1))

	issue := &models.Issue{}
	err := db.QueryRow(query, id).Scan(
		&issue.ID, &issue.Repo, &issue.IssueNumber, &issue.Title, &issue.Body,
		&issue.Labels, &issue.Language, &issue.DifficultyScore, &issue.Status,
		&issue.ErrorMessage, &issue.RetryCount, &issue.DiscoveredAt, &issue.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return issue, nil
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
	// Check if PR already exists by URL
	var prID int64
	query := fmt.Sprintf(`SELECT id FROM pull_requests WHERE pr_url = %s`, db.placeholder(1))
	err := db.QueryRow(query, prURL).Scan(&prID)
	if err == nil {
		return db.getPRByID(prID)
	}

	// Use negative PR number as issue_number to avoid conflicts
	// (real issues have positive numbers, synced PRs use -prNumber)
	issueNumber := -prNumber

	// Create a lightweight issue record
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

	// ON CONFLICT update returns LastInsertId=0 in SQLite, query actual ID
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

// MarkIssueCompleted marks an issue as completed with PR info
func (db *DB) MarkIssueCompleted(issueID int64, prURL string) error {
	query := fmt.Sprintf(`
		UPDATE issues SET status = 'completed', updated_at = CURRENT_TIMESTAMP
		WHERE id = %s
	`, db.placeholder(1))
	_, err := db.Exec(query, issueID)
	return err
}

// MarkIssueFailed marks an issue as failed, optionally for retry
func (db *DB) MarkIssueFailed(issueID int64, errorMsg string, canRetry bool) error {
	status := "failed"
	if canRetry {
		status = "pending" // Put back in queue for retry
	}
	query := fmt.Sprintf(`
		UPDATE issues SET status = %s, error_message = %s, retry_count = retry_count + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = %s
	`, db.placeholder(1), db.placeholder(2), db.placeholder(3))
	_, err := db.Exec(query, status, errorMsg, issueID)
	return err
}

// GetStats returns summary statistics
func (db *DB) GetStats(days int) (map[string]interface{}, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	stats := make(map[string]interface{})

	// Total attempts
	var totalAttempts, successfulAttempts int
	var avgDuration sql.NullFloat64

	query := fmt.Sprintf(`
		SELECT COUNT(*), SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), AVG(duration_seconds)
		FROM solve_attempts
		WHERE started_at >= %s
	`, db.placeholder(1))
	db.QueryRow(query, cutoff).Scan(&totalAttempts, &successfulAttempts, &avgDuration)

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
	db.QueryRow(db.avgHoursQuery("merged_at", "created_at")).Scan(&avgMerge)
	if avgMerge.Valid {
		metrics.AvgTimeToMerge = avgMerge.Float64
	}

	// Average time to first review (in hours)
	var avgReview sql.NullFloat64
	db.QueryRow(db.avgHoursQuery("first_review_at", "created_at")).Scan(&avgReview)
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
		whereClause = fmt.Sprintf("WHERE i.repo = %s", db.placeholder(1))
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

// avgHoursQuery returns a SQL query to compute AVG hours between two timestamp columns.
// SQLite uses julianday(), PostgreSQL uses EXTRACT(EPOCH FROM ...).
func (db *DB) avgHoursQuery(endCol, startCol string) string {
	if db.IsPostgres() {
		return fmt.Sprintf(`
			SELECT AVG(EXTRACT(EPOCH FROM (%s - %s)) / 3600)
			FROM pull_requests WHERE %s IS NOT NULL
		`, endCol, startCol, endCol)
	}
	return fmt.Sprintf(`
		SELECT AVG((julianday(%s) - julianday(%s)) * 24)
		FROM pull_requests WHERE %s IS NOT NULL
	`, endCol, startCol, endCol)
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
	query := fmt.Sprintf(`
		INSERT INTO blacklist (repo, reason) VALUES (%s, %s)
		ON CONFLICT(repo) DO UPDATE SET reason = excluded.reason
	`, db.placeholder(1), db.placeholder(2))
	_, err := db.Exec(query, repo, reason)
	return err
}

// RemoveFromBlacklist removes a repository from the blacklist
func (db *DB) RemoveFromBlacklist(repo string) error {
	query := fmt.Sprintf(`DELETE FROM blacklist WHERE repo = %s`, db.placeholder(1))
	_, err := db.Exec(query, repo)
	return err
}

// IsBlacklisted checks if a repository is blacklisted
func (db *DB) IsBlacklisted(repo string) (bool, error) {
	var count int
	query := fmt.Sprintf(`SELECT COUNT(*) FROM blacklist WHERE repo = %s`, db.placeholder(1))
	err := db.QueryRow(query, repo).Scan(&count)
	return count > 0, err
}

// GetBlacklist returns all blacklisted repositories
func (db *DB) GetBlacklist() ([]*models.BlacklistEntry, error) {
	return db.GetBlacklistFiltered("")
}

// GetBlacklistFiltered returns blacklisted repositories, optionally filtered by a LIKE pattern.
func (db *DB) GetBlacklistFiltered(repoFilter string) ([]*models.BlacklistEntry, error) {
	query := `SELECT id, repo, reason, added_at FROM blacklist`
	whereClause, args := db.repoLikeWhereClause("repo", repoFilter, 1)
	rows, err := db.Query(query+whereClause+` ORDER BY added_at DESC`, args...)
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
