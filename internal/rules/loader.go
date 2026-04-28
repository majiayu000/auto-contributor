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

// maxIDSummaryBytes caps the total byte size of the ExistingRuleIDs payload injected
// into the synthesizer prompt, which is passed via -p argv. Keeping it well below
// typical OS ARG_MAX (128 KB per argument on Linux) prevents "argument list too long" failures.
const maxIDSummaryBytes = 4096

// maxConditionLen caps the per-rule condition snippet included in IDSummaryForStage.
const maxConditionLen = 80

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
// Call this at startup before any write goroutines are running.
func (rl *RuleLoader) Load() error {
	return rl.readFromDisk()
}

// Reload re-reads rules from disk. readFromDisk acquires writeMu internally
// so it cannot read a file being written by a concurrent WriteRule/UpdateRuleQValue call.
func (rl *RuleLoader) Reload() error {
	return rl.readFromDisk()
}

// readFromDisk walks the rules directory and reloads the in-memory cache.
// Callers are responsible for holding writeMu when concurrent writes may occur.
func (rl *RuleLoader) readFromDisk() error {
	if rl.rulesDir == "" {
		return nil
	}

	info, err := os.Stat(rl.rulesDir)
	if err != nil || !info.IsDir() {
		return nil // missing dir is not an error
	}

	var loaded []*Rule
	// Hold writeMu for the entire walk so we cannot read a file that a concurrent
	// writer (UpdateRuleConfidence, UpdateRuleLastValidatedAt, WriteRule, DeleteRule)
	// has only partially written.  Without this lock, os.ReadFile can observe a
	// truncated or zero-byte file mid-os.WriteFile, producing a malformed YAML
	// unmarshal that silently drops the rule from in-memory state.
	writeMu.Lock()
	err = filepath.Walk(rl.rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable files
		}
		// Skip symlinks — following them could escape the rules directory (SEC-07).
		// filepath.Walk uses os.Lstat, so ModeSymlink is set for symlink entries.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
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

		if rule.Body == "" {
			return nil
		}
		// Rules with an invalid Stage or ID can never be managed via the normal
		// write paths (WriteRule, DeleteRule, etc. all reject them). Delete them
		// now so they are not re-injected into prompts on every Reload (self-healing).
		if !allowedStages[rule.Stage] {
			fmt.Fprintf(os.Stderr, "warn: quarantining rule with invalid stage %q: %s\n", rule.Stage, path)
			os.Remove(path) //nolint:errcheck
			return nil
		}
		if rule.ID == "" || strings.ContainsAny(rule.ID, "/\\") || strings.Contains(rule.ID, "..") || filepath.Base(rule.ID) != rule.ID {
			fmt.Fprintf(os.Stderr, "warn: quarantining rule with unsafe ID %q: %s\n", rule.ID, path)
			os.Remove(path) //nolint:errcheck
			return nil
		}

		loaded = append(loaded, &rule)
		return nil
	})
	writeMu.Unlock()
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

func promptSnapshotForRules(matched []*Rule) (ids []string, formatted string) {
	if len(matched) == 0 {
		return nil, ""
	}

	const header = "## Self-Learning Rules\n\nFollow these rules based on past experience:\n\n"
	var sb strings.Builder
	sb.WriteString(header)
	total := len(header)

	for _, r := range matched {
		entry := fmt.Sprintf("### %s (confidence: %.2f)\n\n%s\n\n---\n\n", r.ID, r.Confidence, r.Body)
		if total+len(entry) > MaxPromptChars {
			break
		}
		ids = append(ids, ruleParticipationKey(r.Stage, r.ID))
		sb.WriteString(entry)
		total += len(entry)
	}

	if len(ids) > 0 {
		formatted = sb.String()
	}
	return ids, formatted
}

func ruleParticipationKey(stage, id string) string {
	return stage + "/" + id
}

// IDsForPrompt returns the participation keys of rules actually included in the
// prompt for a given stage, honouring the same MaxPromptChars budget as
// FormatForPrompt. Keys are returned as "stage/ruleID" so that Q-value updates
// can resolve rules unambiguously even when IDs are not unique across stages.
func (rl *RuleLoader) IDsForPrompt(stage string) []string {
	ids, _ := promptSnapshotForRules(rl.ForStage(stage))
	return ids
}

// FormatForPrompt returns concatenated rule bodies as Markdown for prompt injection.
func (rl *RuleLoader) FormatForPrompt(stage string) string {
	_, formatted := promptSnapshotForRules(rl.ForStage(stage))
	return formatted
}

// InjectedRuleIDsForStage returns the set of rule IDs that would actually be
// written into the prompt for stage, respecting the MaxPromptChars budget.
// Rules that pass MinConfidenceForInjection but are truncated by the char limit
// are excluded — they were never seen by the LLM and must not be treated as
// having influenced a merged PR's output.
func (rl *RuleLoader) InjectedRuleIDsForStage(stage string) map[string]bool {
	matched := rl.ForStage(stage)
	injected := make(map[string]bool)
	const header = "## Self-Learning Rules\n\nFollow these rules based on past experience:\n\n"
	total := len(header)
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

// PromptSnapshot returns both the rule IDs and formatted text for stage from a
// single ForStage call, so IDsForPrompt and FormatForPrompt cannot diverge due
// to a concurrent Reload between two separate calls.
func (rl *RuleLoader) PromptSnapshot(stage string) (ids []string, formatted string) {
	return promptSnapshotForRules(rl.ForStage(stage))
}

// ByID finds a rule by its ID. When multiple rules share an ID across stages,
// the first match is returned. Prefer ByStageAndID for unambiguous lookup.
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
// similar to the proposed rule based on keyword and tag overlap.
//
// Match criteria: shared tokens ≥ 2 AND shared/min(|A|,|B|) ≥ 0.5.
// The ratio guard prevents high-frequency but low-signal tokens (e.g. "ci", "tests")
// from triggering false-positive suppression of distinct new rules.
// The second return value is the ID of the matching rule, or "" if none.
func (rl *RuleLoader) HasSemanticMatch(id string, tags []string, stage string) (bool, string) {
	newKW := ruleKeywords(id, tags)
	if len(newKW) < 2 {
		return false, ""
	}

	rl.mu.RLock()
	defer rl.mu.RUnlock()

	for _, r := range rl.rules {
		if r.Stage != stage && r.Stage != "global" {
			continue
		}
		// Exclude rules below the injection threshold: stale/decayed rules should
		// not permanently block creation of fresh replacements.
		if r.Confidence < MinConfidenceForInjection {
			continue
		}
		existKW := ruleKeywords(r.ID, r.Tags)
		if len(existKW) < 2 {
			continue
		}
		shared := sharedKeywords(newKW, existKW)
		minLen := len(newKW)
		if len(existKW) < minLen {
			minLen = len(existKW)
		}
		// Both conditions must hold: enough absolute overlap AND enough relative overlap.
		if shared >= 2 && float64(shared)/float64(minLen) >= 0.5 {
			return true, r.ID
		}
	}
	return false, ""
}

// ByStageAndID finds a rule by its stage and ID, avoiding false matches when
// IDs are not unique across stages.
func (rl *RuleLoader) ByStageAndID(stage, id string) *Rule {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	for _, r := range rl.rules {
		if r.ID == id && r.Stage == stage {
			return r
		}
	}
	return nil
}

// HasSemanticMatchAmong checks whether any rule in the provided candidates slice is
// semantically similar to the proposed rule. It applies the same match criteria as
// HasSemanticMatch (shared tokens ≥ 2 AND ratio ≥ 0.5) but against a caller-supplied
// list rather than the loader's disk snapshot. Use this to detect duplicates within a
// synthesis batch before the loader is reloaded.
func (rl *RuleLoader) HasSemanticMatchAmong(id string, tags []string, stage string, candidates []*Rule) (bool, string) {
	newKW := ruleKeywords(id, tags)
	if len(newKW) < 2 {
		return false, ""
	}
	for _, r := range candidates {
		if r.Stage != stage && r.Stage != "global" {
			continue
		}
		existKW := ruleKeywords(r.ID, r.Tags)
		if len(existKW) < 2 {
			continue
		}
		shared := sharedKeywords(newKW, existKW)
		minLen := len(newKW)
		if len(existKW) < minLen {
			minLen = len(existKW)
		}
		if shared >= 2 && float64(shared)/float64(minLen) >= 0.5 {
			return true, r.ID
		}
	}
	return false, ""
}

// IDSummaryForStage returns a compact newline-separated "id: condition" list for all rules
// matching the given stage (and global). Used to provide deduplication context to the synthesizer.
//
// Output is capped at maxIDSummaryBytes to stay well within OS ARG_MAX when the result
// is embedded in the synthesizer prompt passed via -p argv. Each condition snippet is also
// truncated to maxConditionLen characters.
func (rl *RuleLoader) IDSummaryForStage(stage string) string {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	var sb strings.Builder
	omitted := 0
	for _, r := range rl.rules {
		if r.Stage != stage && r.Stage != "global" {
			continue
		}
		// Exclude low-confidence/decayed rules from the dedup hint list.
		// HasSemanticMatch and ForStage already ignore these rules at runtime, so
		// including them here would incorrectly tell the LLM "this rule exists —
		// don't recreate it", permanently blocking fresh replacements for stale rules.
		if r.Confidence < MinConfidenceForInjection {
			continue
		}
		// Build the entry first so we can measure its exact byte size before
		// committing to the output buffer. This prevents a single oversized
		// rule from pushing sb past maxIDSummaryBytes after the pre-check.
		var entry strings.Builder
		entry.WriteString(r.ID)
		if r.Condition != "" {
			cond := r.Condition
			// Truncate by rune count, not byte count, to avoid splitting a
			// multi-byte UTF-8 codepoint mid-sequence (Issue 2).
			if runes := []rune(cond); len(runes) > maxConditionLen {
				cond = string(runes[:maxConditionLen]) + "…"
			}
			entry.WriteString(": ")
			entry.WriteString(cond)
		}
		entry.WriteByte('\n')
		entryStr := entry.String()
		if sb.Len()+len(entryStr) > maxIDSummaryBytes {
			omitted++
			continue
		}
		sb.WriteString(entryStr)
	}
	if omitted > 0 {
		fmt.Fprintf(&sb, "(%d more rules omitted — see rules/ directory for full list)\n", omitted)
	}
	return sb.String()
}

// ruleKeywords extracts meaningful tokens from a rule ID and its tags, dropping stop words.
func ruleKeywords(id string, tags []string) []string {
	stop := map[string]bool{
		"and": true, "or": true, "the": true, "for": true, "in": true,
		"to": true, "a": true, "of": true, "with": true, "on": true,
		"is": true, "not": true, "no": true, "per": true,
		// domain-specific high-frequency tokens that appear in many rules but
		// do not distinguish rule semantics; without these, two rules sharing
		// only "ci"+"tests" would falsely trigger semantic-duplicate suppression.
		"ci": true, "test": true, "tests": true,
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
