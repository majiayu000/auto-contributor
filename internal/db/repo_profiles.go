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
func (db *DB) RecordPROutcome(repo string, merged bool, responseHours float64) error {
	profile, err := db.GetRepoProfile(repo)
	if err != nil {
		return fmt.Errorf("get repo profile: %w", err)
	}
	now := time.Now()
	if profile == nil {
		profile = &models.RepoProfile{Repo: repo}
	}

	profile.TotalPRsSubmitted++
	if merged {
		profile.TotalMerged++
	} else {
		profile.TotalRejected++
	}
	if profile.TotalPRsSubmitted > 0 {
		profile.MergeRate = float64(profile.TotalMerged) / float64(profile.TotalPRsSubmitted)
	}
	if responseHours > 0 {
		if profile.AvgResponseTimeHours == nil {
			profile.AvgResponseTimeHours = &responseHours
		} else {
			avg := (*profile.AvgResponseTimeHours*float64(profile.TotalPRsSubmitted-1) + responseHours) / float64(profile.TotalPRsSubmitted)
			profile.AvgResponseTimeHours = &avg
		}
	}
	profile.LastInteraction = &now

	return db.UpsertRepoProfile(profile)
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
