package db

import (
	"database/sql"
	"fmt"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// AddToBlacklist adds a repository to the blacklist
func (db *DB) AddToBlacklist(repo, reason string) error {
	query := fmt.Sprintf(`
		INSERT INTO blacklist (repo, reason) VALUES (%s, %s)
		ON CONFLICT(repo) DO UPDATE SET reason = excluded.reason
	`, db.placeholder(1), db.placeholder(2))
	_, err := db.Exec(query, repo, reason)
	return err
}

// RemoveFromBlacklist removes a repository from the blacklist
func (db *DB) RemoveFromBlacklist(repo string) error {
	query := fmt.Sprintf(`DELETE FROM blacklist WHERE repo = %s`, db.placeholder(1))
	_, err := db.Exec(query, repo)
	return err
}

// IsBlacklisted checks if a repository is blacklisted
func (db *DB) IsBlacklisted(repo string) (bool, error) {
	var count int
	query := fmt.Sprintf(`SELECT COUNT(*) FROM blacklist WHERE repo = %s`, db.placeholder(1))
	err := db.QueryRow(query, repo).Scan(&count)
	return count > 0, err
}

// GetBlacklist returns all blacklisted repositories
func (db *DB) GetBlacklist() ([]*models.BlacklistEntry, error) {
	return db.GetBlacklistFiltered("")
}

// GetBlacklistFiltered returns blacklisted repositories, optionally filtered by a LIKE pattern.
func (db *DB) GetBlacklistFiltered(repoFilter string) ([]*models.BlacklistEntry, error) {
	query := `SELECT id, repo, reason, added_at FROM blacklist`
	whereClause, args := db.repoLikeWhereClause("repo", repoFilter, 1)
	rows, err := db.Query(query+whereClause+` ORDER BY added_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*models.BlacklistEntry
	for rows.Next() {
		entry := &models.BlacklistEntry{}
		var reason sql.NullString
		if err := rows.Scan(&entry.ID, &entry.Repo, &reason, &entry.AddedAt); err != nil {
			continue
		}
		if reason.Valid {
			entry.Reason = reason.String
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
