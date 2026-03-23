package pipeline

import (
	"testing"
)

func TestExtractJSON_PlainJSON(t *testing.T) {
	var dest map[string]any
	err := extractJSON(`{"verdict":"PROCEED","score":0.9}`, &dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["verdict"] != "PROCEED" {
		t.Errorf("got verdict=%v, want PROCEED", dest["verdict"])
	}
}

func TestExtractJSON_MarkdownFence(t *testing.T) {
	input := "Here is my analysis:\n\n```json\n{\"verdict\":\"PROCEED\",\"score\":0.9}\n```\n"
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["verdict"] != "PROCEED" {
		t.Errorf("got verdict=%v, want PROCEED", dest["verdict"])
	}
}

func TestExtractJSON_MarkdownFenceUppercase(t *testing.T) {
	input := "```JSON\n{\"ok\":true}\n```"
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["ok"] != true {
		t.Errorf("got ok=%v, want true", dest["ok"])
	}
}

func TestExtractJSON_ProseThenJSON(t *testing.T) {
	// Prose with a brace-like token before the real JSON
	input := "Use map[string]int{} for counting.\n\n{\"verdict\":\"SKIP\"}"
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["verdict"] != "SKIP" {
		t.Errorf("got verdict=%v, want SKIP", dest["verdict"])
	}
}

func TestExtractJSON_LastObjectWins(t *testing.T) {
	// Two JSON objects; the last is the structured output
	input := `Some context {"noise":1} and then the real output {"verdict":"PROCEED","score":0.8}`
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["verdict"] != "PROCEED" {
		t.Errorf("got verdict=%v, want PROCEED", dest["verdict"])
	}
}

func TestExtractJSON_NoJSON(t *testing.T) {
	err := extractJSON("no json here at all", &map[string]any{})
	if err == nil {
		t.Fatal("expected error for input with no JSON")
	}
}

func TestExtractJSON_BracesInStrings(t *testing.T) {
	// Braces inside string values should not confuse the depth counter
	input := `{"key":"value with } brace","ok":true}`
	var dest map[string]any
	if err := extractJSON(input, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest["ok"] != true {
		t.Errorf("got ok=%v, want true", dest["ok"])
	}
}

func TestExtractObjectAt_Incomplete(t *testing.T) {
	s := extractObjectAt(`{"unclosed":`, 0)
	if s != "" {
		t.Errorf("expected empty string for incomplete object, got %q", s)
	}
}

func TestExtractFromCodeFence_NoFence(t *testing.T) {
	s := extractFromCodeFence("no fences here")
	if s != "" {
		t.Errorf("expected empty string, got %q", s)
	}
}
