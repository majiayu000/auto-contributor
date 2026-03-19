package db

import (
	"fmt"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// MigrateLessons creates the review_lessons table if it doesn't exist.
// Called from runMigrations.
func (db *DB) MigrateLessons() {
	if db.IsPostgres() {
		db.Exec(`
			CREATE TABLE IF NOT EXISTS review_lessons (
				id SERIAL PRIMARY KEY,
				pr_id INTEGER NOT NULL REFERENCES pull_requests(id),
				repo TEXT NOT NULL,
				category TEXT NOT NULL,
				lesson TEXT NOT NULL,
				source_comment TEXT,
				reviewer TEXT,
				created_at TIMESTAMP DEFAULT NOW()
			)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_lessons_repo ON review_lessons(repo)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_lessons_category ON review_lessons(category)`)
	} else {
		db.Exec(`
			CREATE TABLE IF NOT EXISTS review_lessons (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				pr_id INTEGER NOT NULL,
				repo TEXT NOT NULL,
				category TEXT NOT NULL,
				lesson TEXT NOT NULL,
				source_comment TEXT,
				reviewer TEXT,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (pr_id) REFERENCES pull_requests(id)
			)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_lessons_repo ON review_lessons(repo)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_lessons_category ON review_lessons(category)`)
	}
}

// SaveReviewLesson inserts a new review lesson.
func (db *DB) SaveReviewLesson(lesson *models.ReviewLesson) error {
	query := fmt.Sprintf(`
		INSERT INTO review_lessons (pr_id, repo, category, lesson, source_comment, reviewer)
		VALUES (%s)
	`, db.placeholders(6))

	_, err := db.Exec(query,
		lesson.PRID, lesson.Repo, lesson.Category,
		lesson.Lesson, lesson.SourceComment, lesson.Reviewer,
	)
	return err
}

// GetRecentLessons returns the most recent lessons, optionally filtered by repo language prefix.
func (db *DB) GetRecentLessons(limit int) ([]*models.ReviewLesson, error) {
	query := fmt.Sprintf(`
		SELECT id, pr_id, repo, category, lesson, COALESCE(source_comment,''), COALESCE(reviewer,''), created_at
		FROM review_lessons
		ORDER BY created_at DESC
		LIMIT %s
	`, db.placeholder(1))

	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lessons []*models.ReviewLesson
	for rows.Next() {
		l := &models.ReviewLesson{}
		if err := rows.Scan(&l.ID, &l.PRID, &l.Repo, &l.Category, &l.Lesson, &l.SourceComment, &l.Reviewer, &l.CreatedAt); err != nil {
			continue
		}
		lessons = append(lessons, l)
	}
	return lessons, nil
}

// GetLessonsByCategory returns lessons for a specific category.
func (db *DB) GetLessonsByCategory(category string, limit int) ([]*models.ReviewLesson, error) {
	query := fmt.Sprintf(`
		SELECT id, pr_id, repo, category, lesson, COALESCE(source_comment,''), COALESCE(reviewer,''), created_at
		FROM review_lessons
		WHERE category = %s
		ORDER BY created_at DESC
		LIMIT %s
	`, db.placeholder(1), db.placeholder(2))

	rows, err := db.Query(query, category, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lessons []*models.ReviewLesson
	for rows.Next() {
		l := &models.ReviewLesson{}
		if err := rows.Scan(&l.ID, &l.PRID, &l.Repo, &l.Category, &l.Lesson, &l.SourceComment, &l.Reviewer, &l.CreatedAt); err != nil {
			continue
		}
		lessons = append(lessons, l)
	}
	return lessons, nil
}

// CountLessonsByPR returns how many lessons exist for a given PR.
func (db *DB) CountLessonsByPR(prID int64) (int, error) {
	var count int
	query := fmt.Sprintf(`SELECT COUNT(*) FROM review_lessons WHERE pr_id = %s`, db.placeholder(1))
	err := db.QueryRow(query, prID).Scan(&count)
	return count, err
}
