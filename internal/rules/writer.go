package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// writeMu serializes all rule YAML read-modify-write operations so that
// concurrent goroutines (feedback scanner + synthesis) never race on the same file.
var writeMu sync.Mutex

// WriteRule writes a Rule to a YAML file in rules/{stage}/ directory.
func WriteRule(rulesDir string, rule *Rule) error {
	// Defense-in-depth: reject IDs that could escape the rules/{stage} directory
	// via path traversal (e.g. "../../../etc/passwd" or absolute paths).
	if rule.ID == "" || strings.ContainsAny(rule.ID, "/\\") || strings.Contains(rule.ID, "..") || filepath.Base(rule.ID) != rule.ID {
		return fmt.Errorf("unsafe rule ID %q", rule.ID)
	}
	writeMu.Lock()
	defer writeMu.Unlock()

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
	writeMu.Lock()
	defer writeMu.Unlock()

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
	writeMu.Lock()
	defer writeMu.Unlock()

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

// UpdateRuleQValue updates the q_value, retrieval_count, and success_count fields of an existing rule file.
func UpdateRuleQValue(rulesDir string, ruleID string, stage string, qValue float64, retrievalCount int, successCount int) error {
	writeMu.Lock()
	defer writeMu.Unlock()

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

	rule.QValue = qValue
	rule.RetrievalCount = retrievalCount
	rule.SuccessCount = successCount
	updated, err := yaml.Marshal(&rule)
	if err != nil {
		return err
	}

	return os.WriteFile(path, updated, 0644)
}

// DeleteRule removes a rule file from disk.
// Holds writeMu to prevent races with UpdateRuleQValue and Reload.
func DeleteRule(rulesDir string, ruleID string, stage string) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	path := findRuleFile(rulesDir, ruleID, stage)
	if path == "" {
		return nil // already gone
	}
	return os.Remove(path)
}

// DecayRuleIfStale atomically reads last_validated_at from disk and, only if the
// rule has not been validated within staleDays, multiplies confidence by decayFactor
// (floored at minConf). The entire read-check-write runs under writeMu, which prevents
// a concurrent stampRuleValidation write from racing with the applyDecay decision.
func DecayRuleIfStale(rulesDir, ruleID, stage string, decayFactor, minConf float64, staleDays int) error {
	writeMu.Lock()
	defer writeMu.Unlock()

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

	// Skip if validated within staleDays — reading from disk guarantees we see
	// any last_validated_at that stampRuleValidation wrote before we acquired writeMu.
	if rule.LastValidatedAt != "" {
		validated, err := time.Parse("2006-01-02", rule.LastValidatedAt)
		if err == nil && time.Since(validated) < time.Duration(staleDays)*24*time.Hour {
			return nil
		}
	}

	if rule.Confidence <= minConf {
		return nil // already at floor
	}

	newConf := rule.Confidence * decayFactor
	if newConf < minConf {
		newConf = minConf
	}
	rule.Confidence = newConf

	updated, err := yaml.Marshal(&rule)
	if err != nil {
		return err
	}
	return os.WriteFile(path, updated, 0644)
}

// IncrementEvidenceCount increments the evidence_count field of an existing rule file
// and stamps last_validated_at to today. Both fields are updated atomically under fileMu
// so that applyDecay cannot observe a stale last_validated_at between the two writes.
// It is called when a candidate rule is merged into an existing rule during dedup.
func IncrementEvidenceCount(rulesDir string, ruleID string, stage string) error {
	// Reject IDs that could escape the rules directory via path traversal.
	// ruleID originates from dedup.MatchedRuleID which is loaded from rule YAML
	// and may be attacker-controlled; apply the same guard used in WriteRule.
	if ruleID == "" || strings.ContainsAny(ruleID, "/\\") || strings.Contains(ruleID, "..") || filepath.Base(ruleID) != ruleID {
		return fmt.Errorf("unsafe rule ID %q", ruleID)
	}

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

	rule.EvidenceCount++
	rule.LastValidatedAt = time.Now().Format("2006-01-02")
	updated, err := yaml.Marshal(&rule)
	if err != nil {
		return err
	}

	return os.WriteFile(path, updated, 0644)
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
