package github

import (
	"encoding/base64"
	"testing"

	"github.com/majiayu000/auto-contributor/internal/config"
)

func TestDecodeBase64(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "valid base64",
			input:   base64.StdEncoding.EncodeToString([]byte("Hello, World!")),
			want:    "Hello, World!",
			wantErr: false,
		},
		{
			name:    "empty string",
			input:   "",
			want:    "",
			wantErr: false,
		},
		{
			name:    "base64 with content",
			input:   base64.StdEncoding.EncodeToString([]byte("# Contributing\n\nPlease follow...")),
			want:    "# Contributing\n\nPlease follow...",
			wantErr: false,
		},
		{
			name:    "invalid base64",
			input:   "not-valid-base64!!!",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := base64Decode(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("base64Decode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && string(got) != tt.want {
				t.Errorf("base64Decode() = %q, want %q", string(got), tt.want)
			}
		})
	}
}

func TestIsExcludedRepo(t *testing.T) {
	cfg := &config.Config{
		ExcludeRepos: []string{
			"owner/excluded-repo",
			"another/excluded",
		},
	}
	client := New(cfg)

	tests := []struct {
		repo string
		want bool
	}{
		{"owner/excluded-repo", true},
		{"another/excluded", true},
		{"owner/allowed-repo", false},
		{"different/repo", false},
	}

	for _, tt := range tests {
		t.Run(tt.repo, func(t *testing.T) {
			got := client.isExcludedRepo(tt.repo)
			if got != tt.want {
				t.Errorf("isExcludedRepo(%q) = %v, want %v", tt.repo, got, tt.want)
			}
		})
	}
}

func TestEstimateDifficulty(t *testing.T) {
	cfg := &config.Config{}
	client := New(cfg)

	tests := []struct {
		name   string
		labels []string
		repo   *RepoInfo
		minMax [2]float64 // min and max expected range
	}{
		{
			name:   "good first issue should be easier",
			labels: []string{"good first issue", "bug"},
			repo:   &RepoInfo{Stars: 1000},
			minMax: [2]float64{0.0, 0.4},
		},
		{
			name:   "beginner friendly should be easier",
			labels: []string{"beginner", "documentation"},
			repo:   &RepoInfo{Stars: 500},
			minMax: [2]float64{0.1, 0.5},
		},
		{
			name:   "complex issue should be harder",
			labels: []string{"complex", "enhancement"},
			repo:   &RepoInfo{Stars: 5000},
			minMax: [2]float64{0.6, 0.9},
		},
		{
			name:   "very popular repo should be slightly harder",
			labels: []string{"bug"},
			repo:   &RepoInfo{Stars: 50000},
			minMax: [2]float64{0.5, 0.7},
		},
		{
			name:   "repo with CLAUDE.md should be easier",
			labels: []string{"bug"},
			repo:   &RepoInfo{Stars: 1000, HasClaudeMD: true},
			minMax: [2]float64{0.2, 0.5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := client.estimateDifficulty(tt.labels, tt.repo)
			if got < tt.minMax[0] || got > tt.minMax[1] {
				t.Errorf("estimateDifficulty() = %v, want between %v and %v", got, tt.minMax[0], tt.minMax[1])
			}
		})
	}
}

func TestBuildSearchQuery(t *testing.T) {
	tests := []struct {
		name   string
		config *config.Config
		want   string
	}{
		{
			name: "with labels and language",
			config: &config.Config{
				IncludeLabels: []string{"good first issue"},
				Languages:     []string{"Go"},
			},
			want: "good first issue language:Go",
		},
		{
			name: "only labels",
			config: &config.Config{
				IncludeLabels: []string{"help wanted"},
			},
			want: "help wanted",
		},
		{
			name:   "empty config",
			config: &config.Config{},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := New(tt.config)
			got := client.buildSearchQuery()
			if got != tt.want {
				t.Errorf("buildSearchQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCIChecks(t *testing.T) {
	tests := []struct {
		name           string
		input          []byte
		wantStatus     string
		wantCodeFail   bool
	}{
		{
			name:       "invalid JSON returns error",
			input:      []byte(`not valid json`),
			wantStatus: "error",
		},
		{
			name:       "empty array returns success",
			input:      []byte(`[]`),
			wantStatus: "success",
		},
		{
			name:       "all passing checks returns success",
			input:      []byte(`[{"name":"build","state":"SUCCESS"}]`),
			wantStatus: "success",
		},
		{
			name:         "code check failure returns failure with CodeFailures true",
			input:        []byte(`[{"name":"build","state":"FAILURE"}]`),
			wantStatus:   "failure",
			wantCodeFail: true,
		},
		{
			name:       "pending code check returns pending",
			input:      []byte(`[{"name":"build","state":"PENDING"}]`),
			wantStatus: "pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCIChecks(tt.input)
			if got.Status != tt.wantStatus {
				t.Errorf("parseCIChecks() status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.CodeFailures != tt.wantCodeFail {
				t.Errorf("parseCIChecks() CodeFailures = %v, want %v", got.CodeFailures, tt.wantCodeFail)
			}
		})
	}
}

func TestRepoInfoStruct(t *testing.T) {
	info := &RepoInfo{
		Stars:           1234,
		Language:        "Go",
		HasContributing: true,
		HasClaudeMD:     false,
		TestFramework:   "go test",
	}

	if info.Stars != 1234 {
		t.Errorf("RepoInfo.Stars = %d, want 1234", info.Stars)
	}
	if info.Language != "Go" {
		t.Errorf("RepoInfo.Language = %q, want Go", info.Language)
	}
	if !info.HasContributing {
		t.Error("RepoInfo.HasContributing should be true")
	}
	if info.HasClaudeMD {
		t.Error("RepoInfo.HasClaudeMD should be false")
	}
}
