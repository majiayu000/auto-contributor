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
