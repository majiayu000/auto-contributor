package pipeline

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/majiayu000/auto-contributor/internal/rules"
	"gopkg.in/yaml.v3"
)

const floatTol = 1e-9

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < floatTol
}

func writeDecayRule(t *testing.T, dir string, rule *rules.Rule) {
	t.Helper()
	stageDir := filepath.Join(dir, rule.Stage)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := yaml.Marshal(rule)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, rule.ID+".yaml"), data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readDecayRule(t *testing.T, dir string, rule *rules.Rule) *rules.Rule {
	t.Helper()
	path := filepath.Join(dir, rule.Stage, rule.ID+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var r rules.Rule
	if err := yaml.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &r
}

func newTestPipelineWithRulesDir(t *testing.T, rulesDir string) *Pipeline {
	t.Helper()
	rl := rules.NewRuleLoader(rulesDir)
	if err := rl.Load(); err != nil {
		t.Fatalf("load rules: %v", err)
	}
	return &Pipeline{ruleLoader: rl}
}

// TestApplyDecay_NoLastValidatedAt verifies that synthesized rules without
// last_validated_at have their confidence decayed.
func TestApplyDecay_NoLastValidatedAt(t *testing.T) {
	dir := t.TempDir()
	rule := &rules.Rule{
		ID:         "decay-no-validated",
		Stage:      "scout",
		Severity:   "medium",
		Confidence: 0.8,
		Source:     "synthesized",
		CreatedAt:  "2024-01-01",
		Body:       "Some synthesized rule body.",
	}
	writeDecayRule(t, dir, rule)

	p := newTestPipelineWithRulesDir(t, dir)
	p.applyDecay()

	got := readDecayRule(t, dir, rule)
	want := 0.8 * 0.9
	if !approxEqual(got.Confidence, want) {
		t.Errorf("confidence after decay = %.10f, want %.10f", got.Confidence, want)
	}
}

// TestApplyDecay_RecentLastValidatedAt verifies that rules validated within
// 30 days are NOT decayed.
func TestApplyDecay_RecentLastValidatedAt(t *testing.T) {
	dir := t.TempDir()
	recent := time.Now().AddDate(0, 0, -5).Format("2006-01-02") // 5 days ago
	rule := &rules.Rule{
		ID:              "decay-recent-validated",
		Stage:           "scout",
		Severity:        "medium",
		Confidence:      0.8,
		Source:          "synthesized",
		CreatedAt:       "2024-01-01",
		LastValidatedAt: recent,
		Body:            "Some synthesized rule body.",
	}
	writeDecayRule(t, dir, rule)

	p := newTestPipelineWithRulesDir(t, dir)
	p.applyDecay()

	got := readDecayRule(t, dir, rule)
	if got.Confidence != 0.8 {
		t.Errorf("confidence should not decay when recently validated: got %.4f, want 0.8000", got.Confidence)
	}
}

// TestApplyDecay_OldLastValidatedAt verifies that rules validated more than
// 30 days ago ARE decayed.
func TestApplyDecay_OldLastValidatedAt(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().AddDate(0, 0, -40).Format("2006-01-02") // 40 days ago
	rule := &rules.Rule{
		ID:              "decay-old-validated",
		Stage:           "scout",
		Severity:        "medium",
		Confidence:      0.8,
		Source:          "synthesized",
		CreatedAt:       "2024-01-01",
		LastValidatedAt: old,
		Body:            "Some synthesized rule body.",
	}
	writeDecayRule(t, dir, rule)

	p := newTestPipelineWithRulesDir(t, dir)
	p.applyDecay()

	got := readDecayRule(t, dir, rule)
	want := 0.8 * 0.9
	if !approxEqual(got.Confidence, want) {
		t.Errorf("confidence after decay = %.10f, want %.10f", got.Confidence, want)
	}
}

// TestApplyDecay_ManualRuleNotDecayed verifies that manual rules are never decayed.
func TestApplyDecay_ManualRuleNotDecayed(t *testing.T) {
	dir := t.TempDir()
	rule := &rules.Rule{
		ID:         "decay-manual",
		Stage:      "scout",
		Severity:   "high",
		Confidence: 0.9,
		Source:     "manual",
		CreatedAt:  "2024-01-01",
		Body:       "Manual rule body.",
	}
	writeDecayRule(t, dir, rule)

	p := newTestPipelineWithRulesDir(t, dir)
	p.applyDecay()

	got := readDecayRule(t, dir, rule)
	if got.Confidence != 0.9 {
		t.Errorf("manual rule confidence changed: got %.4f, want 0.9000", got.Confidence)
	}
}

// TestApplyDecay_FloorAtPoint1 verifies that confidence never decays below 0.1.
func TestApplyDecay_FloorAtPoint1(t *testing.T) {
	dir := t.TempDir()
	rule := &rules.Rule{
		ID:         "decay-floor",
		Stage:      "scout",
		Severity:   "low",
		Confidence: 0.11, // one decay step would take it to 0.099, below floor
		Source:     "synthesized",
		CreatedAt:  "2024-01-01",
		Body:       "Almost floored rule.",
	}
	writeDecayRule(t, dir, rule)

	p := newTestPipelineWithRulesDir(t, dir)
	p.applyDecay()

	got := readDecayRule(t, dir, rule)
	if got.Confidence < 0.1 {
		t.Errorf("confidence below floor: got %.4f, want >= 0.1", got.Confidence)
	}
}
