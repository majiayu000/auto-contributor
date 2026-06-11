package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
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

	if err := db.runMigrations(); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	if err := db.MigrateLessons(); err != nil {
		return fmt.Errorf("migrate lessons: %w", err)
	}
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
	if err := db.MigrateRuleEmbeddings(); err != nil {
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
