package db

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// MigrateTrajectories creates the trajectories table for experience replay.
// issue_id is intentionally NOT unique: multiple attempts (retries, separate PRs)
// for the same issue are stored as separate rows so no history is overwritten.
// Returns an error if any DDL step fails so the caller can abort startup.
func (db *DB) MigrateTrajectories() error {
	if db.IsPostgres() {
		if _, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS trajectories (
				id SERIAL PRIMARY KEY,
				issue_id INTEGER NOT NULL,
				pr_number INTEGER NOT NULL DEFAULT 0,
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
			)`); err != nil {
			return fmt.Errorf("create trajectories table: %w", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_issue ON trajectories(issue_id)`); err != nil {
			return fmt.Errorf("create idx_trajectories_issue: %w", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_outcome ON trajectories(outcome_label)`); err != nil {
			return fmt.Errorf("create idx_trajectories_outcome: %w", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_success ON trajectories(success)`); err != nil {
			return fmt.Errorf("create idx_trajectories_success: %w", err)
		}
		// Drop the old unique index if it exists (created by earlier schema versions).
		if _, err := db.Exec(`DROP INDEX IF EXISTS idx_trajectories_issue_unique`); err != nil {
			return fmt.Errorf("drop idx_trajectories_issue_unique: %w", err)
		}
		// Add pr_number column to existing tables; IF NOT EXISTS makes this idempotent.
		if _, err := db.Exec(`ALTER TABLE trajectories ADD COLUMN IF NOT EXISTS pr_number INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add pr_number column: %w", err)
		}
	} else {
		if _, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS trajectories (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id INTEGER NOT NULL,
				pr_number INTEGER NOT NULL DEFAULT 0,
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
			)`); err != nil {
			return fmt.Errorf("create trajectories table: %w", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_issue ON trajectories(issue_id)`); err != nil {
			return fmt.Errorf("create idx_trajectories_issue: %w", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_outcome ON trajectories(outcome_label)`); err != nil {
			return fmt.Errorf("create idx_trajectories_outcome: %w", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_success ON trajectories(success)`); err != nil {
			return fmt.Errorf("create idx_trajectories_success: %w", err)
		}
		// Drop the old named unique index if it exists (created by earlier schema versions).
		db.Exec(`DROP INDEX IF EXISTS idx_trajectories_issue_unique`)
		// Add pr_number column to existing SQLite tables; "duplicate column name" means already present.
		if _, err := db.Exec(`ALTER TABLE trajectories ADD COLUMN pr_number INTEGER NOT NULL DEFAULT 0`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("add pr_number column: %w", err)
			}
		}
		// Fix inline `issue_id UNIQUE` constraint from older schema versions.
		// SQLite cannot remove a column-level constraint via ALTER TABLE; the table must be recreated.
		// We first ensure pr_number exists (above), then check the stored DDL and recreate if needed.
		var oldSQL string
		if err := db.QueryRow(
			`SELECT sql FROM sqlite_master WHERE type='table' AND name='trajectories'`,
		).Scan(&oldSQL); err == nil {
			if regexp.MustCompile(`(?i)\bissue_id\b[^,\n)]*\bUNIQUE\b`).MatchString(oldSQL) {
				if err := db.rebuildTrajectoriesTable(); err != nil {
					return fmt.Errorf("rebuild trajectories table: %w", err)
				}
			}
		}
	}
	return nil
}

// rebuildTrajectoriesTable recreates the trajectories table inside a transaction
// to remove any legacy column-level UNIQUE constraint on issue_id.
// The transaction ensures the original table is never dropped unless the data
// copy succeeds, preventing irreversible data loss on failure.
func (db *DB) rebuildTrajectoriesTable() error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback() //nolint:errcheck
		}
	}()

	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS trajectories_new (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		issue_id INTEGER NOT NULL,
		pr_number INTEGER NOT NULL DEFAULT 0,
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
	)`); err != nil {
		return fmt.Errorf("create trajectories_new: %w", err)
	}

	if _, err = tx.Exec(`INSERT INTO trajectories_new
		SELECT id, issue_id, pr_number, repo, issue_number, issue_title, issue_body,
			keywords, scout_verdict, scout_approach, analyst_plan, review_rounds,
			review_summary, outcome_label, success, created_at, updated_at
		FROM trajectories`); err != nil {
		return fmt.Errorf("copy trajectories data: %w", err)
	}

	if _, err = tx.Exec(`DROP TABLE trajectories`); err != nil {
		return fmt.Errorf("drop old trajectories: %w", err)
	}

	if _, err = tx.Exec(`ALTER TABLE trajectories_new RENAME TO trajectories`); err != nil {
		return fmt.Errorf("rename trajectories_new: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit trajectories rebuild: %w", err)
	}

	// Rebuild indexes after table recreation (outside transaction; non-fatal if they already exist).
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_issue ON trajectories(issue_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_outcome ON trajectories(outcome_label)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_trajectories_success ON trajectories(success)`)
	return nil
}

// SaveTrajectory inserts a new trajectory record for this attempt.
// Each call always creates a new row so that retries and multiple PR attempts
// for the same issue are preserved independently (no overwrite).
func (db *DB) SaveTrajectory(t *models.Trajectory) error {
	successInt := 0
	if t.Success {
		successInt = 1
	}
	now := time.Now()

	if db.IsPostgres() {
		query := `
			INSERT INTO trajectories
				(issue_id, pr_number, repo, issue_number, issue_title, issue_body, keywords,
				 scout_verdict, scout_approach, analyst_plan, review_rounds,
				 review_summary, outcome_label, success, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`
		_, err := db.Exec(query,
			t.IssueID, t.PRNumber, t.Repo, t.IssueNumber, t.IssueTitle, t.IssueBody,
			t.Keywords, t.ScoutVerdict, t.ScoutApproach, t.AnalystPlan,
			t.ReviewRounds, t.ReviewSummary, t.OutcomeLabel, successInt, now, now,
		)
		return err
	}

	query := `
		INSERT INTO trajectories
			(issue_id, pr_number, repo, issue_number, issue_title, issue_body, keywords,
			 scout_verdict, scout_approach, analyst_plan, review_rounds,
			 review_summary, outcome_label, success, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := db.Exec(query,
		t.IssueID, t.PRNumber, t.Repo, t.IssueNumber, t.IssueTitle, t.IssueBody,
		t.Keywords, t.ScoutVerdict, t.ScoutApproach, t.AnalystPlan,
		t.ReviewRounds, t.ReviewSummary, t.OutcomeLabel, successInt, now, now,
	)
	return err
}

// UpdateTrajectoryOutcome sets the outcome label and success flag on the trajectory row
// that matches both issueID and prNumber. When prNumber > 0 the update is scoped to
// the exact PR, preventing an older PR closure from overwriting a newer trajectory's outcome.
// When prNumber == 0 (no PR was created) we fall back to the most-recent row for the issue.
func (db *DB) UpdateTrajectoryOutcome(issueID int64, prNumber int, outcomeLabel string, success bool) error {
	successInt := 0
	if success {
		successInt = 1
	}
	now := time.Now()
	var query string
	if prNumber > 0 {
		if db.IsPostgres() {
			query = `UPDATE trajectories SET outcome_label = $1, success = $2, updated_at = $3
				WHERE issue_id = $4 AND pr_number = $5`
		} else {
			query = `UPDATE trajectories SET outcome_label = ?, success = ?, updated_at = ?
				WHERE issue_id = ? AND pr_number = ?`
		}
		result, err := db.Exec(query, outcomeLabel, successInt, now, issueID, prNumber)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("no trajectory row found for issue_id=%d pr_number=%d", issueID, prNumber)
		}
		return nil
	}
	// Fallback: no PR number available — update the most-recent row for this issue.
	if db.IsPostgres() {
		query = `UPDATE trajectories SET outcome_label = $1, success = $2, updated_at = $3
			WHERE id = (SELECT id FROM trajectories WHERE issue_id = $4 ORDER BY created_at DESC LIMIT 1)`
	} else {
		query = `UPDATE trajectories SET outcome_label = ?, success = ?, updated_at = ?
			WHERE id = (SELECT id FROM trajectories WHERE issue_id = ? ORDER BY created_at DESC LIMIT 1)`
	}
	result, err := db.Exec(query, outcomeLabel, successInt, now, issueID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("no trajectory row found for issue_id=%d", issueID)
	}
	return nil
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
	Err() error
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return trajectories, nil
}
