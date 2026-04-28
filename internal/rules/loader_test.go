package rules

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/pkg/models"
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

func TestPromptSnapshotForRulesUsesSelectedRules(t *testing.T) {
	selected := []*Rule{
		{ID: "second", Stage: "engineer", Confidence: 0.7, Body: "apply the targeted fix"},
		{ID: "first", Stage: "global", Confidence: 0.9, Body: "always explain the root cause"},
	}

	ids, promptText := promptSnapshotForRules(selected)
	wantIDs := []string{"engineer/second", "global/first"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("ids = %v, want %v", ids, wantIDs)
	}
	if strings.Index(promptText, "### second") > strings.Index(promptText, "### first") {
		t.Fatalf("prompt order did not preserve selection order:\n%s", promptText)
	}
	if !strings.Contains(promptText, "apply the targeted fix") || !strings.Contains(promptText, "always explain the root cause") {
		t.Fatalf("prompt missing selected rule bodies:\n%s", promptText)
	}
}

func TestRuleRetrieverRetrieveFiltersStagesAndReranks(t *testing.T) {
	dir := t.TempDir()
	writeRuleYAML(t, dir, "engineer", "high-q.yaml",
		"id: high-q\nstage: engineer\nconfidence: 0.9\nq_value: 0.9\nbody: prefer the proven root-cause fix\n")
	writeRuleYAML(t, dir, "engineer", "high-sim.yaml",
		"id: high-sim\nstage: engineer\nconfidence: 0.9\nq_value: 0.51\nbody: match the exact failing parser path\n")
	writeRuleYAML(t, dir, "global", "legacy-zero.yaml",
		"id: legacy-zero\nstage: global\nconfidence: 0.9\nq_value: 0\nbody: keep changes minimal and well scoped\n")
	writeRuleYAML(t, dir, "reviewer", "offstage.yaml",
		"id: offstage\nstage: reviewer\nconfidence: 0.9\nq_value: 1.0\nbody: reviewer-only guidance should not leak\n")

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	store := &fakeRuleEmbeddingStore{
		isPostgres: true,
		candidates: []db.RuleEmbeddingCandidate{
			{RuleKey: "engineer/high-sim", Stage: "engineer", Similarity: 0.82},
			{RuleKey: "engineer/high-q", Stage: "engineer", Similarity: 0.80},
			{RuleKey: "global/legacy-zero", Stage: "global", Similarity: 0.81},
			{RuleKey: "reviewer/offstage", Stage: "reviewer", Similarity: 0.99},
		},
	}

	cfg := config.Default()
	cfg.SemanticRetrievalEnabled = true
	cfg.SemanticRetrievalLambda = 0.5
	cfg.SemanticRetrievalTopK = 5
	cfg.SemanticRetrievalPhaseA = 20
	cfg.SemanticRetrievalProvider = "local"
	cfg.SemanticRetrievalModel = "hash-v1"

	retriever, err := NewRuleRetriever(cfg, store, rl)
	if err != nil {
		t.Fatalf("NewRuleRetriever() failed: %v", err)
	}

	ids, promptText, err := retriever.Retrieve("engineer", &models.Issue{
		Repo:        "owner/repo",
		Language:    "go",
		Labels:      "[\"bug\"]",
		Title:       "panic in parser",
		Body:        "nil pointer when parsing malformed input",
		IssueNumber: 12,
	})
	if err != nil {
		t.Fatalf("Retrieve() failed: %v", err)
	}

	if !reflect.DeepEqual(store.lastStages, []string{"engineer", "global"}) {
		t.Fatalf("stages = %v, want [engineer global]", store.lastStages)
	}
	wantIDs := []string{"engineer/high-q", "engineer/high-sim", "global/legacy-zero"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("ids = %v, want %v", ids, wantIDs)
	}
	if strings.Contains(promptText, "reviewer-only guidance should not leak") {
		t.Fatalf("off-stage rule leaked into prompt:\n%s", promptText)
	}
	if strings.Index(promptText, "### high-q") > strings.Index(promptText, "### high-sim") {
		t.Fatalf("rerank did not prioritize higher-Q near-tie:\n%s", promptText)
	}
}

func TestRuleRetrieverSyncUpsertsChangedAndDeletesMissing(t *testing.T) {
	dir := t.TempDir()
	writeRuleYAML(t, dir, "global", "keep.yaml",
		"id: keep\nstage: global\nconfidence: 0.9\nbody: stable rule text\n")
	writeRuleYAML(t, dir, "engineer", "update.yaml",
		"id: update\nstage: engineer\nconfidence: 0.9\nbody: changed body text\n")
	writeRuleYAML(t, dir, "engineer", "new.yaml",
		"id: new\nstage: engineer\nconfidence: 0.9\nbody: newly added rule\n")

	rl := NewRuleLoader(dir)
	if err := rl.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	keepRule := rl.ByStageAndID("global", "keep")
	store := &fakeRuleEmbeddingStore{
		isPostgres: true,
		hashes: map[string]string{
			"global/keep":      contentHashForModel("hash-v1", buildRuleEmbeddingText(keepRule)),
			"engineer/update":  "outdated-hash",
			"engineer/deleted": "stale",
		},
	}

	cfg := config.Default()
	cfg.SemanticRetrievalEnabled = true
	cfg.SemanticRetrievalProvider = "local"
	cfg.SemanticRetrievalModel = "hash-v1"

	retriever, err := NewRuleRetriever(cfg, store, rl)
	if err != nil {
		t.Fatalf("NewRuleRetriever() failed: %v", err)
	}

	if err := retriever.Sync(); err != nil {
		t.Fatalf("Sync() failed: %v", err)
	}

	if len(store.upserts) != 2 {
		t.Fatalf("upserts = %d, want 2", len(store.upserts))
	}
	if store.upserts[0].ruleKey != "engineer/new" && store.upserts[1].ruleKey != "engineer/new" {
		t.Fatalf("new rule was not upserted: %+v", store.upserts)
	}
	if store.upserts[0].ruleKey != "engineer/update" && store.upserts[1].ruleKey != "engineer/update" {
		t.Fatalf("changed rule was not upserted: %+v", store.upserts)
	}
	wantDeleted := []string{"engineer/new", "engineer/update", "global/keep"}
	if !reflect.DeepEqual(store.deletedKeys, wantDeleted) {
		t.Fatalf("deletedKeys = %v, want %v", store.deletedKeys, wantDeleted)
	}
}

type fakeRuleEmbeddingStore struct {
	isPostgres  bool
	hashes      map[string]string
	candidates  []db.RuleEmbeddingCandidate
	upserts     []fakeRuleEmbeddingUpsert
	deletedKeys []string
	lastStages  []string
}

type fakeRuleEmbeddingUpsert struct {
	ruleKey     string
	stage       string
	contentHash string
	modelName   string
}

func (s *fakeRuleEmbeddingStore) IsPostgres() bool {
	return s.isPostgres
}

func (s *fakeRuleEmbeddingStore) GetRuleEmbeddingHashes() (map[string]string, error) {
	out := make(map[string]string, len(s.hashes))
	for key, value := range s.hashes {
		out[key] = value
	}
	return out, nil
}

func (s *fakeRuleEmbeddingStore) UpsertRuleEmbedding(ruleKey, stage, contentHash string, _ []float64, modelName string) error {
	s.upserts = append(s.upserts, fakeRuleEmbeddingUpsert{
		ruleKey:     ruleKey,
		stage:       stage,
		contentHash: contentHash,
		modelName:   modelName,
	})
	return nil
}

func (s *fakeRuleEmbeddingStore) DeleteRuleEmbeddingsExcept(ruleKeys []string) error {
	s.deletedKeys = append([]string(nil), ruleKeys...)
	return nil
}

func (s *fakeRuleEmbeddingStore) FindRuleEmbeddingCandidates(stages []string, _ []float64, _ int) ([]db.RuleEmbeddingCandidate, error) {
	s.lastStages = append([]string(nil), stages...)
	return append([]db.RuleEmbeddingCandidate(nil), s.candidates...), nil
}
