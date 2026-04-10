package pipeline

import (
	"testing"
	"time"

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

// TestApplyDecay_ReducesConfidenceWithoutValidation verifies that a synthesized rule
// with no last_validated_at has its confidence decayed by applyDecay.
func TestApplyDecay_ReducesConfidenceWithoutValidation(t *testing.T) {
	dir := t.TempDir()

	rule := &rules.Rule{
		ID:         "decay-test-no-validation",
		Stage:      "engineer",
		Severity:   "medium",
		Confidence: 0.8,
		Source:     "synthesized",
		CreatedAt:  "2024-01-01",
		Body:       "Write unit tests.",
	}
	writeRule(t, dir, rule)

	p := newTestPipeline(t, dir)
	p.applyDecay()

	got := loadRule(t, p.ruleLoader, rule.ID)
	want := 0.8 * 0.9
	if got.Confidence >= 0.8 {
		t.Errorf("expected confidence to decay below 0.80, got %.4f", got.Confidence)
	}
	if got.Confidence < want-0.001 || got.Confidence > want+0.001 {
		t.Errorf("confidence = %.4f, want %.4f", got.Confidence, want)
	}
}

// TestApplyDecay_ExemptsRecentlyValidatedRules verifies that a rule with
// last_validated_at within 30 days is NOT decayed.
func TestApplyDecay_ExemptsRecentlyValidatedRules(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Format("2006-01-02")

	rule := &rules.Rule{
		ID:              "decay-test-recent-validation",
		Stage:           "analyst",
		Severity:        "high",
		Confidence:      0.75,
		Source:          "synthesized",
		CreatedAt:       "2024-01-01",
		LastValidatedAt: today,
		Body:            "Check for existing PRs.",
	}
	writeRule(t, dir, rule)

	p := newTestPipeline(t, dir)
	p.applyDecay()

	got := loadRule(t, p.ruleLoader, rule.ID)
	if got.Confidence != rule.Confidence {
		t.Errorf("confidence changed for recently-validated rule: got %.4f, want %.4f",
			got.Confidence, rule.Confidence)
	}
}

// TestApplyDecay_ExemptsManualRules verifies that manual rules are never decayed.
func TestApplyDecay_ExemptsManualRules(t *testing.T) {
	dir := t.TempDir()

	rule := &rules.Rule{
		ID:         "decay-test-manual",
		Stage:      "scout",
		Severity:   "critical",
		Confidence: 0.9,
		Source:     "manual",
		CreatedAt:  "2024-01-01",
		Body:       "Never skip triage.",
	}
	writeRule(t, dir, rule)

	p := newTestPipeline(t, dir)
	p.applyDecay()

	got := loadRule(t, p.ruleLoader, rule.ID)
	if got.Confidence != rule.Confidence {
		t.Errorf("manual rule confidence should not change: got %.4f, want %.4f",
			got.Confidence, rule.Confidence)
	}
}

// TestApplyDecay_FloorsAtMinimum verifies that repeated decay does not push
// confidence below the 0.1 floor.
func TestApplyDecay_FloorsAtMinimum(t *testing.T) {
	dir := t.TempDir()

	rule := &rules.Rule{
		ID:         "decay-test-floor",
		Stage:      "reviewer",
		Severity:   "low",
		Confidence: 0.11,
		Source:     "synthesized",
		CreatedAt:  "2024-01-01",
		Body:       "Low-confidence rule near floor.",
	}
	writeRule(t, dir, rule)

	p := newTestPipeline(t, dir)
	// Run decay multiple times to ensure we hit the floor
	for i := 0; i < 5; i++ {
		p.applyDecay()
		if err := p.ruleLoader.Reload(); err != nil {
			t.Fatalf("Reload: %v", err)
		}
	}

	got := loadRule(t, p.ruleLoader, rule.ID)
	if got.Confidence < 0.1 {
		t.Errorf("confidence went below floor: %.4f", got.Confidence)
	}
}

// TestApplyDecay_ExemptsStaleValidation verifies that a rule with
// last_validated_at older than 30 days IS decayed.
func TestApplyDecay_ExemptsStaleValidation(t *testing.T) {
	dir := t.TempDir()
	staleDate := time.Now().AddDate(0, 0, -31).Format("2006-01-02")

	rule := &rules.Rule{
		ID:              "decay-test-stale-validation",
		Stage:           "submitter",
		Severity:        "medium",
		Confidence:      0.7,
		Source:          "synthesized",
		CreatedAt:       "2024-01-01",
		LastValidatedAt: staleDate,
		Body:            "Stale validation — should decay.",
	}
	writeRule(t, dir, rule)

	p := newTestPipeline(t, dir)
	p.applyDecay()

	got := loadRule(t, p.ruleLoader, rule.ID)
	if got.Confidence >= rule.Confidence {
		t.Errorf("expected confidence to decay for stale validation, got %.4f (was %.4f)",
			got.Confidence, rule.Confidence)
	}
}

// TestApplySynthesisResult_NewRuleSetsLastValidatedAt verifies that new rules written
// by applySynthesisResult have last_validated_at populated.
func TestApplySynthesisResult_NewRuleSetsLastValidatedAt(t *testing.T) {
	dir := t.TempDir()
	p := newTestPipeline(t, dir)

	result := &SynthesizerResult{
		NewRules: []SynthesizedRule{
			{
				ID:            "synth-new-rule-001",
				Stage:         "engineer",
				Severity:      "medium",
				Confidence:    0.6,
				EvidenceCount: 5,
				Body:          "Ensure CI passes before submitting.",
			},
		},
	}

	applied := p.applySynthesisResult("engineer", result)
	if applied.newCount != 1 {
		t.Fatalf("expected 1 new rule, got %d", applied.newCount)
	}

	// Reload from disk
	got := loadRule(t, p.ruleLoader, "synth-new-rule-001")
	if got.LastValidatedAt == "" {
		t.Error("new synthesized rule has empty last_validated_at")
	}
	today := time.Now().Format("2006-01-02")
	if got.LastValidatedAt != today {
		t.Errorf("last_validated_at = %q, want %q", got.LastValidatedAt, today)
	}
}

// TestApplySynthesisResult_UpdatedRuleSetsLastValidatedAt verifies that updating
// an existing rule's confidence also refreshes last_validated_at.
func TestApplySynthesisResult_UpdatedRuleSetsLastValidatedAt(t *testing.T) {
	dir := t.TempDir()

	// Pre-create a synthesized rule without last_validated_at
	existing := &rules.Rule{
		ID:         "synth-existing-rule-001",
		Stage:      "analyst",
		Severity:   "medium",
		Confidence: 0.5,
		Source:     "synthesized",
		CreatedAt:  "2024-01-01",
		Body:       "Analyse scope carefully.",
	}
	writeRule(t, dir, existing)

	p := newTestPipeline(t, dir)

	result := &SynthesizerResult{
		UpdatedRules: []RuleUpdate{
			{
				ID:            existing.ID,
				NewConfidence: 0.65,
				Reason:        "additional positive evidence",
			},
		},
	}

	applied := p.applySynthesisResult("analyst", result)
	if applied.updatedCount != 1 {
		t.Fatalf("expected 1 updated rule, got %d", applied.updatedCount)
	}

	got := loadRule(t, p.ruleLoader, existing.ID)
	if got.LastValidatedAt == "" {
		t.Error("updated synthesized rule has empty last_validated_at")
	}
	today := time.Now().Format("2006-01-02")
	if got.LastValidatedAt != today {
		t.Errorf("last_validated_at = %q, want %q", got.LastValidatedAt, today)
	}
	if got.Confidence != 0.65 {
		t.Errorf("confidence = %.2f, want 0.65", got.Confidence)
	}
}
