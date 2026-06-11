package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// GetStats returns summary statistics
func (db *DB) GetStats(days int) (map[string]interface{}, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	stats := make(map[string]interface{})

	var totalAttempts, successfulAttempts int
	var avgDuration sql.NullFloat64

	query := fmt.Sprintf(`
		SELECT COUNT(*), SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), AVG(duration_seconds)
		FROM solve_attempts
		WHERE started_at >= %s
	`, db.placeholder(1))
	db.QueryRow(query, cutoff).Scan(&totalAttempts, &successfulAttempts, &avgDuration)

	stats["total_attempts"] = totalAttempts
	stats["successful_attempts"] = successfulAttempts
	if totalAttempts > 0 {
		stats["success_rate"] = float64(successfulAttempts) / float64(totalAttempts)
	} else {
		stats["success_rate"] = 0.0
	}
	if avgDuration.Valid {
		stats["avg_duration_seconds"] = avgDuration.Float64
	} else {
		stats["avg_duration_seconds"] = 0.0
	}

	return stats, nil
}

// GetPRMetrics returns PR-related aggregate statistics
func (db *DB) GetPRMetrics() (*models.PRMetrics, error) {
	metrics := &models.PRMetrics{}

	db.QueryRow(`SELECT COUNT(*) FROM pull_requests`).Scan(&metrics.TotalPRs)
	db.QueryRow(`SELECT COUNT(*) FROM pull_requests WHERE status = 'open'`).Scan(&metrics.OpenPRs)
	db.QueryRow(`SELECT COUNT(*) FROM pull_requests WHERE status = 'merged'`).Scan(&metrics.MergedPRs)
	db.QueryRow(`SELECT COUNT(*) FROM pull_requests WHERE status = 'closed'`).Scan(&metrics.ClosedPRs)

	if metrics.TotalPRs > 0 {
		metrics.MergeRate = float64(metrics.MergedPRs) / float64(metrics.TotalPRs)
	}

	var avgMerge sql.NullFloat64
	db.QueryRow(db.avgHoursQuery("merged_at", "created_at")).Scan(&avgMerge)
	if avgMerge.Valid {
		metrics.AvgTimeToMerge = avgMerge.Float64
	}

	var avgReview sql.NullFloat64
	db.QueryRow(db.avgHoursQuery("first_review_at", "created_at")).Scan(&avgReview)
	if avgReview.Valid {
		metrics.AvgTimeToFirstReview = avgReview.Float64
	}

	var avgComments sql.NullFloat64
	db.QueryRow(`SELECT AVG(review_comment_count) FROM pull_requests`).Scan(&avgComments)
	if avgComments.Valid {
		metrics.AvgReviewComments = avgComments.Float64
	}

	return metrics, nil
}

// GetLanguageMetrics returns metrics grouped by programming language
func (db *DB) GetLanguageMetrics() ([]*models.LanguageMetrics, error) {
	rows, err := db.Query(`
		SELECT
			i.language,
			COUNT(DISTINCT sa.id) as attempt_count,
			SUM(CASE WHEN sa.success = 1 THEN 1 ELSE 0 END) as success_count,
			AVG(sa.duration_seconds) as avg_duration,
			SUM(sa.total_tokens) as total_tokens,
			COUNT(DISTINCT pr.id) as prs_created,
			SUM(CASE WHEN pr.status = 'merged' THEN 1 ELSE 0 END) as prs_merged
		FROM issues i
		LEFT JOIN solve_attempts sa ON i.id = sa.issue_id
		LEFT JOIN pull_requests pr ON i.id = pr.issue_id
		WHERE i.language IS NOT NULL AND i.language != ''
		GROUP BY i.language
		ORDER BY attempt_count DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []*models.LanguageMetrics
	for rows.Next() {
		m := &models.LanguageMetrics{}
		var avgDuration, totalTokens sql.NullFloat64
		var prsCreated, prsMerged sql.NullInt64
		err := rows.Scan(&m.Language, &m.AttemptCount, &m.SuccessCount, &avgDuration, &totalTokens, &prsCreated, &prsMerged)
		if err != nil {
			continue
		}
		if avgDuration.Valid {
			m.AvgDurationSeconds = avgDuration.Float64
		}
		if totalTokens.Valid {
			m.TotalTokensUsed = int(totalTokens.Float64)
		}
		if prsCreated.Valid {
			m.PRsCreated = int(prsCreated.Int64)
		}
		if prsMerged.Valid {
			m.PRsMerged = int(prsMerged.Int64)
		}
		if m.AttemptCount > 0 {
			m.SuccessRate = float64(m.SuccessCount) / float64(m.AttemptCount)
		}
		if m.PRsCreated > 0 {
			m.MergeRate = float64(m.PRsMerged) / float64(m.PRsCreated)
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetRepoMetrics returns metrics for a specific repository or all repos
func (db *DB) GetRepoMetrics(repo string) ([]*models.RepoMetrics, error) {
	query := `
		SELECT
			i.repo,
			i.language,
			COUNT(DISTINCT sa.id) as attempt_count,
			SUM(CASE WHEN sa.success = 1 THEN 1 ELSE 0 END) as success_count,
			AVG(sa.duration_seconds) as avg_duration,
			SUM(sa.total_tokens) as total_tokens,
			SUM(sa.lines_added + sa.lines_deleted) as total_lines,
			COUNT(DISTINCT pr.id) as prs_created,
			SUM(CASE WHEN pr.status = 'merged' THEN 1 ELSE 0 END) as prs_merged,
			SUM(CASE WHEN pr.status = 'closed' THEN 1 ELSE 0 END) as prs_closed
		FROM issues i
		LEFT JOIN solve_attempts sa ON i.id = sa.issue_id
		LEFT JOIN pull_requests pr ON i.id = pr.issue_id
		%s
		GROUP BY i.repo
		ORDER BY attempt_count DESC
	`

	whereClause := ""
	var rows *sql.Rows
	var err error
	if repo != "" {
		whereClause = fmt.Sprintf("WHERE i.repo = %s", db.placeholder(1))
		rows, err = db.Query(fmt.Sprintf(query, whereClause), repo)
	} else {
		rows, err = db.Query(fmt.Sprintf(query, whereClause))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []*models.RepoMetrics
	for rows.Next() {
		m := &models.RepoMetrics{}
		var avgDuration, totalTokens, totalLines sql.NullFloat64
		var prsCreated, prsMerged, prsClosed sql.NullInt64
		err := rows.Scan(&m.Repo, &m.Language, &m.AttemptCount, &m.SuccessCount, &avgDuration, &totalTokens, &totalLines, &prsCreated, &prsMerged, &prsClosed)
		if err != nil {
			continue
		}
		if avgDuration.Valid {
			m.AvgDurationSeconds = avgDuration.Float64
		}
		if totalTokens.Valid {
			m.TotalTokensUsed = int(totalTokens.Float64)
		}
		if totalLines.Valid {
			m.TotalLinesChanged = int(totalLines.Float64)
		}
		if prsCreated.Valid {
			m.PRsCreated = int(prsCreated.Int64)
		}
		if prsMerged.Valid {
			m.PRsMerged = int(prsMerged.Int64)
		}
		if prsClosed.Valid {
			m.PRsClosed = int(prsClosed.Int64)
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetFailureReasonStats returns failure counts grouped by reason
func (db *DB) GetFailureReasonStats() (map[string]int, error) {
	rows, err := db.Query(`
		SELECT failure_reason, COUNT(*) as count
		FROM solve_attempts
		WHERE success = 0 AND failure_reason IS NOT NULL AND failure_reason != ''
		GROUP BY failure_reason
		ORDER BY count DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[string]int)
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			continue
		}
		stats[reason] = count
	}

	return stats, nil
}

// avgHoursQuery returns a SQL query to compute AVG hours between two timestamp columns.
// SQLite uses julianday(), PostgreSQL uses EXTRACT(EPOCH FROM ...).
func (db *DB) avgHoursQuery(endCol, startCol string) string {
	if db.IsPostgres() {
		return fmt.Sprintf(`
			SELECT AVG(EXTRACT(EPOCH FROM (%s - %s)) / 3600)
			FROM pull_requests WHERE %s IS NOT NULL
		`, endCol, startCol, endCol)
	}
	return fmt.Sprintf(`
		SELECT AVG((julianday(%s) - julianday(%s)) * 24)
		FROM pull_requests WHERE %s IS NOT NULL
	`, endCol, startCol, endCol)
}

// GetTokenUsageStats returns token usage statistics
func (db *DB) GetTokenUsageStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	var totalPrompt, totalCompletion, totalTokens sql.NullInt64
	var avgPerAttempt sql.NullFloat64
	db.QueryRow(`
		SELECT SUM(prompt_tokens), SUM(completion_tokens), SUM(total_tokens), AVG(total_tokens)
		FROM solve_attempts
		WHERE total_tokens > 0
	`).Scan(&totalPrompt, &totalCompletion, &totalTokens, &avgPerAttempt)

	if totalPrompt.Valid {
		stats["total_prompt_tokens"] = totalPrompt.Int64
	}
	if totalCompletion.Valid {
		stats["total_completion_tokens"] = totalCompletion.Int64
	}
	if totalTokens.Valid {
		stats["total_tokens"] = totalTokens.Int64
	}
	if avgPerAttempt.Valid {
		stats["avg_tokens_per_attempt"] = avgPerAttempt.Float64
	}

	var successTokens, failTokens sql.NullFloat64
	db.QueryRow(`SELECT AVG(total_tokens) FROM solve_attempts WHERE success = 1 AND total_tokens > 0`).Scan(&successTokens)
	db.QueryRow(`SELECT AVG(total_tokens) FROM solve_attempts WHERE success = 0 AND total_tokens > 0`).Scan(&failTokens)
	if successTokens.Valid {
		stats["avg_tokens_successful"] = successTokens.Float64
	}
	if failTokens.Valid {
		stats["avg_tokens_failed"] = failTokens.Float64
	}

	return stats, nil
}
