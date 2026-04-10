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

	// Verify file on disk still has all fields
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
		if !contains(content, want) {
			t.Errorf("rule file missing expected content %q", want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
