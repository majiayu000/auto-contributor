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
	// Hold fileMu for the entire walk so we cannot read a file that a concurrent
	// writer (UpdateRuleConfidence, UpdateRuleLastValidatedAt, WriteRule, DeleteRule)
	// has only partially written.  Without this lock, os.ReadFile can observe a
	// truncated or zero-byte file mid-os.WriteFile, producing a malformed YAML
	// unmarshal that silently drops the rule from in-memory state.
	fileMu.Lock()
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
	fileMu.Unlock()
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

// InjectedRuleIDsForStage returns the set of rule IDs that would actually be
// written into the prompt for stage, respecting the MaxPromptChars budget.
// Rules that pass MinConfidenceForInjection but are truncated by the char limit
// are excluded — they were never seen by the LLM and must not be treated as
// having influenced a merged PR's output.
func (rl *RuleLoader) InjectedRuleIDsForStage(stage string) map[string]bool {
	matched := rl.ForStage(stage)
	injected := make(map[string]bool)
	total := 0
	for _, r := range matched {
		entry := fmt.Sprintf("### %s (confidence: %.2f)\n\n%s\n\n---\n\n", r.ID, r.Confidence, r.Body)
		if total+len(entry) > MaxPromptChars {
			break
		}
		injected[r.ID] = true
		total += len(entry)
	}
	return injected
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

// HasSemanticMatch returns true if an existing rule for the given stage is semantically
// similar to the proposed rule based on keyword and tag overlap (threshold: 2 shared tokens).
// The second return value is the ID of the matching rule, or "" if none.
func (rl *RuleLoader) HasSemanticMatch(id string, tags []string, stage string) (bool, string) {
	newKW := ruleKeywords(id, tags)

	rl.mu.RLock()
	defer rl.mu.RUnlock()

	for _, r := range rl.rules {
		if r.Stage != stage && r.Stage != "global" {
			continue
		}
		if sharedKeywords(newKW, ruleKeywords(r.ID, r.Tags)) >= 2 {
			return true, r.ID
		}
	}
	return false, ""
}

// IDSummaryForStage returns a compact newline-separated "id: condition" list for all rules
// matching the given stage (and global). Used to provide deduplication context to the synthesizer.
func (rl *RuleLoader) IDSummaryForStage(stage string) string {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	var sb strings.Builder
	for _, r := range rl.rules {
		if r.Stage != stage && r.Stage != "global" {
			continue
		}
		sb.WriteString(r.ID)
		if r.Condition != "" {
			sb.WriteString(": ")
			sb.WriteString(r.Condition)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// ruleKeywords extracts meaningful tokens from a rule ID and its tags, dropping stop words.
func ruleKeywords(id string, tags []string) []string {
	stop := map[string]bool{
		"and": true, "or": true, "the": true, "for": true, "in": true,
		"to": true, "a": true, "of": true, "with": true, "on": true,
		"is": true, "not": true, "no": true, "per": true,
	}
	seen := map[string]bool{}
	var kw []string
	for _, p := range strings.Split(id, "-") {
		p = strings.ToLower(p)
		if p != "" && !stop[p] && !seen[p] {
			kw = append(kw, p)
			seen[p] = true
		}
	}
	for _, t := range tags {
		t = strings.ToLower(t)
		if t != "" && !stop[t] && !seen[t] {
			kw = append(kw, t)
			seen[t] = true
		}
	}
	return kw
}

// sharedKeywords counts distinct keywords that appear in both slices.
func sharedKeywords(a, b []string) int {
	setA := make(map[string]bool, len(a))
	for _, x := range a {
		setA[x] = true
	}
	count := 0
	seen := make(map[string]bool)
	for _, x := range b {
		if setA[x] && !seen[x] {
			count++
			seen[x] = true
		}
	}
	return count
}
