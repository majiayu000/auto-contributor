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
