package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

type ruleEmbeddingStore interface {
	IsPostgres() bool
	GetRuleEmbeddingHashes() (map[string]string, error)
	UpsertRuleEmbedding(ruleKey, stage, contentHash string, embedding []float64, modelName string) error
	DeleteRuleEmbeddingsExcept(ruleKeys []string) error
	FindRuleEmbeddingCandidates(stages []string, embedding []float64, limit int) ([]db.RuleEmbeddingCandidate, error)
}

type textEmbedder interface {
	Embed(text string) ([]float64, error)
	ModelName() string
}

// RuleRetriever provides issue-aware rule selection backed by a derived vector index.
type RuleRetriever struct {
	store       ruleEmbeddingStore
	loader      *RuleLoader
	enabled     bool
	phaseALimit int
	topK        int
	lambda      float64
	embedder    textEmbedder
}

// NewRuleRetriever creates the semantic rule retriever. Unsupported providers fail
// at construction so callers can degrade to the full-load prompt path explicitly.
func NewRuleRetriever(cfg *config.Config, store ruleEmbeddingStore, loader *RuleLoader) (*RuleRetriever, error) {
	if loader == nil {
		return &RuleRetriever{}, nil
	}

	if cfg == nil || !cfg.SemanticRetrievalEnabled || store == nil || !store.IsPostgres() {
		return &RuleRetriever{store: store, loader: loader}, nil
	}

	embedder, err := newTextEmbedder(cfg.SemanticRetrievalProvider, cfg.SemanticRetrievalModel)
	if err != nil {
		return nil, err
	}

	phaseALimit := cfg.SemanticRetrievalPhaseA
	if phaseALimit <= 0 {
		phaseALimit = 20
	}
	topK := cfg.SemanticRetrievalTopK
	if topK <= 0 {
		topK = 5
	}
	if topK > phaseALimit {
		topK = phaseALimit
	}
	lambda := cfg.SemanticRetrievalLambda
	if lambda < 0 {
		lambda = 0
	}
	if lambda > 1 {
		lambda = 1
	}

	return &RuleRetriever{
		store:       store,
		loader:      loader,
		enabled:     true,
		phaseALimit: phaseALimit,
		topK:        topK,
		lambda:      lambda,
		embedder:    embedder,
	}, nil
}

// Sync upserts changed/new YAML rules into the derived index and removes deleted rules.
func (rr *RuleRetriever) Sync() error {
	if rr == nil || !rr.enabled || rr.loader == nil {
		return nil
	}

	existingHashes, err := rr.store.GetRuleEmbeddingHashes()
	if err != nil {
		return err
	}

	rules := rr.loader.All()
	ruleKeys := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule == nil || rule.Body == "" {
			continue
		}

		ruleKey := ruleParticipationKey(rule.Stage, rule.ID)
		ruleKeys = append(ruleKeys, ruleKey)

		ruleText := buildRuleEmbeddingText(rule)
		contentHash := contentHashForModel(rr.embedder.ModelName(), ruleText)
		if existingHashes[ruleKey] == contentHash {
			continue
		}

		embedding, err := rr.embedder.Embed(ruleText)
		if err != nil {
			return fmt.Errorf("embed rule %s: %w", ruleKey, err)
		}
		if err := rr.store.UpsertRuleEmbedding(ruleKey, rule.Stage, contentHash, embedding, rr.embedder.ModelName()); err != nil {
			return err
		}
	}

	sort.Strings(ruleKeys)
	return rr.store.DeleteRuleEmbeddingsExcept(ruleKeys)
}

// Retrieve returns the selected rule IDs and formatted prompt text for one stage+issue pair.
func (rr *RuleRetriever) Retrieve(stage string, issue *models.Issue) ([]string, string, error) {
	if rr == nil || rr.loader == nil {
		return nil, "", nil
	}
	if issue == nil || !rr.enabled {
		ids, promptText := rr.loader.PromptSnapshot(stage)
		return ids, promptText, nil
	}

	queryEmbedding, err := rr.embedder.Embed(buildIssueQueryText(issue))
	if err != nil {
		return nil, "", fmt.Errorf("embed issue query: %w", err)
	}

	candidates, err := rr.store.FindRuleEmbeddingCandidates([]string{stage, "global"}, queryEmbedding, rr.phaseALimit)
	if err != nil {
		return nil, "", err
	}
	if len(candidates) == 0 {
		return nil, "", nil
	}

	type rankedRule struct {
		rule       *Rule
		similarity float64
		score      float64
	}

	ranked := make([]rankedRule, 0, len(candidates))
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		if seen[candidate.RuleKey] {
			continue
		}
		seen[candidate.RuleKey] = true

		ruleStage, ruleID, ok := splitRuleParticipationKey(candidate.RuleKey)
		if !ok {
			continue
		}
		if ruleStage != stage && ruleStage != "global" {
			continue
		}
		rule := rr.loader.ByStageAndID(ruleStage, ruleID)
		if rule == nil || rule.Confidence < MinConfidenceForInjection {
			continue
		}

		similarity := clamp01(candidate.Similarity)
		qValue := normalizedQValue(rule.QValue)
		ranked = append(ranked, rankedRule{
			rule:       rule,
			similarity: similarity,
			score:      (1-rr.lambda)*similarity + rr.lambda*qValue,
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].similarity != ranked[j].similarity {
			return ranked[i].similarity > ranked[j].similarity
		}
		left := ruleParticipationKey(ranked[i].rule.Stage, ranked[i].rule.ID)
		right := ruleParticipationKey(ranked[j].rule.Stage, ranked[j].rule.ID)
		return left < right
	})

	selected := make([]*Rule, 0, rr.topK)
	for _, item := range ranked {
		selected = append(selected, item.rule)
		if len(selected) == rr.topK {
			break
		}
	}

	ids, promptText := promptSnapshotForRules(selected)
	return ids, promptText, nil
}

func buildRuleEmbeddingText(rule *Rule) string {
	var parts []string
	if rule.Condition != "" {
		parts = append(parts, "condition: "+rule.Condition)
	}
	if rule.Body != "" {
		parts = append(parts, "body: "+rule.Body)
	}
	if len(rule.Tags) > 0 {
		parts = append(parts, "tags: "+strings.Join(rule.Tags, ", "))
	}
	if rule.Severity != "" {
		parts = append(parts, "severity: "+rule.Severity)
	}
	return strings.Join(parts, "\n")
}

func buildIssueQueryText(issue *models.Issue) string {
	parts := []string{
		"repo: " + issue.Repo,
		"language: " + issue.Language,
		"labels: " + issue.Labels,
		"title: " + issue.Title,
		"body: " + issue.Body,
	}
	return strings.Join(parts, "\n")
}

func contentHashForModel(modelName, text string) string {
	sum := sha256.Sum256([]byte(modelName + "\n" + text))
	return hex.EncodeToString(sum[:])
}

func normalizedQValue(q float64) float64 {
	if q == 0 {
		return 0.5
	}
	return clamp01(q)
}

func clamp01(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func splitRuleParticipationKey(key string) (stage string, id string, ok bool) {
	stage, id, found := strings.Cut(key, "/")
	if !found || stage == "" || id == "" {
		return "", "", false
	}
	return stage, id, true
}

func newTextEmbedder(provider, model string) (textEmbedder, error) {
	normalizedProvider := strings.TrimSpace(strings.ToLower(provider))
	if normalizedProvider == "" {
		normalizedProvider = "local"
	}
	switch normalizedProvider {
	case "local":
		if strings.TrimSpace(model) == "" {
			model = "hash-v1"
		}
		return &hashEmbedder{model: model}, nil
	default:
		return nil, fmt.Errorf("unsupported semantic retrieval provider %q", provider)
	}
}

type hashEmbedder struct {
	model string
}

func (e *hashEmbedder) ModelName() string {
	return e.model
}

func (e *hashEmbedder) Embed(text string) ([]float64, error) {
	tokens := tokenizeForEmbedding(text)
	vector := make([]float64, db.RuleEmbeddingDimensions)
	if len(tokens) == 0 {
		vector[0] = 1
		return vector, nil
	}

	for _, token := range tokens {
		hash := fnv.New64a()
		if _, err := hash.Write([]byte(token)); err != nil {
			return nil, err
		}
		sum := hash.Sum64()
		index := int(sum % uint64(db.RuleEmbeddingDimensions))
		sign := 1.0
		if (sum>>8)&1 == 1 {
			sign = -1
		}
		vector[index] += sign
	}

	var norm float64
	for _, value := range vector {
		norm += value * value
	}
	if norm == 0 {
		vector[0] = 1
		return vector, nil
	}

	norm = math.Sqrt(norm)
	for i := range vector {
		vector[i] /= norm
	}
	return vector, nil
}

func tokenizeForEmbedding(text string) []string {
	text = strings.ToLower(text)
	replacer := strings.NewReplacer(
		"\n", " ",
		"\r", " ",
		"\t", " ",
		"/", " ",
		"-", " ",
		"_", " ",
		":", " ",
		",", " ",
		".", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
		"{", " ",
		"}", " ",
	)
	text = replacer.Replace(text)
	return strings.Fields(text)
}
