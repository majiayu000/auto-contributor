package db

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

const RuleEmbeddingDimensions = 64

// RuleEmbeddingCandidate is a Phase A retrieval hit scored by cosine similarity.
type RuleEmbeddingCandidate struct {
	RuleKey    string
	Stage      string
	Similarity float64
}

// MigrateRuleEmbeddings creates the derived rule embedding index on PostgreSQL.
// SQLite remains a no-op so the existing YAML full-load path keeps working.
func (db *DB) MigrateRuleEmbeddings() error {
	if !db.IsPostgres() {
		return nil
	}

	statements := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS rule_embeddings (
				rule_key TEXT PRIMARY KEY,
				stage TEXT NOT NULL,
				content_hash TEXT NOT NULL,
				embedding vector(%d) NOT NULL,
				model_name TEXT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
			)
		`, RuleEmbeddingDimensions),
		`CREATE INDEX IF NOT EXISTS idx_rule_embeddings_stage ON rule_embeddings(stage)`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate rule embeddings: %w", err)
		}
	}

	return nil
}

// GetRuleEmbeddingHashes returns the current content hashes keyed by stage/ruleID.
func (db *DB) GetRuleEmbeddingHashes() (map[string]string, error) {
	if !db.IsPostgres() {
		return map[string]string{}, nil
	}

	rows, err := db.Query(`SELECT rule_key, content_hash FROM rule_embeddings`)
	if err != nil {
		return nil, fmt.Errorf("query rule embedding hashes: %w", err)
	}
	defer rows.Close()

	hashes := make(map[string]string)
	for rows.Next() {
		var ruleKey, contentHash string
		if err := rows.Scan(&ruleKey, &contentHash); err != nil {
			return nil, fmt.Errorf("scan rule embedding hash: %w", err)
		}
		hashes[ruleKey] = contentHash
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rule embedding hashes: %w", err)
	}

	return hashes, nil
}

// UpsertRuleEmbedding inserts or updates a rule's derived embedding row.
func (db *DB) UpsertRuleEmbedding(ruleKey, stage, contentHash string, embedding []float64, modelName string) error {
	if !db.IsPostgres() {
		return nil
	}

	const stmt = `
		INSERT INTO rule_embeddings (rule_key, stage, content_hash, embedding, model_name)
		VALUES ($1, $2, $3, $4::vector, $5)
		ON CONFLICT (rule_key) DO UPDATE
		SET stage = EXCLUDED.stage,
			content_hash = EXCLUDED.content_hash,
			embedding = EXCLUDED.embedding,
			model_name = EXCLUDED.model_name,
			updated_at = CURRENT_TIMESTAMP
	`

	if _, err := db.Exec(stmt, ruleKey, stage, contentHash, vectorLiteral(embedding), modelName); err != nil {
		return fmt.Errorf("upsert rule embedding %s: %w", ruleKey, err)
	}
	return nil
}

// DeleteRuleEmbeddingsExcept removes derived rows for rules no longer present in YAML.
func (db *DB) DeleteRuleEmbeddingsExcept(ruleKeys []string) error {
	if !db.IsPostgres() {
		return nil
	}

	if len(ruleKeys) == 0 {
		if _, err := db.Exec(`DELETE FROM rule_embeddings`); err != nil {
			return fmt.Errorf("delete rule embeddings: %w", err)
		}
		return nil
	}

	if _, err := db.Exec(`DELETE FROM rule_embeddings WHERE NOT (rule_key = ANY($1))`, pq.Array(ruleKeys)); err != nil {
		return fmt.Errorf("delete stale rule embeddings: %w", err)
	}
	return nil
}

// FindRuleEmbeddingCandidates runs the Phase A cosine-similarity lookup.
func (db *DB) FindRuleEmbeddingCandidates(stages []string, embedding []float64, limit int) ([]RuleEmbeddingCandidate, error) {
	if !db.IsPostgres() || len(stages) == 0 || limit <= 0 {
		return nil, nil
	}

	const stmt = `
		SELECT
			rule_key,
			stage,
			1 - (embedding <=> $2::vector) AS similarity
		FROM rule_embeddings
		WHERE stage = ANY($1)
		ORDER BY embedding <=> $2::vector
		LIMIT $3
	`

	rows, err := db.Query(stmt, pq.Array(stages), vectorLiteral(embedding), limit)
	if err != nil {
		return nil, fmt.Errorf("query rule embedding candidates: %w", err)
	}
	defer rows.Close()

	var candidates []RuleEmbeddingCandidate
	for rows.Next() {
		var candidate RuleEmbeddingCandidate
		var similarity sql.NullFloat64
		if err := rows.Scan(&candidate.RuleKey, &candidate.Stage, &similarity); err != nil {
			return nil, fmt.Errorf("scan rule embedding candidate: %w", err)
		}
		if similarity.Valid {
			candidate.Similarity = similarity.Float64
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rule embedding candidates: %w", err)
	}

	return candidates, nil
}

func vectorLiteral(values []float64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatFloat(value, 'f', -1, 64))
	}
	return "[" + strings.Join(parts, ",") + "]"
}
