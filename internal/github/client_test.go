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

func TestParseChecksOutput_MalformedJSON(t *testing.T) {
	result := parseChecksOutput([]byte("not json at all"))
	if result.Status != "unknown" {
		t.Errorf("malformed JSON: got status %q, want unknown", result.Status)
	}
}

func TestParseChecksOutput_EmptyArray(t *testing.T) {
	// No checks configured — should report success, not unknown.
	result := parseChecksOutput([]byte(`[]`))
	if result.Status != "success" {
		t.Errorf("empty checks: got status %q, want success", result.Status)
	}
	if len(result.FailedChecks) != 0 {
		t.Errorf("empty checks: expected no failed checks, got %v", result.FailedChecks)
	}
}

func TestParseChecksOutput_AllSuccess(t *testing.T) {
	data := `[{"name":"build","bucket":"pass"},{"name":"lint","bucket":"pass"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("all success: got status %q, want success", result.Status)
	}
	if result.CodeFailures {
		t.Error("all success: CodeFailures should be false")
	}
}

func TestParseChecksOutput_CodeFailure(t *testing.T) {
	data := `[{"name":"build","bucket":"fail"},{"name":"lint","bucket":"pass"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("code failure: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("code failure: CodeFailures should be true")
	}
	if len(result.FailedChecks) != 1 || result.FailedChecks[0] != "build" {
		t.Errorf("code failure: FailedChecks = %v, want [build]", result.FailedChecks)
	}
}

func TestParseChecksOutput_MetadataFailureOnly(t *testing.T) {
	// DCO and similar metadata checks should not set CodeFailures.
	data := `[{"name":"DCO","bucket":"fail"},{"name":"build","bucket":"pass"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("metadata failure: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("metadata failure: CodeFailures should be false for DCO-only failure")
	}
}

func TestParseChecksOutput_Pending(t *testing.T) {
	data := `[{"name":"build","bucket":"pending"},{"name":"lint","bucket":"pass"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MetadataFailureWithCodePendingIsPending(t *testing.T) {
	data := `[{"name":"DCO","bucket":"fail"},{"name":"build","bucket":"pending"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("metadata failure with code pending: got status %q, want pending", result.Status)
	}
	if result.CodeFailures {
		t.Error("metadata failure with code pending: CodeFailures should be false")
	}
	if len(result.FailedChecks) != 1 || result.FailedChecks[0] != "DCO" {
		t.Errorf("metadata failure with code pending: FailedChecks = %v, want [DCO]", result.FailedChecks)
	}
}

func TestParseChecksOutput_CancelledIsFailure(t *testing.T) {
	data := `[{"name":"build","bucket":"cancel"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("cancelled: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("cancelled: CodeFailures should be true")
	}
	if len(result.FailedChecks) != 1 || result.FailedChecks[0] != "build" {
		t.Errorf("cancelled: FailedChecks = %v, want [build]", result.FailedChecks)
	}
}

func TestParseChecksOutput_SkippingIsSuccess(t *testing.T) {
	data := `[{"name":"optional-scan","bucket":"skipping"},{"name":"build","bucket":"pass"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("skipping+pass: got status %q, want success", result.Status)
	}
	if result.CodeFailures {
		t.Error("skipping+pass: CodeFailures should be false")
	}
}

// TestParseChecksOutput_NonEmptyOutputOnCommandError verifies that valid JSON returned
// alongside a non-zero exit code (normal for gh pr checks when CI fails) is still parsed.
func TestParseChecksOutput_ValidJSONWithCommandError(t *testing.T) {
	// Simulates the stdout bytes that cmd.Output() still returns on ExitError.
	data := `[{"name":"build","bucket":"fail"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("ci failure json: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("ci failure json: CodeFailures should be true")
	}
}

func TestParseChecksOutput_UnknownBucketIsUnknown(t *testing.T) {
	data := `[{"name":"build","bucket":"mystery"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "unknown" {
		t.Errorf("unknown bucket: got status %q, want unknown", result.Status)
	}
}

func TestParseChecksOutput_MissingBucketIsUnknown(t *testing.T) {
	data := `[{"name":"build"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "unknown" {
		t.Errorf("missing bucket: got status %q, want unknown", result.Status)
	}
}

func TestNoChecksConfigured_DetectsNoChecksMessage(t *testing.T) {
	cases := []struct {
		stderr string
		want   bool
	}{
		{"no checks reported for this pull request", true},
		{"No checks reported on the 'main' branch", true},
		{"error: authentication required", false},
		{"", false},
		{"gh: not found", false},
	}
	for _, tc := range cases {
		if got := noChecksConfigured(tc.stderr); got != tc.want {
			t.Errorf("noChecksConfigured(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}
