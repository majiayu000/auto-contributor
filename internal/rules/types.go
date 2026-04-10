package rules

// Rule represents a single self-learning rule loaded from disk.
type Rule struct {
	ID              string   `yaml:"id"`
	Stage           string   `yaml:"stage"`
	Severity        string   `yaml:"severity"`
	Confidence      float64  `yaml:"confidence"`
	Source          string   `yaml:"source"`
	CreatedAt       string   `yaml:"created_at"`
	LastValidatedAt string   `yaml:"last_validated_at"`
	EvidenceCount   int      `yaml:"evidence_count"`
	Tags            []string `yaml:"tags"`
	Condition       string   `yaml:"condition"`
	Body            string   `yaml:"body"`
	// MemRL Q-value fields (see GitHub issue #15)
	QValue         float64 `yaml:"q_value"`
	RetrievalCount int     `yaml:"retrieval_count"`
	SuccessCount   int     `yaml:"success_count"`
}

// SeverityRank returns a numeric rank for sorting (lower = more severe).
func (r *Rule) SeverityRank() int {
	switch r.Severity {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}
