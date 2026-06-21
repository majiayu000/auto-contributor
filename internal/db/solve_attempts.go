package db

import (
	"fmt"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

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
