package rules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRules(t *testing.T) {
	// Use the actual rules directory
	rulesDir := filepath.Join("..", "..", "rules")
	if _, err := os.Stat(rulesDir); os.IsNotExist(err) {
		t.Skip("rules directory not found")
	}

	rl := NewRuleLoader(rulesDir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	all := rl.All()
	if len(all) == 0 {
		t.Fatal("expected at least 1 rule, got 0")
	}

	t.Logf("loaded %d rules", len(all))
	for _, r := range all {
		t.Logf("  [%s] %s (stage=%s, confidence=%.2f)", r.Severity, r.ID, r.Stage, r.Confidence)
	}
}

func TestForStage(t *testing.T) {
	rulesDir := filepath.Join("..", "..", "rules")
	if _, err := os.Stat(rulesDir); os.IsNotExist(err) {
		t.Skip("rules directory not found")
	}

	rl := NewRuleLoader(rulesDir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	scout := rl.ForStage("scout")
	if len(scout) == 0 {
		t.Fatal("expected scout rules, got 0")
	}

	// Scout rules should include global rules
	hasGlobal := false
	for _, r := range scout {
		if r.Stage == "global" {
			hasGlobal = true
		}
	}
	if !hasGlobal {
		t.Error("scout rules should include global rules")
	}

	// Scout rules should NOT include engineer-only rules
	for _, r := range scout {
		if r.Stage == "engineer" {
			t.Errorf("scout rules should not include engineer rule %s", r.ID)
		}
	}
}

func TestFormatForPrompt(t *testing.T) {
	rulesDir := filepath.Join("..", "..", "rules")
	if _, err := os.Stat(rulesDir); os.IsNotExist(err) {
		t.Skip("rules directory not found")
	}

	rl := NewRuleLoader(rulesDir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	output := rl.FormatForPrompt("scout")
	if output == "" {
		t.Fatal("expected non-empty prompt output for scout")
	}
	if len(output) > MaxPromptChars+200 { // allow some margin for headers
		t.Errorf("prompt output too long: %d chars", len(output))
	}
	t.Logf("scout prompt (%d chars):\n%s", len(output), output)
}

func TestEmptyDir(t *testing.T) {
	rl := NewRuleLoader("")
	if err := rl.Load(); err != nil {
		t.Fatalf("Load() on empty dir should not error: %v", err)
	}
	if len(rl.All()) != 0 {
		t.Error("expected 0 rules for empty dir")
	}
	if rl.FormatForPrompt("scout") != "" {
		t.Error("expected empty prompt for empty dir")
	}
}

func TestConfidenceFilter(t *testing.T) {
	rulesDir := filepath.Join("..", "..", "rules")
	if _, err := os.Stat(rulesDir); os.IsNotExist(err) {
		t.Skip("rules directory not found")
	}

	rl := NewRuleLoader(rulesDir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	for _, r := range rl.ForStage("scout") {
		if r.Confidence < MinConfidenceForInjection {
			t.Errorf("rule %s has confidence %.2f below threshold %.2f", r.ID, r.Confidence, MinConfidenceForInjection)
		}
	}
}

func TestHasSimilarInStage_DetectsDuplicate(t *testing.T) {
	dir := t.TempDir()

	existing := &Rule{
		ID:         "reviewer-approval-not-predictive",
		Stage:      "reviewer",
		Severity:   "medium",
		Confidence: 0.7,
		Source:     "synthesized",
		CreatedAt:  "2026-01-01",
		// Keywords: reviewer, approval, predict, whether, merged, treat, merge, signal
		Body: "Reviewer approval does not predict whether the PR will be merged. Do not treat approval as a merge signal.",
	}
	if err := WriteRule(dir, existing); err != nil {
		t.Fatalf("WriteRule: %v", err)
	}

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Near-duplicate: shares reviewer, approval, predict, whether, merged, merge
	// Jaccard ≈ 6/10 = 0.6 — above the 0.4 detection threshold.
	dupBody := "Reviewer approval does not predict whether a PR gets merged. Approval is not a merge predictor."
	got, ok := rl.HasSimilarInStage("reviewer", dupBody)
	if !ok {
		t.Fatal("expected HasSimilarInStage to return true for a semantic duplicate")
	}
	if got.ID != existing.ID {
		t.Errorf("expected similar rule ID %q, got %q", existing.ID, got.ID)
	}
}

func TestHasSimilarInStage_AllowsDistinctRule(t *testing.T) {
	dir := t.TempDir()

	existing := &Rule{
		ID:         "blacklist-unresponsive-repos",
		Stage:      "scout",
		Severity:   "high",
		Confidence: 0.8,
		Source:     "synthesized",
		CreatedAt:  "2026-01-01",
		Body:       "Skip repositories that have historically ignored contributor PRs for 14+ days.",
	}
	if err := WriteRule(dir, existing); err != nil {
		t.Fatalf("WriteRule: %v", err)
	}

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Completely different concept — should NOT be flagged as duplicate.
	distinctBody := "Prefer issues labelled good-first-issue when the repository has many open pull requests."
	_, ok := rl.HasSimilarInStage("scout", distinctBody)
	if ok {
		t.Error("expected HasSimilarInStage to return false for a distinct rule")
	}
}

func TestHasSimilarInStage_IgnoresDifferentStage(t *testing.T) {
	dir := t.TempDir()

	// Rule in engineer stage
	existing := &Rule{
		ID:         "minimal-diff-only",
		Stage:      "engineer",
		Severity:   "medium",
		Confidence: 0.7,
		Source:     "synthesized",
		CreatedAt:  "2026-01-01",
		Body:       "Keep diffs minimal. Only change files directly required to fix the issue.",
	}
	if err := WriteRule(dir, existing); err != nil {
		t.Fatalf("WriteRule: %v", err)
	}

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Same body but queried for "reviewer" stage — should not match engineer rule.
	_, ok := rl.HasSimilarInStage("reviewer", existing.Body)
	if ok {
		t.Error("HasSimilarInStage should not match rules from a different stage")
	}
}
