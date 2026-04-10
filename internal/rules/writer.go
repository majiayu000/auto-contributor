package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// fileMu serializes all read-modify-write operations on rule YAML files.
// Both the feedback goroutine (stampRuleValidation) and the synthesis goroutine
// (applySynthesisResult / applyDecay) write rule files concurrently; without this
// lock a read→modify→write in one goroutine can overwrite a concurrent write from
// the other, silently dropping either the confidence or last_validated_at update.
var fileMu sync.Mutex

// WriteRule writes a Rule to a YAML file in rules/{stage}/ directory.
func WriteRule(rulesDir string, rule *Rule) error {
	dir := filepath.Join(rulesDir, rule.Stage)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	data, err := yaml.Marshal(rule)
	if err != nil {
		return fmt.Errorf("marshal rule %s: %w", rule.ID, err)
	}

	path := filepath.Join(dir, rule.ID+".yaml")
	return os.WriteFile(path, data, 0644)
}

// UpdateRuleConfidence updates only the confidence field of an existing rule file.
func UpdateRuleConfidence(rulesDir string, ruleID string, stage string, newConfidence float64) error {
	fileMu.Lock()
	defer fileMu.Unlock()

	path := findRuleFile(rulesDir, ruleID, stage)
	if path == "" {
		return fmt.Errorf("rule file not found: %s", ruleID)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var rule Rule
	if err := yaml.Unmarshal(data, &rule); err != nil {
		return err
	}

	rule.Confidence = newConfidence
	updated, err := yaml.Marshal(&rule)
	if err != nil {
		return err
	}

	return os.WriteFile(path, updated, 0644)
}

// UpdateRuleLastValidatedAt updates the last_validated_at field of an existing rule file.
func UpdateRuleLastValidatedAt(rulesDir string, ruleID string, stage string, validatedAt string) error {
	fileMu.Lock()
	defer fileMu.Unlock()

	path := findRuleFile(rulesDir, ruleID, stage)
	if path == "" {
		return fmt.Errorf("rule file not found: %s", ruleID)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var rule Rule
	if err := yaml.Unmarshal(data, &rule); err != nil {
		return err
	}

	rule.LastValidatedAt = validatedAt
	updated, err := yaml.Marshal(&rule)
	if err != nil {
		return err
	}

	return os.WriteFile(path, updated, 0644)
}

// DeleteRule removes a rule file from disk.
func DeleteRule(rulesDir string, ruleID string, stage string) error {
	fileMu.Lock()
	defer fileMu.Unlock()

	path := findRuleFile(rulesDir, ruleID, stage)
	if path == "" {
		return nil // already gone
	}
	return os.Remove(path)
}

// findRuleFile locates a rule file by ID, checking stage dir first then walking all dirs.
func findRuleFile(rulesDir, ruleID, stage string) string {
	// Check stage-specific dir first
	if stage != "" {
		path := filepath.Join(rulesDir, stage, ruleID+".yaml")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Walk all dirs
	var found string
	filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.TrimSuffix(info.Name(), ".yaml") == ruleID {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
