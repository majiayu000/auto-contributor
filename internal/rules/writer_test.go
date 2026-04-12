package rules

import (
	"os"
	"testing"
	"time"
)

func writeTestRule(t *testing.T, dir string, rule *Rule) {
	t.Helper()
	if err := WriteRule(dir, rule); err != nil {
		t.Fatalf("WriteRule: %v", err)
	}
}

func TestUpdateRuleLastValidatedAt(t *testing.T) {
	dir := t.TempDir()

	rule := &Rule{
		ID:         "test-rule-001",
		Stage:      "engineer",
		Severity:   "medium",
		Confidence: 0.8,
		Source:     "synthesized",
		CreatedAt:  "2024-01-01",
		Body:       "Always write tests.",
	}
	writeTestRule(t, dir, rule)

	today := time.Now().Format("2006-01-02")
	if err := UpdateRuleLastValidatedAt(dir, rule.ID, rule.Stage, today); err != nil {
		t.Fatalf("UpdateRuleLastValidatedAt: %v", err)
	}

	// Reload and verify
	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	loaded := rl.ByID(rule.ID)
	if loaded == nil {
		t.Fatal("rule not found after update")
	}
	if loaded.LastValidatedAt != today {
		t.Errorf("LastValidatedAt = %q, want %q", loaded.LastValidatedAt, today)
	}
	// Other fields must be unchanged
	if loaded.Confidence != rule.Confidence {
		t.Errorf("Confidence changed: got %.2f, want %.2f", loaded.Confidence, rule.Confidence)
	}
	if loaded.Body != rule.Body {
		t.Errorf("Body changed")
	}
}

func TestUpdateRuleLastValidatedAt_NotFound(t *testing.T) {
	dir := t.TempDir()
	err := UpdateRuleLastValidatedAt(dir, "nonexistent", "engineer", "2024-01-01")
	if err == nil {
		t.Fatal("expected error for missing rule, got nil")
	}
}

func TestWriteRuleSetsLastValidatedAt(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Format("2006-01-02")

	rule := &Rule{
		ID:              "test-rule-002",
		Stage:           "scout",
		Severity:        "low",
		Confidence:      0.6,
		Source:          "synthesized",
		CreatedAt:       today,
		LastValidatedAt: today,
		Body:            "Check upstream issues first.",
	}
	writeTestRule(t, dir, rule)

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	loaded := rl.ByID(rule.ID)
	if loaded == nil {
		t.Fatal("rule not found")
	}
	if loaded.LastValidatedAt != today {
		t.Errorf("LastValidatedAt = %q, want %q", loaded.LastValidatedAt, today)
	}
}

func TestUpdateRuleLastValidatedAt_PreservesContent(t *testing.T) {
	dir := t.TempDir()
	rule := &Rule{
		ID:            "test-rule-003",
		Stage:         "analyst",
		Severity:      "high",
		Confidence:    0.75,
		Source:        "synthesized",
		CreatedAt:     "2024-06-01",
		EvidenceCount: 10,
		Tags:          []string{"scope", "quality"},
		Condition:     "when PR modifies core logic",
		Body:          "Verify test coverage before approving.",
	}
	writeTestRule(t, dir, rule)

	newDate := "2025-03-15"
	if err := UpdateRuleLastValidatedAt(dir, rule.ID, rule.Stage, newDate); err != nil {
		t.Fatalf("UpdateRuleLastValidatedAt: %v", err)
	}

	data, err := os.ReadFile(dir + "/analyst/" + rule.ID + ".yaml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		"test-rule-003",
		"analyst",
		"synthesized",
		"10",
		"2025-03-15",
		"Verify test coverage",
	} {
		if !containsStr(content, want) {
			t.Errorf("rule file missing expected content %q", want)
		}
	}
}

// TestUpdateRuleConfidence_PreservesLastValidatedAt verifies that updating
// confidence does not overwrite last_validated_at.
func TestUpdateRuleConfidence_PreservesLastValidatedAt(t *testing.T) {
	dir := t.TempDir()
	rule := &Rule{
		ID:              "test-rule-004",
		Stage:           "analyst",
		Severity:        "high",
		Confidence:      0.8,
		Source:          "synthesized",
		CreatedAt:       "2024-06-01",
		LastValidatedAt: "2024-06-15",
		Body:            "Check for CLA requirement.",
	}
	writeTestRule(t, dir, rule)

	if err := UpdateRuleConfidence(dir, rule.ID, rule.Stage, 0.6); err != nil {
		t.Fatalf("UpdateRuleConfidence: %v", err)
	}

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	loaded := rl.ByID(rule.ID)
	if loaded == nil {
		t.Fatal("rule not found after confidence update")
	}
	if loaded.Confidence != 0.6 {
		t.Errorf("Confidence = %.2f, want 0.60", loaded.Confidence)
	}
	if loaded.LastValidatedAt != "2024-06-15" {
		t.Errorf("LastValidatedAt changed: got %q, want %q", loaded.LastValidatedAt, "2024-06-15")
	}
}

// TestIncrementEvidenceCount_StampsLastValidatedAt verifies that IncrementEvidenceCount
// updates both evidence_count and last_validated_at atomically so that applyDecay
// cannot decay a rule that just received fresh evidence in the same cycle.
func TestIncrementEvidenceCount_StampsLastValidatedAt(t *testing.T) {
	dir := t.TempDir()
	staleDate := "2024-01-01"

	rule := &Rule{
		ID:              "test-inc-evidence-001",
		Stage:           "engineer",
		Severity:        "medium",
		Confidence:      0.7,
		Source:          "synthesized",
		CreatedAt:       staleDate,
		LastValidatedAt: staleDate,
		EvidenceCount:   3,
		Body:            "Always run tests before submitting.",
	}
	writeTestRule(t, dir, rule)

	if err := IncrementEvidenceCount(dir, rule.ID, rule.Stage); err != nil {
		t.Fatalf("IncrementEvidenceCount: %v", err)
	}

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	loaded := rl.ByID(rule.ID)
	if loaded == nil {
		t.Fatal("rule not found after IncrementEvidenceCount")
	}
	if loaded.EvidenceCount != 4 {
		t.Errorf("EvidenceCount = %d, want 4", loaded.EvidenceCount)
	}
	today := time.Now().Format("2006-01-02")
	if loaded.LastValidatedAt != today {
		t.Errorf("LastValidatedAt = %q, want %q (today); stale date not updated after merge", loaded.LastValidatedAt, today)
	}
}

// TestWriteRule_UnsafeStage verifies that WriteRule rejects Stage values that
// could escape the rules directory via path traversal (SEC-07).
func TestWriteRule_UnsafeStage(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		stage string
	}{
		{"../../../etc/cron.d"},
		{".."},
		{"scout/../../../etc"},
		{""},
		{"unknown-stage"},
		{"/etc/passwd"},
	}
	for _, tc := range cases {
		rule := &Rule{
			ID:    "safe-id",
			Stage: tc.stage,
			Body:  "test",
		}
		if err := WriteRule(dir, rule); err == nil {
			t.Errorf("WriteRule(%q) should have returned an error for unsafe stage", tc.stage)
		}
	}
}

// TestWriteRule_AllowedStages verifies that WriteRule accepts all valid stage names.
func TestWriteRule_AllowedStages(t *testing.T) {
	dir := t.TempDir()
	stages := []string{"scout", "analyst", "engineer", "reviewer", "submitter", "responder", "global"}
	for _, stage := range stages {
		rule := &Rule{
			ID:    "test-stage-rule",
			Stage: stage,
			Body:  "test body",
		}
		if err := WriteRule(dir, rule); err != nil {
			t.Errorf("WriteRule with valid stage %q returned unexpected error: %v", stage, err)
		}
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
