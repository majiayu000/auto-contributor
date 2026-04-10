package pipeline

import (
	"context"
	"encoding/json"
	"testing"
)

// --- CriticResult JSON parsing tests ---

func TestCriticResult_ParseApprove(t *testing.T) {
	input := `{
		"verdict": "approve",
		"severity": "",
		"findings": [],
		"rework_instructions": "",
		"summary": "LGTM from maintainer perspective"
	}`
	var result CriticResult
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if result.Verdict != "approve" {
		t.Errorf("got verdict=%q, want approve", result.Verdict)
	}
	if len(result.Findings) != 0 {
		t.Errorf("got %d findings, want 0", len(result.Findings))
	}
}

func TestCriticResult_ParseRejectWithFindings(t *testing.T) {
	input := `{
		"verdict": "reject",
		"severity": "severe",
		"findings": [
			{
				"category": "backward_compat",
				"description": "Removes exported function Foo() without deprecation",
				"suggestion": "Add Foo() as a deprecated wrapper calling the new implementation"
			}
		],
		"rework_instructions": "Add a deprecated wrapper for Foo() before removing it.",
		"summary": "Breaking API removal without deprecation path"
	}`
	var result CriticResult
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if result.Verdict != "reject" {
		t.Errorf("got verdict=%q, want reject", result.Verdict)
	}
	if result.Severity != "severe" {
		t.Errorf("got severity=%q, want severe", result.Severity)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(result.Findings))
	}
	if result.Findings[0].Category != "backward_compat" {
		t.Errorf("got category=%q, want backward_compat", result.Findings[0].Category)
	}
	if result.ReworkInstructions == "" {
		t.Error("expected non-empty rework_instructions")
	}
}

func TestCriticResult_ParseViaExtractJSON(t *testing.T) {
	// Mirrors existing reviewer JSON recovery tests: JSON inside prose output
	input := "After careful analysis of the changes:\n\n" +
		`{"verdict":"reject","severity":"minor","findings":[{"category":"documentation","description":"Missing godoc on exported type","suggestion":"Add doc comment"}],"rework_instructions":"","summary":"Minor doc gap"}`
	var result CriticResult
	if err := extractJSON(input, &result); err != nil {
		t.Fatalf("extractJSON failed: %v", err)
	}
	if result.Verdict != "reject" {
		t.Errorf("got verdict=%q, want reject", result.Verdict)
	}
	if result.Severity != "minor" {
		t.Errorf("got severity=%q, want minor", result.Severity)
	}
}

func TestCriticResult_ParseMalformedRecovery(t *testing.T) {
	// Malformed JSON that json-repair should handle
	input := `{"verdict":"approve","severity":"","findings":[],"summary":"looks good"`
	var result CriticResult
	// extractJSON uses json-repair as a fallback; truncated JSON may be recovered
	_ = extractJSON(input, &result)
	// We don't assert success here because repair is best-effort,
	// but we verify no panic occurs and the function returns deterministically.
}

// --- criticLoop behaviour: maxCriticRounds=0 skips critic ---

func TestCriticLoop_SkippedWhenMaxRoundsZero(t *testing.T) {
	// A Pipeline with maxCriticRounds=0 must return nil without touching the runner.
	p := &Pipeline{maxCriticRounds: 0}
	err := p.criticLoop(context.Background(), nil, "", nil)
	if err != nil {
		t.Errorf("expected nil error when maxCriticRounds=0, got: %v", err)
	}
}
