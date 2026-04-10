package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const MinConfidenceForInjection = 0.3
const MaxPromptChars = 2000

// RuleLoader reads and caches rules from the rules/ directory tree.
type RuleLoader struct {
	rulesDir string
	mu       sync.RWMutex
	rules    []*Rule
}

func NewRuleLoader(rulesDir string) *RuleLoader {
	return &RuleLoader{rulesDir: rulesDir}
}

// RulesDir returns the rules directory path.
func (rl *RuleLoader) RulesDir() string {
	return rl.rulesDir
}

// Load reads all YAML files from the rules directory.
func (rl *RuleLoader) Load() error {
	if rl.rulesDir == "" {
		return nil
	}

	info, err := os.Stat(rl.rulesDir)
	if err != nil || !info.IsDir() {
		return nil // missing dir is not an error
	}

	var loaded []*Rule
	err = filepath.Walk(rl.rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable files
		}
		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var rule Rule
		if err := yaml.Unmarshal(data, &rule); err != nil {
			fmt.Fprintf(os.Stderr, "warn: skipping malformed rule %s: %v\n", path, err)
			return nil
		}

		if rule.ID == "" || rule.Body == "" {
			return nil
		}

		loaded = append(loaded, &rule)
		return nil
	})
	if err != nil {
		return err
	}

	// Sort: severity (critical first), then confidence (highest first)
	sort.Slice(loaded, func(i, j int) bool {
		if loaded[i].SeverityRank() != loaded[j].SeverityRank() {
			return loaded[i].SeverityRank() < loaded[j].SeverityRank()
		}
		return loaded[i].Confidence > loaded[j].Confidence
	})

	rl.mu.Lock()
	rl.rules = loaded
	rl.mu.Unlock()

	return nil
}

// Reload re-reads rules from disk.
func (rl *RuleLoader) Reload() error {
	return rl.Load()
}

// All returns all loaded rules.
func (rl *RuleLoader) All() []*Rule {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return rl.rules
}

// ForStage returns rules matching a stage (stage-specific + global), filtered by confidence.
func (rl *RuleLoader) ForStage(stage string) []*Rule {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	var matched []*Rule
	for _, r := range rl.rules {
		if r.Confidence < MinConfidenceForInjection {
			continue
		}
		if r.Stage == stage || r.Stage == "global" {
			matched = append(matched, r)
		}
	}
	return matched
}

// FormatForPrompt returns concatenated rule bodies as Markdown for prompt injection.
func (rl *RuleLoader) FormatForPrompt(stage string) string {
	matched := rl.ForStage(stage)
	if len(matched) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Self-Learning Rules\n\n")
	sb.WriteString("Follow these rules based on past experience:\n\n")

	total := 0
	for _, r := range matched {
		entry := fmt.Sprintf("### %s (confidence: %.2f)\n\n%s\n\n---\n\n", r.ID, r.Confidence, r.Body)
		if total+len(entry) > MaxPromptChars {
			break
		}
		sb.WriteString(entry)
		total += len(entry)
	}

	return sb.String()
}

// ByID finds a rule by its ID.
func (rl *RuleLoader) ByID(id string) *Rule {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	for _, r := range rl.rules {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// HasSimilarInStage reports whether any existing rule for the given stage has a
// body semantically similar to the candidate body (Jaccard similarity ≥ 0.4).
// Returns the most-similar rule and true when a duplicate is detected.
func (rl *RuleLoader) HasSimilarInStage(stage, body string) (*Rule, bool) {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	newKW := extractKeywords(body)
	if len(newKW) == 0 {
		return nil, false
	}

	var best *Rule
	bestScore := 0.0
	for _, r := range rl.rules {
		if r.Stage != stage && r.Stage != "global" {
			continue
		}
		score := jaccardSimilarity(newKW, extractKeywords(r.Body))
		if score > bestScore {
			bestScore = score
			best = r
		}
	}
	if bestScore >= 0.4 {
		return best, true
	}
	return nil, false
}

// extractKeywords tokenizes text into lowercase significant words,
// filtering out short words and common English stopwords.
func extractKeywords(text string) map[string]struct{} {
	stopwords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "this": true,
		"that": true, "with": true, "for": true, "not": true, "and": true,
		"or": true, "if": true, "in": true, "of": true, "to": true,
		"it": true, "its": true, "from": true, "by": true, "on": true,
		"at": true, "as": true, "do": true, "does": true, "has": true,
		"have": true, "had": true, "will": true, "would": true, "should": true,
		"can": true, "when": true, "than": true, "no": true, "any": true,
		"all": true, "per": true, "via": true, "use": true, "set": true,
	}

	keywords := make(map[string]struct{})
	for _, word := range strings.Fields(strings.ToLower(text)) {
		word = strings.Trim(word, ".,;:!?\"'()[]{}*#-_/\\")
		if len(word) <= 3 || stopwords[word] {
			continue
		}
		keywords[word] = struct{}{}
	}
	return keywords
}

// jaccardSimilarity returns |A ∩ B| / |A ∪ B| for two keyword sets.
func jaccardSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for k := range a {
		if _, ok := b[k]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
