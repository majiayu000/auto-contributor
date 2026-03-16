package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_LoadAndRender(t *testing.T) {
	dir := t.TempDir()

	// Write a test template
	content := `Hello {{ .Name }}, issue #{{ .Number }}`
	if err := os.WriteFile(filepath.Join(dir, "test.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewStore(dir)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := s.Render("test", map[string]any{
		"Name":   "Alice",
		"Number": 42,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := "Hello Alice, issue #42"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStore_RenderMissing(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}

	_, err := s.Render("nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestStore_Names(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "skip.txt"} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644)
	}

	s := NewStore(dir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}

	names := s.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 templates, got %d: %v", len(names), names)
	}
}
