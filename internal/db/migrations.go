package db

import (
	"fmt"
	"strings"
)

type migrationStatement struct {
	name  string
	query string
}

var postgresColumnMigrations = []migrationStatement{
	{name: "issues.retry_count", query: "ALTER TABLE issues ADD COLUMN IF NOT EXISTS retry_count INTEGER DEFAULT 0"},
	{name: "pull_requests.merged_at", query: "ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS merged_at TIMESTAMP"},
	{name: "pull_requests.closed_at", query: "ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS closed_at TIMESTAMP"},
	{name: "pull_requests.review_comment_count", query: "ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS review_comment_count INTEGER DEFAULT 0"},
	{name: "pull_requests.first_review_at", query: "ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS first_review_at TIMESTAMP"},
	{name: "pull_requests.last_feedback_check_at", query: "ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS last_feedback_check_at TIMESTAMP"},
	{name: "pull_requests.feedback_round", query: "ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS feedback_round INTEGER DEFAULT 0"},
	{name: "pull_requests.outcome_recorded", query: "ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS outcome_recorded INTEGER DEFAULT 0"},
	{name: "solve_attempts.prompt_tokens", query: "ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS prompt_tokens INTEGER DEFAULT 0"},
	{name: "solve_attempts.completion_tokens", query: "ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS completion_tokens INTEGER DEFAULT 0"},
	{name: "solve_attempts.total_tokens", query: "ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS total_tokens INTEGER DEFAULT 0"},
	{name: "solve_attempts.lines_added", query: "ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS lines_added INTEGER DEFAULT 0"},
	{name: "solve_attempts.lines_deleted", query: "ALTER TABLE solve_attempts ADD COLUMN IF NOT EXISTS lines_deleted INTEGER DEFAULT 0"},
}

var sqliteColumnMigrations = []migrationStatement{
	{name: "issues.retry_count", query: "ALTER TABLE issues ADD COLUMN retry_count INTEGER DEFAULT 0"},
	{name: "pull_requests.merged_at", query: "ALTER TABLE pull_requests ADD COLUMN merged_at DATETIME"},
	{name: "pull_requests.closed_at", query: "ALTER TABLE pull_requests ADD COLUMN closed_at DATETIME"},
	{name: "pull_requests.review_comment_count", query: "ALTER TABLE pull_requests ADD COLUMN review_comment_count INTEGER DEFAULT 0"},
	{name: "pull_requests.first_review_at", query: "ALTER TABLE pull_requests ADD COLUMN first_review_at DATETIME"},
	{name: "pull_requests.last_feedback_check_at", query: "ALTER TABLE pull_requests ADD COLUMN last_feedback_check_at DATETIME"},
	{name: "pull_requests.feedback_round", query: "ALTER TABLE pull_requests ADD COLUMN feedback_round INTEGER DEFAULT 0"},
	{name: "pull_requests.outcome_recorded", query: "ALTER TABLE pull_requests ADD COLUMN outcome_recorded INTEGER DEFAULT 0"},
	{name: "solve_attempts.prompt_tokens", query: "ALTER TABLE solve_attempts ADD COLUMN prompt_tokens INTEGER DEFAULT 0"},
	{name: "solve_attempts.completion_tokens", query: "ALTER TABLE solve_attempts ADD COLUMN completion_tokens INTEGER DEFAULT 0"},
	{name: "solve_attempts.total_tokens", query: "ALTER TABLE solve_attempts ADD COLUMN total_tokens INTEGER DEFAULT 0"},
	{name: "solve_attempts.lines_added", query: "ALTER TABLE solve_attempts ADD COLUMN lines_added INTEGER DEFAULT 0"},
	{name: "solve_attempts.lines_deleted", query: "ALTER TABLE solve_attempts ADD COLUMN lines_deleted INTEGER DEFAULT 0"},
}

func (db *DB) runMigrations() error {
	migrations := sqliteColumnMigrations
	if db.IsPostgres() {
		migrations = postgresColumnMigrations
	}

	for _, migration := range migrations {
		if _, err := db.Exec(migration.query); err != nil {
			if !db.IsPostgres() && isSQLiteDuplicateColumnError(err) {
				continue
			}
			return fmt.Errorf("%s: %w", migration.name, err)
		}
	}
	return nil
}

func isSQLiteDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}
