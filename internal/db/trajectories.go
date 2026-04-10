package db

import (
	"fmt"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// MigrateTrajectories creates the trajectories table for experience replay.
func (db *DB) MigrateTrajectories() {
	if db.IsPostgres() {
		db.Exec(`
			CREATE TABLE IF NOT EXISTS trajectories (
				id SERIAL PRIMARY KEY,
				issue_id INTEGER NOT NULL,
				repo TEXT NOT NULL,
				issue_number INTEGER NOT NULL,
				issue_title TEXT NOT NULL,
				issue_body TEXT,
				keywords TEXT,
				scout_verdict TEXT,
				scout_approach TEXT,
				analyst_plan TEXT,
				review_rounds INTEGER DEFAULT 0,
				review_summary TEXT,
				outcome_label TEXT,
				success INTEGER DEFAULT 0,
				created_at TIMESTAMP DEFAULT NOW(),
				updated_at TIMESTAMP DEFAULT NOW()
			)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_issue ON trajectories(issue_id)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_outcome ON trajectories(outcome_label)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_success ON trajectories(success)`)
		db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_trajectories_issue_unique ON trajectories(issue_id)`)
	} else {
		db.Exec(`
			CREATE TABLE IF NOT EXISTS trajectories (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id INTEGER NOT NULL UNIQUE,
				repo TEXT NOT NULL,
				issue_number INTEGER NOT NULL,
				issue_title TEXT NOT NULL,
				issue_body TEXT,
				keywords TEXT,
				scout_verdict TEXT,
				scout_approach TEXT,
				analyst_plan TEXT,
				review_rounds INTEGER DEFAULT 0,
				review_summary TEXT,
				outcome_label TEXT,
				success INTEGER DEFAULT 0,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_outcome ON trajectories(outcome_label)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_success ON trajectories(success)`)
	}
}

// SaveTrajectory inserts or replaces a trajectory record.
func (db *DB) SaveTrajectory(t *models.Trajectory) error {
	successInt := 0
	if t.Success {
		successInt = 1
	}
	now := time.Now()

	if db.IsPostgres() {
		query := `
			INSERT INTO trajectories
				(issue_id, repo, issue_number, issue_title, issue_body, keywords,
				 scout_verdict, scout_approach, analyst_plan, review_rounds,
				 review_summary, outcome_label, success, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (issue_id) DO UPDATE SET
				keywords = EXCLUDED.keywords,
				scout_verdict = EXCLUDED.scout_verdict,
				scout_approach = EXCLUDED.scout_approach,
				analyst_plan = EXCLUDED.analyst_plan,
				review_rounds = EXCLUDED.review_rounds,
				review_summary = EXCLUDED.review_summary,
				outcome_label = EXCLUDED.outcome_label,
				success = EXCLUDED.success,
				updated_at = EXCLUDED.updated_at`
		_, err := db.Exec(query,
			t.IssueID, t.Repo, t.IssueNumber, t.IssueTitle, t.IssueBody,
			t.Keywords, t.ScoutVerdict, t.ScoutApproach, t.AnalystPlan,
			t.ReviewRounds, t.ReviewSummary, t.OutcomeLabel, successInt, now, now,
		)
		return err
	}

	// SQLite: use INSERT OR REPLACE
	query := `
		INSERT OR REPLACE INTO trajectories
			(issue_id, repo, issue_number, issue_title, issue_body, keywords,
			 scout_verdict, scout_approach, analyst_plan, review_rounds,
			 review_summary, outcome_label, success, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := db.Exec(query,
		t.IssueID, t.Repo, t.IssueNumber, t.IssueTitle, t.IssueBody,
		t.Keywords, t.ScoutVerdict, t.ScoutApproach, t.AnalystPlan,
		t.ReviewRounds, t.ReviewSummary, t.OutcomeLabel, successInt, now, now,
	)
	return err
}

// UpdateTrajectoryOutcome sets the outcome label and success flag for a trajectory.
func (db *DB) UpdateTrajectoryOutcome(issueID int64, outcomeLabel string, success bool) error {
	successInt := 0
	if success {
		successInt = 1
	}
	query := fmt.Sprintf(`
		UPDATE trajectories
		SET outcome_label = %s, success = %s, updated_at = %s
		WHERE issue_id = %s`,
		db.placeholder(1), db.placeholder(2), db.placeholder(3), db.placeholder(4),
	)
	_, err := db.Exec(query, outcomeLabel, successInt, time.Now(), issueID)
	return err
}

// GetSuccessfulTrajectories returns recent successful trajectories for experience replay.
func (db *DB) GetSuccessfulTrajectories(limit int) ([]*models.Trajectory, error) {
	query := fmt.Sprintf(`
		SELECT id, issue_id, repo, issue_number, issue_title, issue_body,
			COALESCE(keywords,''), COALESCE(scout_verdict,''), COALESCE(scout_approach,''),
			COALESCE(analyst_plan,''), review_rounds, COALESCE(review_summary,''),
			COALESCE(outcome_label,''), success, created_at, updated_at
		FROM trajectories
		WHERE success = 1
		ORDER BY updated_at DESC
		LIMIT %s`,
		db.placeholder(1),
	)
	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrajectories(rows)
}

// GetRecentTrajectories returns recent trajectories regardless of outcome.
func (db *DB) GetRecentTrajectories(limit int) ([]*models.Trajectory, error) {
	query := fmt.Sprintf(`
		SELECT id, issue_id, repo, issue_number, issue_title, issue_body,
			COALESCE(keywords,''), COALESCE(scout_verdict,''), COALESCE(scout_approach,''),
			COALESCE(analyst_plan,''), review_rounds, COALESCE(review_summary,''),
			COALESCE(outcome_label,''), success, created_at, updated_at
		FROM trajectories
		ORDER BY updated_at DESC
		LIMIT %s`,
		db.placeholder(1),
	)
	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrajectories(rows)
}

func scanTrajectories(rows interface {
	Next() bool
	Scan(...any) error
}) ([]*models.Trajectory, error) {
	var trajectories []*models.Trajectory
	for rows.Next() {
		t := &models.Trajectory{}
		var successInt int
		err := rows.Scan(
			&t.ID, &t.IssueID, &t.Repo, &t.IssueNumber, &t.IssueTitle, &t.IssueBody,
			&t.Keywords, &t.ScoutVerdict, &t.ScoutApproach, &t.AnalystPlan,
			&t.ReviewRounds, &t.ReviewSummary, &t.OutcomeLabel, &successInt,
			&t.CreatedAt, &t.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		t.Success = successInt != 0
		trajectories = append(trajectories, t)
	}
	return trajectories, nil
}
