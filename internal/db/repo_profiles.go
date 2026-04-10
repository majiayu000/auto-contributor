package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// UpsertRepoProfile creates or fully replaces a repo profile record.
// Use this for manually managed fields (RequiresCLA, StrategyNotes, CooldownUntil, etc.).
// Stats fields are overwritten too, so call SyncRepoProfileStats afterwards if you
// want to keep computed stats accurate.
func (db *DB) UpsertRepoProfile(p *models.RepoProfile) error {
	now := time.Now()
	p.UpdatedAt = now

	var query string
	if db.IsPostgres() {
		query = fmt.Sprintf(`
			INSERT INTO repo_profiles (
				repo, total_prs_submitted, total_merged, total_rejected, merge_rate,
				avg_response_time_hours, requires_cla, requires_assignment, preferred_pr_size,
				blacklisted, blacklist_reason, cooldown_until, strategy_notes,
				last_interaction, updated_at
			) VALUES (%s)
			ON CONFLICT(repo) DO UPDATE SET
				total_prs_submitted   = EXCLUDED.total_prs_submitted,
				total_merged          = EXCLUDED.total_merged,
				total_rejected        = EXCLUDED.total_rejected,
				merge_rate            = EXCLUDED.merge_rate,
				avg_response_time_hours = EXCLUDED.avg_response_time_hours,
				requires_cla          = EXCLUDED.requires_cla,
				requires_assignment   = EXCLUDED.requires_assignment,
				preferred_pr_size     = EXCLUDED.preferred_pr_size,
				blacklisted           = EXCLUDED.blacklisted,
				blacklist_reason      = EXCLUDED.blacklist_reason,
				cooldown_until        = EXCLUDED.cooldown_until,
				strategy_notes        = EXCLUDED.strategy_notes,
				last_interaction      = EXCLUDED.last_interaction,
				updated_at            = EXCLUDED.updated_at
		`, db.placeholders(15))
	} else {
		query = fmt.Sprintf(`
			INSERT INTO repo_profiles (
				repo, total_prs_submitted, total_merged, total_rejected, merge_rate,
				avg_response_time_hours, requires_cla, requires_assignment, preferred_pr_size,
				blacklisted, blacklist_reason, cooldown_until, strategy_notes,
				last_interaction, updated_at
			) VALUES (%s)
			ON CONFLICT(repo) DO UPDATE SET
				total_prs_submitted   = excluded.total_prs_submitted,
				total_merged          = excluded.total_merged,
				total_rejected        = excluded.total_rejected,
				merge_rate            = excluded.merge_rate,
				avg_response_time_hours = excluded.avg_response_time_hours,
				requires_cla          = excluded.requires_cla,
				requires_assignment   = excluded.requires_assignment,
				preferred_pr_size     = excluded.preferred_pr_size,
				blacklisted           = excluded.blacklisted,
				blacklist_reason      = excluded.blacklist_reason,
				cooldown_until        = excluded.cooldown_until,
				strategy_notes        = excluded.strategy_notes,
				last_interaction      = excluded.last_interaction,
				updated_at            = excluded.updated_at
		`, db.placeholders(15))
	}

	_, err := db.Exec(query,
		p.Repo,
		p.TotalPRsSubmitted,
		p.TotalMerged,
		p.TotalRejected,
		p.MergeRate,
		p.AvgResponseTimeHours,
		p.RequiresCLA,
		p.RequiresAssignment,
		p.PreferredPRSize,
		p.Blacklisted,
		p.BlacklistReason,
		p.CooldownUntil,
		p.StrategyNotes,
		p.LastInteraction,
		p.UpdatedAt,
	)
	return err
}

// GetRepoProfile retrieves the profile for a repo.
// Returns (nil, nil) when no profile exists yet.
func (db *DB) GetRepoProfile(repo string) (*models.RepoProfile, error) {
	query := fmt.Sprintf(`
		SELECT repo, total_prs_submitted, total_merged, total_rejected, merge_rate,
		       avg_response_time_hours, requires_cla, requires_assignment, preferred_pr_size,
		       blacklisted, blacklist_reason, cooldown_until, strategy_notes,
		       last_interaction, updated_at
		FROM repo_profiles WHERE repo = %s
	`, db.placeholder(1))

	p := &models.RepoProfile{}
	var (
		avgResp     sql.NullFloat64
		prefSize    sql.NullString
		blackReason sql.NullString
		cooldown    sql.NullTime
		strategy    sql.NullString
		lastInter   sql.NullTime
	)
	err := db.QueryRow(query, repo).Scan(
		&p.Repo,
		&p.TotalPRsSubmitted,
		&p.TotalMerged,
		&p.TotalRejected,
		&p.MergeRate,
		&avgResp,
		&p.RequiresCLA,
		&p.RequiresAssignment,
		&prefSize,
		&p.Blacklisted,
		&blackReason,
		&cooldown,
		&strategy,
		&lastInter,
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
	if prefSize.Valid {
		p.PreferredPRSize = prefSize.String
	}
	if blackReason.Valid {
		p.BlacklistReason = blackReason.String
	}
	if cooldown.Valid {
		p.CooldownUntil = &cooldown.Time
	}
	if strategy.Valid {
		p.StrategyNotes = strategy.String
	}
	if lastInter.Valid {
		p.LastInteraction = &lastInter.Time
	}

	return p, nil
}

// SyncRepoProfileStats recomputes the stat fields for a repo from pull_requests
// and issues, then upserts only those fields — leaving manually set fields
// (RequiresCLA, StrategyNotes, CooldownUntil, etc.) untouched.
func (db *DB) SyncRepoProfileStats(repo string) error {
	// Compute stats from pull_requests JOIN issues.
	type stats struct {
		submitted    int
		merged       int
		rejected     int
		mergeRate    float64
		avgRespHours sql.NullFloat64
		lastInter    sql.NullTime
	}

	var s stats

	countQ := fmt.Sprintf(`
		SELECT
			COUNT(DISTINCT pr.id),
			SUM(CASE WHEN pr.status = 'merged' THEN 1 ELSE 0 END),
			SUM(CASE WHEN pr.status = 'closed' THEN 1 ELSE 0 END)
		FROM pull_requests pr
		JOIN issues i ON pr.issue_id = i.id
		WHERE i.repo = %s
	`, db.placeholder(1))
	if err := db.QueryRow(countQ, repo).Scan(&s.submitted, &s.merged, &s.rejected); err != nil {
		return fmt.Errorf("count PRs for %s: %w", repo, err)
	}

	terminal := s.merged + s.rejected
	if terminal > 0 {
		s.mergeRate = float64(s.merged) / float64(terminal)
	}

	// Average response time: hours from PR creation to first review, where available.
	var avgQ string
	if db.IsPostgres() {
		avgQ = fmt.Sprintf(`
			SELECT AVG(EXTRACT(EPOCH FROM (pr.first_review_at - pr.created_at)) / 3600)
			FROM pull_requests pr
			JOIN issues i ON pr.issue_id = i.id
			WHERE i.repo = %s AND pr.first_review_at IS NOT NULL
		`, db.placeholder(1))
	} else {
		avgQ = fmt.Sprintf(`
			SELECT AVG((julianday(pr.first_review_at) - julianday(pr.created_at)) * 24)
			FROM pull_requests pr
			JOIN issues i ON pr.issue_id = i.id
			WHERE i.repo = %s AND pr.first_review_at IS NOT NULL
		`, db.placeholder(1))
	}
	db.QueryRow(avgQ, repo).Scan(&s.avgRespHours) //nolint:errcheck — NULL is fine

	// Last interaction: most recent PR updated_at.
	lastQ := fmt.Sprintf(`
		SELECT MAX(pr.updated_at)
		FROM pull_requests pr
		JOIN issues i ON pr.issue_id = i.id
		WHERE i.repo = %s
	`, db.placeholder(1))
	db.QueryRow(lastQ, repo).Scan(&s.lastInter) //nolint:errcheck — NULL is fine

	// Upsert only stat columns; preserve manually set fields on conflict.
	now := time.Now()
	var upsertQ string
	if db.IsPostgres() {
		upsertQ = fmt.Sprintf(`
			INSERT INTO repo_profiles (repo, total_prs_submitted, total_merged, total_rejected,
				merge_rate, avg_response_time_hours, last_interaction, updated_at)
			VALUES (%s)
			ON CONFLICT(repo) DO UPDATE SET
				total_prs_submitted     = EXCLUDED.total_prs_submitted,
				total_merged            = EXCLUDED.total_merged,
				total_rejected          = EXCLUDED.total_rejected,
				merge_rate              = EXCLUDED.merge_rate,
				avg_response_time_hours = EXCLUDED.avg_response_time_hours,
				last_interaction        = EXCLUDED.last_interaction,
				updated_at              = EXCLUDED.updated_at
		`, db.placeholders(8))
	} else {
		upsertQ = fmt.Sprintf(`
			INSERT INTO repo_profiles (repo, total_prs_submitted, total_merged, total_rejected,
				merge_rate, avg_response_time_hours, last_interaction, updated_at)
			VALUES (%s)
			ON CONFLICT(repo) DO UPDATE SET
				total_prs_submitted     = excluded.total_prs_submitted,
				total_merged            = excluded.total_merged,
				total_rejected          = excluded.total_rejected,
				merge_rate              = excluded.merge_rate,
				avg_response_time_hours = excluded.avg_response_time_hours,
				last_interaction        = excluded.last_interaction,
				updated_at              = excluded.updated_at
		`, db.placeholders(8))
	}

	var avgRespVal interface{}
	if s.avgRespHours.Valid {
		avgRespVal = s.avgRespHours.Float64
	}
	var lastInterVal interface{}
	if s.lastInter.Valid {
		lastInterVal = s.lastInter.Time
	}

	_, err := db.Exec(upsertQ,
		repo,
		s.submitted,
		s.merged,
		s.rejected,
		s.mergeRate,
		avgRespVal,
		lastInterVal,
		now,
	)
	return err
}
