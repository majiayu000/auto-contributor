package pipeline

import (
	"testing"

	"github.com/majiayu000/auto-contributor/internal/rules"
)

// newTestPipeline returns a Pipeline wired to a temp rules directory.
func newTestPipeline(t *testing.T, rulesDir string) *Pipeline {
	t.Helper()
	rl := rules.NewRuleLoader(rulesDir)
	if err := rl.Load(); err != nil {
		t.Fatalf("RuleLoader.Load: %v", err)
	}
	return &Pipeline{ruleLoader: rl}
}

// writeRule is a test helper that writes a rule and fatals on error.
func writeRule(t *testing.T, dir string, r *rules.Rule) {
	t.Helper()
	if err := rules.WriteRule(dir, r); err != nil {
		t.Fatalf("WriteRule: %v", err)
	}
}

// loadRule reloads the loader and returns the rule by ID, or fatals if not found.
func loadRule(t *testing.T, rl *rules.RuleLoader, id string) *rules.Rule {
	t.Helper()
	if err := rl.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	r := rl.ByID(id)
	if r == nil {
		t.Fatalf("rule %q not found after reload", id)
	}
	return r
}

// TestApplySynthesisResult_NormalizesConfidence verifies that confidence values are
// rounded to one decimal place and clamped, preventing decayed fractional values
// (e.g. 0.5019165) from being written to new rules.
// Note: rules with confidence < 0.3 are rejected before normalization; test cases
// that reach normalization must have confidence >= 0.3.
func TestApplySynthesisResult_NormalizesConfidence(t *testing.T) {
	cases := []struct {
		name     string
		input    float64
		wantConf float64
	}{
		{"decayed fraction", 0.5019165, 0.5},
		{"already round", 0.7, 0.7},
		{"above max — clamped to 0.9", 0.95, 0.9},
		{"rounds up", 0.65, 0.7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := newTestPipeline(t, dir)

			result := &SynthesizerResult{
				NewRules: []SynthesizedRule{
					{
						ID:            "conf-norm-test",
						Stage:         "engineer",
						Severity:      "medium",
						Confidence:    tc.input,
						EvidenceCount: 5,
						Body:          "Unique body text for confidence normalization test.",
					},
				},
			}

			applied := p.applySynthesisResult("engineer", result)
			if applied.newCount != 1 {
				t.Fatalf("expected 1 new rule, got %d", applied.newCount)
			}

			got := loadRule(t, p.ruleLoader, "conf-norm-test")
			if got.Confidence != tc.wantConf {
				t.Errorf("confidence = %.7f, want %.1f", got.Confidence, tc.wantConf)
			}
		})
	}
}

// TestApplySynthesisResult_SkipsSemanticDuplicate verifies that a new rule whose
// body shares substantial keyword overlap (Jaccard ≥ 0.4) with an existing rule
// is not written to disk.
func TestApplySynthesisResult_SkipsSemanticDuplicate(t *testing.T) {
	dir := t.TempDir()

	existing := &rules.Rule{
		ID:         "reviewer-approval-not-predictive",
		Stage:      "reviewer",
		Severity:   "medium",
		Confidence: 0.7,
		Source:     "synthesized",
		CreatedAt:  "2026-01-01",
		// Keywords: reviewer, approval, predict, whether, merged, treat, merge, signal
		Body: "Reviewer approval does not predict whether the PR will be merged. Do not treat approval as a merge signal.",
	}
	writeRule(t, dir, existing)

	p := newTestPipeline(t, dir)

	result := &SynthesizerResult{
		NewRules: []SynthesizedRule{
			{
				ID:            "reviewer-approval-2",
				Stage:         "reviewer",
				Severity:      "medium",
				Confidence:    0.6,
				EvidenceCount: 4,
				// Keywords: reviewer, approval, predict, whether, merged, merge, predictor
				// Jaccard with existing ≈ 6/10 = 0.6 — above the 0.4 threshold.
				Body: "Reviewer approval does not predict whether a PR gets merged. Approval is not a merge predictor.",
			},
		},
	}

	applied := p.applySynthesisResult("reviewer", result)
	if applied.newCount != 0 {
		t.Errorf("expected 0 new rules (semantic duplicate), got %d", applied.newCount)
	}
}

// TestApplySynthesisResult_WritesDistinctRule verifies that a genuinely new rule
// is written even when similar-looking rules already exist in a different stage.
func TestApplySynthesisResult_WritesDistinctRule(t *testing.T) {
	dir := t.TempDir()

	// Existing rule in a different stage — must not block the new one.
	existing := &rules.Rule{
		ID:         "engineer-minimal-diff",
		Stage:      "engineer",
		Severity:   "medium",
		Confidence: 0.7,
		Source:     "synthesized",
		CreatedAt:  "2026-01-01",
		Body:       "Keep changes minimal. Only touch files required by the issue.",
	}
	writeRule(t, dir, existing)

	p := newTestPipeline(t, dir)

	result := &SynthesizerResult{
		NewRules: []SynthesizedRule{
			{
				ID:            "scout-skip-unresponsive",
				Stage:         "scout",
				Severity:      "high",
				Confidence:    0.8,
				EvidenceCount: 6,
				Body:          "Skip repositories that have ignored contributor PRs for 14 or more days without any feedback.",
			},
		},
	}

	applied := p.applySynthesisResult("scout", result)
	if applied.newCount != 1 {
		t.Errorf("expected 1 new rule (distinct concept, different stage), got %d", applied.newCount)
	}
}
