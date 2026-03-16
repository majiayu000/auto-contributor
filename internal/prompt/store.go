package prompt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"text/template"
)

// Store loads and renders prompt templates from the prompts/ directory.
type Store struct {
	dir       string
	mu        sync.RWMutex
	templates map[string]*template.Template
}

// NewStore creates a Store that reads .md files from dir.
func NewStore(dir string) *Store {
	return &Store{
		dir:       dir,
		templates: make(map[string]*template.Template),
	}
}

// Load parses all .md files in the prompts directory.
// Call once at startup; templates are cached.
func (s *Store) Load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read prompts dir %s: %w", s.dir, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}

		name := e.Name()[:len(e.Name())-3] // strip .md
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}

		tmpl, err := template.New(name).Parse(string(data))
		if err != nil {
			return fmt.Errorf("parse template %s: %w", name, err)
		}

		s.templates[name] = tmpl
	}

	return nil
}

// Render executes the named template with the given context map.
func (s *Store) Render(name string, ctx map[string]any) (string, error) {
	s.mu.RLock()
	tmpl, ok := s.templates[name]
	s.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("template %q not found (available: %v)", name, s.Names())
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}

	return buf.String(), nil
}

// Names returns a sorted list of loaded template names.
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.templates))
	for k := range s.templates {
		names = append(names, k)
	}
	return names
}
