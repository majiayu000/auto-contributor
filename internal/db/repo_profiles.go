package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// MigrateRepoProfiles ensures the repo_profiles table exists for existing DBs.
func (db *DB) MigrateRepoProfiles() {
	// Table is created in the main schema; this handles pre-existing databases.
	if db.IsPostgres() {
		db.Exec(`CREATE TABLE IF NOT EXISTS repo_profiles (
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
		)`)
	} else {
		db.Exec(`CREATE TABLE IF NOT EXISTS repo_profiles (
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
		)`)
	}
}

// GetRepoProfile returns the profile for a repo, or nil if not found.
func (db *DB) GetRepoProfile(repo string) (*models.RepoProfile, error) {
	query := fmt.Sprintf(`
		SELECT repo, total_prs_submitted, total_merged, total_rejected, merge_rate,
		       avg_response_time_hours, requires_cla, requires_assignment, preferred_pr_size,
		       blacklisted, blacklist_reason, cooldown_until, strategy_notes, last_interaction,
		       updated_at
		FROM repo_profiles WHERE repo = %s
	`, db.placeholder(1))

	row := db.QueryRow(query, repo)
	p := &models.RepoProfile{}
	var (
		avgResp      sql.NullFloat64
		preferredSz  sql.NullString
		blackReason  sql.NullString
		cooldown     sql.NullTime
		stratNotes   sql.NullString
		lastInteract sql.NullTime
	)
	err := row.Scan(
		&p.Repo, &p.TotalPRsSubmitted, &p.TotalMerged, &p.TotalRejected, &p.MergeRate,
		&avgResp, &p.RequiresCLA, &p.RequiresAssignment, &preferredSz,
		&p.Blacklisted, &blackReason, &cooldown, &stratNotes, &lastInteract,
		&p.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if avgResp.Valid {
		p.AvgResponseTimeHours = &avgResp.Float64
	}
	if preferredSz.Valid {
		p.PreferredPRSize = preferredSz.String
	}
	if blackReason.Valid {
		p.BlacklistReason = blackReason.String
	}
	if cooldown.Valid {
		p.CooldownUntil = &cooldown.Time
	}
	if stratNotes.Valid {
		p.StrategyNotes = stratNotes.String
	}
	if lastInteract.Valid {
		p.LastInteraction = &lastInteract.Time
	}
	return p, nil
}

// UpsertRepoProfile inserts or updates a repo profile.
func (db *DB) UpsertRepoProfile(p *models.RepoProfile) error {
	now := time.Now()
	query := fmt.Sprintf(`
		INSERT INTO repo_profiles (
			repo, total_prs_submitted, total_merged, total_rejected, merge_rate,
			avg_response_time_hours, requires_cla, requires_assignment, preferred_pr_size,
			blacklisted, blacklist_reason, cooldown_until, strategy_notes, last_interaction,
			updated_at
		) VALUES (%s)
		ON CONFLICT(repo) DO UPDATE SET
			total_prs_submitted = excluded.total_prs_submitted,
			total_merged        = excluded.total_merged,
			total_rejected      = excluded.total_rejected,
			merge_rate          = excluded.merge_rate,
			avg_response_time_hours = excluded.avg_response_time_hours,
			requires_cla        = excluded.requires_cla,
			requires_assignment = excluded.requires_assignment,
			preferred_pr_size   = excluded.preferred_pr_size,
			blacklisted         = excluded.blacklisted,
			blacklist_reason    = excluded.blacklist_reason,
			cooldown_until      = excluded.cooldown_until,
			strategy_notes      = excluded.strategy_notes,
			last_interaction    = excluded.last_interaction,
			updated_at          = excluded.updated_at
	`, db.placeholders(15))

	_, err := db.Exec(query,
		p.Repo, p.TotalPRsSubmitted, p.TotalMerged, p.TotalRejected, p.MergeRate,
		p.AvgResponseTimeHours, p.RequiresCLA, p.RequiresAssignment, p.PreferredPRSize,
		p.Blacklisted, p.BlacklistReason, p.CooldownUntil, p.StrategyNotes, p.LastInteraction,
		now,
	)
	return err
}

// RecordPROutcome updates the repo profile after a PR reaches a terminal state.
// merged=true means the PR was merged; merged=false means it was closed/rejected.
// responseHours is the time from PR creation to the terminal event (may be 0 if unknown).
//
// Idempotency: the outcome_recorded flag on the pull_requests row is set atomically
// before touching repo_profiles. If called twice for the same PR (parallel runners,
// overlapping loop executions), the second call is a no-op. If the profile update
// fails after the flag is set, the flag is reset so the next cycle can retry.
func (db *DB) RecordPROutcome(prID int64, repo string, merged bool, responseHours float64) error {
	// Atomically claim the right to record this outcome. If outcome_recorded is
	// already 1 (set by a prior call), affected rows will be 0 → skip.
	claimQ := fmt.Sprintf(
		`UPDATE pull_requests SET outcome_recorded = 1 WHERE id = %s AND outcome_recorded = 0`,
		db.placeholder(1),
	)
	res, err := db.Exec(claimQ, prID)
	if err != nil {
		return fmt.Errorf("claim outcome_recorded for pr %d: %w", prID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for pr %d: %w", prID, err)
	}
	if affected == 0 {
		// Already recorded by a previous run — idempotent skip.
		return nil
	}

	now := time.Now()
	var mergedInc, rejectedInc int
	if merged {
		mergedInc = 1
	} else {
		rejectedInc = 1
	}
	mergeRate := float64(mergedInc) // merge_rate for a brand-new row (1 PR total)

	// Single atomic upsert: increment counters and recompute merge_rate in-database.
	// Using excluded.* to reference the INSERT values inside ON CONFLICT DO UPDATE.
	profileQ := fmt.Sprintf(`
		INSERT INTO repo_profiles (
			repo, total_prs_submitted, total_merged, total_rejected,
			merge_rate, last_interaction, updated_at
		) VALUES (%s, 1, %s, %s, %s, %s, %s)
		ON CONFLICT(repo) DO UPDATE SET
			total_prs_submitted = repo_profiles.total_prs_submitted + 1,
			total_merged        = repo_profiles.total_merged + excluded.total_merged,
			total_rejected      = repo_profiles.total_rejected + excluded.total_rejected,
			merge_rate          = CAST(repo_profiles.total_merged + excluded.total_merged AS REAL)
			                      / (repo_profiles.total_prs_submitted + 1),
			last_interaction    = excluded.last_interaction,
			updated_at          = excluded.updated_at`,
		db.placeholder(1), // repo
		db.placeholder(2), // mergedInc
		db.placeholder(3), // rejectedInc
		db.placeholder(4), // mergeRate (initial row only)
		db.placeholder(5), // now (last_interaction)
		db.placeholder(6), // now (updated_at)
	)
	if _, err := db.Exec(profileQ, repo, mergedInc, rejectedInc, mergeRate, now, now); err != nil {
		// Reset the flag so the next feedback cycle can retry.
		resetQ := fmt.Sprintf(
			`UPDATE pull_requests SET outcome_recorded = 0 WHERE id = %s`,
			db.placeholder(1),
		)
		db.Exec(resetQ, prID) //nolint:errcheck — best-effort reset; original error takes priority
		return fmt.Errorf("record PR outcome: %w", err)
	}

	// Update running average response time separately (best-effort; not used in
	// skip-repo decisions, so a non-atomic update here is acceptable).
	if responseHours > 0 {
		if err := db.updateAvgResponseTime(repo, responseHours); err != nil {
			// Non-fatal: avg_response_time_hours is informational only.
			fmt.Printf("warn: updateAvgResponseTime for %s: %v\n", repo, err)
		}
	}

	return nil
}

// updateAvgResponseTime updates the running average response time for a repo.
// Called after the counter upsert, so total_prs_submitted already reflects the new PR.
func (db *DB) updateAvgResponseTime(repo string, responseHours float64) error {
	query := fmt.Sprintf(`
		UPDATE repo_profiles
		SET avg_response_time_hours = CASE
			WHEN avg_response_time_hours IS NULL THEN %s
			ELSE (avg_response_time_hours * (total_prs_submitted - 1) + %s) / total_prs_submitted
		END,
		updated_at = %s
		WHERE repo = %s`,
		db.placeholder(1),
		db.placeholder(2),
		db.placeholder(3),
		db.placeholder(4),
	)
	_, err := db.Exec(query, responseHours, responseHours, time.Now(), repo)
	return err
}

// ShouldSkipRepo returns true when a repo's profile indicates it is not worth
// attempting: blacklisted, in cooldown, or has a low merge rate with enough
// data to be statistically meaningful (>= minSamples submitted PRs).
func (db *DB) ShouldSkipRepo(repo string, mergeRateThreshold float64, minSamples int) (bool, string, error) {
	profile, err := db.GetRepoProfile(repo)
	if err != nil {
		return false, "", fmt.Errorf("get repo profile: %w", err)
	}
	if profile == nil {
		return false, "", nil
	}
	if profile.Blacklisted {
		reason := profile.BlacklistReason
		if reason == "" {
			reason = "blacklisted in repo_profiles"
		}
		return true, reason, nil
	}
	if profile.CooldownUntil != nil && time.Now().Before(*profile.CooldownUntil) {
		return true, fmt.Sprintf("in cooldown until %s", profile.CooldownUntil.Format(time.RFC3339)), nil
	}
	if profile.TotalPRsSubmitted >= minSamples && profile.MergeRate < mergeRateThreshold {
		return true, fmt.Sprintf("low merge rate %.0f%% (%d/%d PRs)",
			profile.MergeRate*100, profile.TotalMerged, profile.TotalPRsSubmitted), nil
	}
	return false, "", nil
}
