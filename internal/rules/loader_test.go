package rules

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRuleYAML writes a minimal rule YAML file to dir/subdir/name.yaml.
func writeRuleYAML(t *testing.T, dir, subdir, name, content string) string {
	t.Helper()
	d := filepath.Join(dir, subdir)
	if err := os.MkdirAll(d, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

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

// TestQuarantineInvalidStage verifies that readFromDisk deletes rule files whose
// Stage field is not in allowedStages and does not load them into memory.
// This prevents legacy or manually-placed files with bad stages from being
// re-injected into prompts on every Reload and from blocking the retirement path.
func TestQuarantineInvalidStage(t *testing.T) {
	dir := t.TempDir()

	good := writeRuleYAML(t, dir, "engineer", "valid-rule.yaml",
		"id: valid-rule\nstage: engineer\nbody: keep this\nconfidence: 0.8\n")
	bad := writeRuleYAML(t, dir, "badstage", "poisoned.yaml",
		"id: poisoned\nstage: badstage\nbody: should be deleted\nconfidence: 0.9\n")

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	all := rl.All()
	for _, r := range all {
		if r.Stage == "badstage" {
			t.Errorf("rule with invalid stage %q should not be loaded", r.Stage)
		}
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Error("bad-stage rule file should have been quarantined (deleted) from disk")
	}
	if _, err := os.Stat(good); err != nil {
		t.Errorf("valid rule file should remain on disk: %v", err)
	}
}

// TestQuarantineUnsafeID verifies that readFromDisk deletes rule files whose
// ID field contains path traversal characters and does not load them into memory.
// A rule with stage: engineer but id: ../../../etc/cron.d/pwn would otherwise
// be injected into prompts but can never be decayed, updated, or retired.
func TestQuarantineUnsafeID(t *testing.T) {
	dir := t.TempDir()

	good := writeRuleYAML(t, dir, "engineer", "safe-rule.yaml",
		"id: safe-rule\nstage: engineer\nbody: keep this\nconfidence: 0.8\n")
	bad := writeRuleYAML(t, dir, "engineer", "traversal.yaml",
		"id: \"../../../etc/cron.d/pwn\"\nstage: engineer\nbody: injected\nconfidence: 0.9\n")

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	all := rl.All()
	for _, r := range all {
		if r.ID == "../../../etc/cron.d/pwn" {
			t.Error("rule with traversal ID should not be loaded")
		}
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Error("traversal-ID rule file should have been quarantined (deleted) from disk")
	}
	if _, err := os.Stat(good); err != nil {
		t.Errorf("valid rule file should remain on disk: %v", err)
	}
}
