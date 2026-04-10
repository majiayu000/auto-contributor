package db

import (
	"fmt"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// migrateEventsV2 adds the experiences_used column to the pipeline_events table.
// Returns nil if the column already exists (SQLite "duplicate column" error is suppressed).
// All other errors (locked DB, read-only FS, etc.) are propagated so callers know the
// column may be missing and subsequent reads/writes of experiences_used would fail.
func (db *DB) migrateEventsV2() error {
	var stmt string
	if db.IsPostgres() {
		// IF NOT EXISTS is supported by Postgres — never errors.
		stmt = `ALTER TABLE pipeline_events ADD COLUMN IF NOT EXISTS experiences_used TEXT`
	} else {
		stmt = `ALTER TABLE pipeline_events ADD COLUMN experiences_used TEXT`
	}
	_, err := db.Exec(stmt)
	if err != nil && strings.Contains(err.Error(), "duplicate column") {
		return nil // column already exists — idempotent, not an error
	}
	return err
}

// MigrateEvents creates the pipeline_events table.
func (db *DB) MigrateEvents() error {
	var schema string
	if db.IsPostgres() {
		schema = `
		CREATE TABLE IF NOT EXISTS pipeline_events (
			id SERIAL PRIMARY KEY,
			issue_id INTEGER NOT NULL,
			pr_id INTEGER,
			repo TEXT NOT NULL,
			issue_number INTEGER NOT NULL,
			stage TEXT NOT NULL,
			round INTEGER DEFAULT 1,
			started_at TIMESTAMP NOT NULL,
			completed_at TIMESTAMP,
			duration_seconds REAL,
			output_summary TEXT,
			verdict TEXT,
			success INTEGER DEFAULT 0,
			error_message TEXT,
			outcome_label TEXT,
			experiences_used TEXT,
			created_at TIMESTAMP DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_events_issue ON pipeline_events(issue_id);
		CREATE INDEX IF NOT EXISTS idx_events_stage ON pipeline_events(stage);
		CREATE INDEX IF NOT EXISTS idx_events_repo ON pipeline_events(repo);
		CREATE INDEX IF NOT EXISTS idx_events_outcome ON pipeline_events(outcome_label);
		`
	} else {
		schema = `
		CREATE TABLE IF NOT EXISTS pipeline_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id INTEGER NOT NULL,
			pr_id INTEGER,
			repo TEXT NOT NULL,
			issue_number INTEGER NOT NULL,
			stage TEXT NOT NULL,
			round INTEGER DEFAULT 1,
			started_at DATETIME NOT NULL,
			completed_at DATETIME,
			duration_seconds REAL,
			output_summary TEXT,
			verdict TEXT,
			success INTEGER DEFAULT 0,
			error_message TEXT,
			outcome_label TEXT,
			experiences_used TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_events_issue ON pipeline_events(issue_id);
		CREATE INDEX IF NOT EXISTS idx_events_stage ON pipeline_events(stage);
		CREATE INDEX IF NOT EXISTS idx_events_repo ON pipeline_events(repo);
		CREATE INDEX IF NOT EXISTS idx_events_outcome ON pipeline_events(outcome_label);
		`
	}
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	// Add experiences_used column to existing databases (idempotent).
	if err := db.migrateEventsV2(); err != nil {
		return fmt.Errorf("migrateEventsV2: %w", err)
	}
	return nil
}

// RecordEvent inserts a pipeline event.
func (db *DB) RecordEvent(event *models.PipelineEvent) error {
	query := fmt.Sprintf(`
		INSERT INTO pipeline_events (issue_id, pr_id, repo, issue_number, stage, round,
			started_at, completed_at, duration_seconds, output_summary, verdict,
			success, error_message, experiences_used)
		VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
	`,
		db.placeholder(1), db.placeholder(2), db.placeholder(3), db.placeholder(4),
		db.placeholder(5), db.placeholder(6), db.placeholder(7), db.placeholder(8),
		db.placeholder(9), db.placeholder(10), db.placeholder(11), db.placeholder(12),
		db.placeholder(13), db.placeholder(14),
	)

	successInt := 0
	if event.Success {
		successInt = 1
	}

	_, err := db.Exec(query,
		event.IssueID, event.PRID, event.Repo, event.IssueNumber,
		event.Stage, event.Round, event.StartedAt, event.CompletedAt,
		event.DurationSeconds, event.OutputSummary, event.Verdict,
		successInt, event.ErrorMessage, event.ExperiencesUsed,
	)
	return err
}

// GetEventsByIssue returns all events for an issue, ordered chronologically.
func (db *DB) GetEventsByIssue(issueID int64) ([]*models.PipelineEvent, error) {
	query := fmt.Sprintf(`
		SELECT id, issue_id, pr_id, repo, issue_number, stage, round,
			started_at, completed_at, duration_seconds, output_summary, verdict,
			success, error_message, outcome_label, experiences_used, created_at
		FROM pipeline_events
		WHERE issue_id = %s
		ORDER BY created_at ASC
	`, db.placeholder(1))

	rows, err := db.Query(query, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEvents(rows)
}

// GetEventsByStage returns recent events for a stage.
func (db *DB) GetEventsByStage(stage string, limit int) ([]*models.PipelineEvent, error) {
	query := fmt.Sprintf(`
		SELECT id, issue_id, pr_id, repo, issue_number, stage, round,
			started_at, completed_at, duration_seconds, output_summary, verdict,
			success, error_message, outcome_label, experiences_used, created_at
		FROM pipeline_events
		WHERE stage = %s
		ORDER BY created_at DESC
		LIMIT %s
	`, db.placeholder(1), db.placeholder(2))

	rows, err := db.Query(query, stage, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEvents(rows)
}

// GetLabeledEventsByStage returns events with outcome labels for synthesis.
func (db *DB) GetLabeledEventsByStage(stage string, limit int) ([]*models.PipelineEvent, error) {
	query := fmt.Sprintf(`
		SELECT id, issue_id, pr_id, repo, issue_number, stage, round,
			started_at, completed_at, duration_seconds, output_summary, verdict,
			success, error_message, outcome_label, experiences_used, created_at
		FROM pipeline_events
		WHERE stage = %s AND outcome_label IS NOT NULL AND outcome_label != ''
		ORDER BY created_at DESC
		LIMIT %s
	`, db.placeholder(1), db.placeholder(2))

	rows, err := db.Query(query, stage, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEvents(rows)
}

// LabelEventsByIssue sets the outcome_label on all events for an issue.
func (db *DB) LabelEventsByIssue(issueID int64, label string) error {
	query := fmt.Sprintf(`
		UPDATE pipeline_events SET outcome_label = %s WHERE issue_id = %s
	`, db.placeholder(1), db.placeholder(2))
	_, err := db.Exec(query, label, issueID)
	return err
}

// LabelEventsByPR sets the outcome_label on all events associated with a PR.
func (db *DB) LabelEventsByPR(prID int64, label string) error {
	query := fmt.Sprintf(`
		UPDATE pipeline_events SET outcome_label = %s WHERE pr_id = %s
	`, db.placeholder(1), db.placeholder(2))
	_, err := db.Exec(query, label, prID)
	return err
}

func scanEvents(rows interface {
	Next() bool
	Scan(...any) error
}) ([]*models.PipelineEvent, error) {
	var events []*models.PipelineEvent
	for rows.Next() {
		e := &models.PipelineEvent{}
		var completedAt *time.Time
		var prID *int64
		var outcomeLabel *string
		var experiencesUsed *string
		err := rows.Scan(
			&e.ID, &e.IssueID, &prID, &e.Repo, &e.IssueNumber,
			&e.Stage, &e.Round, &e.StartedAt, &completedAt,
			&e.DurationSeconds, &e.OutputSummary, &e.Verdict,
			&e.Success, &e.ErrorMessage, &outcomeLabel, &experiencesUsed, &e.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		e.PRID = prID
		e.CompletedAt = completedAt
		if outcomeLabel != nil {
			e.OutcomeLabel = *outcomeLabel
		}
		if experiencesUsed != nil {
			e.ExperiencesUsed = *experiencesUsed
		}
		events = append(events, e)
	}
	return events, nil
}
