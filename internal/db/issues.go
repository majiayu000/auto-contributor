package db

import (
	"fmt"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

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
