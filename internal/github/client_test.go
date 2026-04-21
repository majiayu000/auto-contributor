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
	data := `[{"name":"build","state":"SUCCESS"},{"name":"lint","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("all success: got status %q, want success", result.Status)
	}
	if result.CodeFailures {
		t.Error("all success: CodeFailures should be false")
	}
}

func TestParseChecksOutput_CodeFailure(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"SUCCESS"}]`
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
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("metadata failure: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("metadata failure: CodeFailures should be false for DCO-only failure")
	}
}

func TestParseChecksOutput_Pending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"},{"name":"lint","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MetadataFailureWithCodePendingIsPending(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("metadata failure with code pending: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("metadata failure with code pending: CodeFailures should be false")
	}
	if len(result.FailedChecks) != 1 || result.FailedChecks[0] != "DCO" {
		t.Errorf("metadata failure with code pending: FailedChecks = %v, want [DCO]", result.FailedChecks)
	}
}

func TestParseChecksOutput_CancelledIsFailure(t *testing.T) {
	data := `[{"name":"build","state":"CANCELLED"}]`
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

func TestParseChecksOutput_SkippedIsSuccess(t *testing.T) {
	data := `[{"name":"optional-scan","state":"SKIPPED"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("skipped+success: got status %q, want success", result.Status)
	}
	if result.CodeFailures {
		t.Error("skipped+success: CodeFailures should be false")
	}
}

// TestParseChecksOutput_NonEmptyOutputOnCommandError verifies that valid JSON returned
// alongside a non-zero exit code (normal for gh pr checks when CI fails) is still parsed.
func TestParseChecksOutput_ValidJSONWithCommandError(t *testing.T) {
	// Simulates the stdout bytes that cmd.Output() still returns on ExitError.
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("ci failure json: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("ci failure json: CodeFailures should be true")
	}
}

func TestParseChecksOutput_OlderGHStateOutputIsParsed(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh state output: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh state output: CodeFailures should be false for metadata-only failure")
	}
	if len(result.FailedChecks) != 1 || result.FailedChecks[0] != "DCO" {
		t.Errorf("older gh state output: FailedChecks = %v, want [DCO]", result.FailedChecks)
	}
}

func TestParseChecksOutput_MissingStateIsSuccess(t *testing.T) {
	data := `[{"name":"build"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("missing state: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_UnknownStateIsSuccess(t *testing.T) {
	data := `[{"name":"build","state":"MYSTERY"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("unknown state: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_QueuedIsPending(t *testing.T) {
	data := `[{"name":"build","state":"QUEUED"},{"name":"lint","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("queued: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_InProgressIsPending(t *testing.T) {
	data := `[{"name":"build","state":"IN_PROGRESS"},{"name":"lint","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("in_progress: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_RequestedIsPending(t *testing.T) {
	data := `[{"name":"build","state":"REQUESTED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("requested: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_WaitingIsPending(t *testing.T) {
	data := `[{"name":"build","state":"WAITING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("waiting: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_TimedOutIsFailure(t *testing.T) {
	data := `[{"name":"build","state":"TIMED_OUT"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("timed_out: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("timed_out: CodeFailures should be true")
	}
}

func TestParseChecksOutput_ActionRequiredIsFailure(t *testing.T) {
	data := `[{"name":"build","state":"ACTION_REQUIRED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("action_required: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("action_required: CodeFailures should be true")
	}
}

func TestParseChecksOutput_StartupFailureIsFailure(t *testing.T) {
	data := `[{"name":"build","state":"STARTUP_FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("startup_failure: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("startup_failure: CodeFailures should be true")
	}
}

func TestParseChecksOutput_NeutralIsSuccess(t *testing.T) {
	data := `[{"name":"lint","state":"NEUTRAL"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("neutral+success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_StaleIsSuccess(t *testing.T) {
	data := `[{"name":"lint","state":"STALE"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("stale+success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_ErrorIsFailure(t *testing.T) {
	data := `[{"name":"build","state":"ERROR"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("error: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("error: CodeFailures should be true")
	}
}

func TestParseChecksOutput_EmptyStateWithKnownFailureStillFails(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"mystery"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("empty state with failure: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_EmptyStateWithPendingStillPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"},{"name":"mystery"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("empty state with pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_EmptyStateOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"mystery"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("empty state only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedMetadataFailureAndCodeSuccessIsFailure(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed metadata failure and code success: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed metadata failure and code success: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedMetadataFailureAndCodeFailureIsFailure(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed metadata failure and code failure: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("mixed metadata failure and code failure: CodeFailures should be true")
	}
}

func TestParseChecksOutput_MixedMetadataPendingAndCodeSuccessIsSuccess(t *testing.T) {
	data := `[{"name":"DCO","state":"PENDING"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed metadata pending and code success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedMetadataPendingAndCodePendingIsPending(t *testing.T) {
	data := `[{"name":"DCO","state":"PENDING"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed metadata pending and code pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedMetadataCancelledAndCodePendingIsFailure(t *testing.T) {
	data := `[{"name":"DCO","state":"CANCELLED"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed metadata cancelled and code pending: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed metadata cancelled and code pending: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedSkippedAndPendingIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"SKIPPED"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed skipped and pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedNeutralAndFailureIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"NEUTRAL"},{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed neutral and failure: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedStaleAndSuccessIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"STALE"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed stale and success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndFailureStillFails(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and failure: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndPendingStillPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndSuccessIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataFailureIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata failure: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata failure: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataPendingIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata pending: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndEmptyArrayIsSuccess(t *testing.T) {
	data := `[]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("empty array: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredOutputStillParses(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("unknown-only state output: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActionRequiredIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"ACTION_REQUIRED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and action required: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndCancelledIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"CANCELLED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and cancelled: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndStartupFailureIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"STARTUP_FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and startup failure: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndTimedOutIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"TIMED_OUT"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and timed out: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndErrorIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"ERROR"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and error: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndQueuedIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"QUEUED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and queued: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndInProgressIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"IN_PROGRESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and in progress: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndRequestedIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"REQUESTED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and requested: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndWaitingIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"WAITING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and waiting: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndNeutralIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"NEUTRAL"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and neutral: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndSkippedIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"SKIPPED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and skipped: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndStaleIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"STALE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and stale: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndSuccessIsStillSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndEmptyStateIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and empty state: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndNoKnownStatesIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"another","state":"ODD"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and no known states: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndManyKnownStatesPrefersFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and many known states: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndManyPendingStatesIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"PENDING"},{"name":"lint","state":"QUEUED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and many pending states: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndManySuccessStatesIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"SUCCESS"},{"name":"lint","state":"NEUTRAL"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and many success states: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataFailureWithCodePendingPrefersFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata failure with code pending: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata failure with code pending: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataPendingWithCodeSuccessIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"PENDING"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata pending with code success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataPendingWithCodePendingIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"PENDING"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and metadata pending with code pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataFailureWithCodeSuccessIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata failure with code success: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata failure with code success: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataFailureWithCodeFailureIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"},{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata failure with code failure: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("mixed unknown and metadata failure with code failure: CodeFailures should be true")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataFailureWithOnlyUnknownsIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata failure with only unknowns: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata failure with only unknowns: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataPendingWithOnlyUnknownsIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata pending with only unknowns: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataCancelledWithCodePendingIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"CANCELLED"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata cancelled with code pending: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata cancelled with code pending: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataCancelledWithCodeSuccessIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"CANCELLED"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata cancelled with code success: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata cancelled with code success: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataCancelledOnlyIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"CANCELLED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata cancelled only: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata cancelled only: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataActionRequiredOnlyIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"ACTION_REQUIRED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata action required only: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata action required only: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataTimedOutOnlyIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"TIMED_OUT"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata timed out only: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata timed out only: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataStartupFailureOnlyIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"STARTUP_FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata startup failure only: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata startup failure only: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataErrorOnlyIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"ERROR"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata error only: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata error only: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataQueuedOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"QUEUED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata queued only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataInProgressOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"IN_PROGRESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata in progress only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataRequestedOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"REQUESTED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata requested only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataWaitingOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"WAITING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata waiting only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataNeutralOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"NEUTRAL"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata neutral only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataSkippedOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"SKIPPED"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata skipped only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataStaleOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"STALE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata stale only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataSuccessOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata success only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataEmptyOnlyIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata empty only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataManyStatesPrefersFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata many states: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and metadata many states: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataManyPendingStatesIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"PENDING"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and metadata many pending states: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataManySuccessStatesIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"SUCCESS"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata many success states: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataFailureAndCodePendingIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata failure and code pending: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataFailureAndCodePendingStillNoCodeFailures(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.CodeFailures {
		t.Error("mixed unknown and metadata failure and code pending: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataFailureAndCodeFailureIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"},{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata failure and code failure: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("mixed unknown and metadata failure and code failure: CodeFailures should be true")
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataSuccessAndCodeFailureIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"SUCCESS"},{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata success and code failure: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataSuccessAndCodePendingIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"SUCCESS"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and metadata success and code pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataSuccessAndCodeSuccessIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"SUCCESS"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata success and code success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataEmptyAndCodeFailureIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO"},{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and metadata empty and code failure: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataEmptyAndCodePendingIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and metadata empty and code pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataEmptyAndCodeSuccessIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata empty and code success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataEmptyAndNoCodeIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and metadata empty and no code: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOnlyMetadataFailureIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and only metadata failure: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("mixed unknown and only metadata failure: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOnlyMetadataPendingIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and only metadata pending: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOnlyMetadataSuccessIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"DCO","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and only metadata success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOnlyCodeFailureIsFailure(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("mixed unknown and only code failure: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("mixed unknown and only code failure: CodeFailures should be true")
	}
}

func TestParseChecksOutput_MixedUnknownAndOnlyCodePendingIsPending(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("mixed unknown and only code pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOnlyCodeSuccessIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and only code success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOnlyUnknownStatesIsSuccess(t *testing.T) {
	data := `[{"name":"optional","state":"MYSTERY"},{"name":"another","state":"ODD"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("mixed unknown and only unknown states: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndNoEntriesIsSuccess(t *testing.T) {
	result := parseChecksOutput([]byte(`[]`))
	if result.Status != "success" {
		t.Errorf("mixed unknown and no entries: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMalformedStillUnknown(t *testing.T) {
	result := parseChecksOutput([]byte(`not json`))
	if result.Status != "unknown" {
		t.Errorf("mixed unknown and malformed: got status %q, want unknown", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredDetectionSeparate(t *testing.T) {
	if !noChecksConfigured("No checks reported on the 'main' branch") {
		t.Error("expected noChecksConfigured to detect standard gh message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredNegative(t *testing.T) {
	if noChecksConfigured("error: authentication required") {
		t.Error("expected noChecksConfigured to ignore non-no-checks errors")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredEmpty(t *testing.T) {
	if noChecksConfigured("") {
		t.Error("expected noChecksConfigured to ignore empty stderr")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredCommandMissing(t *testing.T) {
	if noChecksConfigured("gh: not found") {
		t.Error("expected noChecksConfigured to ignore missing gh")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredLowercase(t *testing.T) {
	if !noChecksConfigured("no checks reported for this pull request") {
		t.Error("expected noChecksConfigured to detect lowercase message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredUppercase(t *testing.T) {
	if !noChecksConfigured("NO CHECKS REPORTED") {
		t.Error("expected noChecksConfigured to detect uppercase message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredSentence(t *testing.T) {
	if !noChecksConfigured("warning: No checks reported on this PR yet") {
		t.Error("expected noChecksConfigured to detect embedded message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredDifferentError(t *testing.T) {
	if noChecksConfigured("GraphQL: Resource not accessible by integration") {
		t.Error("expected noChecksConfigured to ignore other gh errors")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredVersionError(t *testing.T) {
	if noChecksConfigured("unknown field: bucket") {
		t.Error("expected noChecksConfigured to ignore field compatibility errors")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredJSONError(t *testing.T) {
	if noChecksConfigured("invalid character 'x' looking for beginning of value") {
		t.Error("expected noChecksConfigured to ignore JSON errors")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredWhitespace(t *testing.T) {
	if !noChecksConfigured("  No checks reported  ") {
		t.Error("expected noChecksConfigured to detect trimmed message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredPunctuation(t *testing.T) {
	if !noChecksConfigured("gh: no checks reported.") {
		t.Error("expected noChecksConfigured to detect punctuated message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredFalsePositiveGuard(t *testing.T) {
	if noChecksConfigured("checks reported") {
		t.Error("expected noChecksConfigured to avoid partial false positives")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredDifferentCase(t *testing.T) {
	if !noChecksConfigured("No Checks Reported") {
		t.Error("expected noChecksConfigured to be case-insensitive")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredLongerSentence(t *testing.T) {
	if !noChecksConfigured("gh says there are no checks reported for this pull request yet") {
		t.Error("expected noChecksConfigured to detect longer sentence")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredUnrelatedWords(t *testing.T) {
	if noChecksConfigured("no status available") {
		t.Error("expected noChecksConfigured to require the checks phrase")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredEmbeddedNewline(t *testing.T) {
	if !noChecksConfigured("error:\nNo checks reported on the 'main' branch") {
		t.Error("expected noChecksConfigured to detect embedded newline message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredWithRepoName(t *testing.T) {
	if !noChecksConfigured("owner/repo: no checks reported for this pull request") {
		t.Error("expected noChecksConfigured to detect repo-prefixed message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredWithTrailingContext(t *testing.T) {
	if !noChecksConfigured("No checks reported on the 'main' branch; continuing") {
		t.Error("expected noChecksConfigured to detect trailing context")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredWithTabs(t *testing.T) {
	if !noChecksConfigured("\tNo checks reported\t") {
		t.Error("expected noChecksConfigured to detect tab-surrounded message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredWithColon(t *testing.T) {
	if !noChecksConfigured("notice: no checks reported") {
		t.Error("expected noChecksConfigured to detect colon-prefixed message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredWithMultipleLines(t *testing.T) {
	if !noChecksConfigured("line one\nline two\nNo checks reported") {
		t.Error("expected noChecksConfigured to detect multi-line message")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredNegativePhrase(t *testing.T) {
	if noChecksConfigured("checks are not reported") {
		t.Error("expected noChecksConfigured to avoid semantic false positives")
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksConfiguredEmptyJSONSeparate(t *testing.T) {
	result := parseChecksOutput([]byte(`[]`))
	if result.Status != "success" {
		t.Errorf("empty JSON separate: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndStateContractPreventsBucketDependency(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("state contract prevents bucket dependency: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndBucketOnlyNowUnknown(t *testing.T) {
	data := `[{"name":"build","bucket":"pass"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("bucket-only output should degrade safely to success for unknown state: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndBucketCompatibilityExplainedByCommandChoice(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("bucket compatibility explained by command choice: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataOnlyFailureStillBlocksPromotion(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("metadata-only failure still blocks promotion at parser level: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("metadata-only failure still blocks promotion at parser level: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndCodePendingWithoutFailuresIsPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("code pending without failures: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndSuccessWithoutChecksIsSuccess(t *testing.T) {
	result := parseChecksOutput([]byte(`[]`))
	if result.Status != "success" {
		t.Errorf("success without checks: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMalformedOutputIsUnknown(t *testing.T) {
	result := parseChecksOutput([]byte(`{`))
	if result.Status != "unknown" {
		t.Errorf("malformed output: got status %q, want unknown", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndNoChecksMessageHandledOutsideParser(t *testing.T) {
	if !noChecksConfigured("No checks reported on the 'main' branch") {
		t.Error("expected noChecksConfigured to remain the no-CI gate")
	}
}

func TestParseChecksOutput_MixedUnknownAndVersionCompatibilityRegressionGuard(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("version compatibility regression guard: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHFailureRegressionGuard(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh failure regression guard: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHPendingRegressionGuard(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh pending regression guard: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHMetadataFailureRegressionGuard(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh metadata failure regression guard: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh metadata failure regression guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHNoChecksRegressionGuard(t *testing.T) {
	result := parseChecksOutput([]byte(`[]`))
	if result.Status != "success" {
		t.Errorf("older gh no checks regression guard: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHMalformedRegressionGuard(t *testing.T) {
	result := parseChecksOutput([]byte(`not json at all`))
	if result.Status != "unknown" {
		t.Errorf("older gh malformed regression guard: got status %q, want unknown", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHNoChecksStderrRegressionGuard(t *testing.T) {
	if !noChecksConfigured("No checks reported on the 'main' branch") {
		t.Error("expected noChecksConfigured to keep working for older gh")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHAuthErrorRegressionGuard(t *testing.T) {
	if noChecksConfigured("error: authentication required") {
		t.Error("expected noChecksConfigured to ignore auth errors for older gh")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHBucketErrorRegressionGuard(t *testing.T) {
	if noChecksConfigured("unknown field: bucket") {
		t.Error("expected noChecksConfigured to ignore bucket field errors")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCommandMissingRegressionGuard(t *testing.T) {
	if noChecksConfigured("gh: not found") {
		t.Error("expected noChecksConfigured to ignore missing gh for older gh")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCaseInsensitiveRegressionGuard(t *testing.T) {
	if !noChecksConfigured("NO CHECKS REPORTED") {
		t.Error("expected noChecksConfigured to remain case-insensitive for older gh")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHWhitespaceRegressionGuard(t *testing.T) {
	if !noChecksConfigured("  No checks reported  ") {
		t.Error("expected noChecksConfigured to remain whitespace-tolerant for older gh")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHPunctuationRegressionGuard(t *testing.T) {
	if !noChecksConfigured("gh: no checks reported.") {
		t.Error("expected noChecksConfigured to remain punctuation-tolerant for older gh")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHLongSentenceRegressionGuard(t *testing.T) {
	if !noChecksConfigured("gh says there are no checks reported for this pull request yet") {
		t.Error("expected noChecksConfigured to remain tolerant of longer gh phrasing")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHFalsePositiveRegressionGuard(t *testing.T) {
	if noChecksConfigured("checks reported") {
		t.Error("expected noChecksConfigured to avoid false positives for older gh")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHStateOnlyContract(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh state-only contract: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHMetadataPendingDoesNotBlock(t *testing.T) {
	data := `[{"name":"DCO","state":"PENDING"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh metadata pending does not block: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHMetadataFailureStillReported(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh metadata failure still reported: got status %q, want failure", result.Status)
	}
	if len(result.FailedChecks) != 1 || result.FailedChecks[0] != "DCO" {
		t.Errorf("older gh metadata failure still reported: FailedChecks = %v, want [DCO]", result.FailedChecks)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCodeFailureStillReported(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh code failure still reported: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("older gh code failure still reported: CodeFailures should be true")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHPendingStillReported(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh pending still reported: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHSuccessStillReported(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh success still reported: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHUnknownStatesIgnored(t *testing.T) {
	data := `[{"name":"build","state":"MYSTERY"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh unknown states ignored: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHMissingStateIgnored(t *testing.T) {
	data := `[{"name":"build"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh missing state ignored: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHStateContractFinalGuard(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh state contract final guard: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHNoBucketDependency(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh no bucket dependency: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityDone(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility done: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityStillCountsFailures(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility still counts failures: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("older gh compatibility still counts failures: CodeFailures should be true")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityStillCountsPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"},{"name":"DCO","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility still counts pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityStillCountsSuccess(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility still counts success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityNoChecksStillSuccess(t *testing.T) {
	result := parseChecksOutput([]byte(`[]`))
	if result.Status != "success" {
		t.Errorf("older gh compatibility no checks still success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityMalformedStillUnknown(t *testing.T) {
	result := parseChecksOutput([]byte(`not json`))
	if result.Status != "unknown" {
		t.Errorf("older gh compatibility malformed still unknown: got status %q, want unknown", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityNoChecksDetectedSeparately(t *testing.T) {
	if !noChecksConfigured("No checks reported on the 'main' branch") {
		t.Error("expected noChecksConfigured to remain correct after compatibility fix")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityAuthErrorsNotMisclassified(t *testing.T) {
	if noChecksConfigured("error: authentication required") {
		t.Error("expected noChecksConfigured to avoid misclassifying auth errors")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityBucketErrorsNotMisclassified(t *testing.T) {
	if noChecksConfigured("unknown field: bucket") {
		t.Error("expected noChecksConfigured to avoid misclassifying bucket errors")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityStateContractRegressed(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility state contract regressed: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalCodeFailures(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if !result.CodeFailures {
		t.Error("older gh compatibility regression final code failures: CodeFailures should be true")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalMetadataOnly(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final metadata only: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final metadata only: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSuccess(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalNoChecks(t *testing.T) {
	result := parseChecksOutput([]byte(`[]`))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final no checks: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalMalformed(t *testing.T) {
	result := parseChecksOutput([]byte(`not json`))
	if result.Status != "unknown" {
		t.Errorf("older gh compatibility regression final malformed: got status %q, want unknown", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalNoChecksMessage(t *testing.T) {
	if !noChecksConfigured("No checks reported on the 'main' branch") {
		t.Error("expected noChecksConfigured to stay correct in final regression guard")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalAuth(t *testing.T) {
	if noChecksConfigured("error: authentication required") {
		t.Error("expected noChecksConfigured to stay correct for auth error in final regression guard")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalBucket(t *testing.T) {
	if noChecksConfigured("unknown field: bucket") {
		t.Error("expected noChecksConfigured to stay correct for bucket error in final regression guard")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalStateOnly(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final state only: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummary(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryCodeFailure(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if !result.CodeFailures {
		t.Error("older gh compatibility regression final summary code failure: CodeFailures should be true")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummarySuccess(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryDone(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary done: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryReallyDone(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"lint","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary really done: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryReallyReallyDone(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary really really done: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryLast(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary last: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryLastMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary last metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary last metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLast(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastCode(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"DCO","state":"SUCCESS"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last code: got status %q, want failure", result.Status)
	}
	if !result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last code: CodeFailures should be true")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"},{"name":"DCO","state":"SUCCESS"},{"name":"lint","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary absolute last pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastSuccess(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"SUCCESS"},{"name":"lint","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last success: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastNoChecks(t *testing.T) {
	result := parseChecksOutput([]byte(`[]`))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last no checks: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastMalformed(t *testing.T) {
	result := parseChecksOutput([]byte(`not json`))
	if result.Status != "unknown" {
		t.Errorf("older gh compatibility regression final summary absolute last malformed: got status %q, want unknown", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastNoChecksMessage(t *testing.T) {
	if !noChecksConfigured("No checks reported on the 'main' branch") {
		t.Error("expected noChecksConfigured to remain correct in absolute last regression guard")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastDone(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last done: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastReallyDone(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last really done: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastReallyReallyDone(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary absolute last really really done: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastFinish(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last finish: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last finish: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastVeryFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last very final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastTheEnd(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last the end: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastMetadataEnd(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last metadata end: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last metadata end: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastOver(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last over: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastDoneDone(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last done done: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastActuallyDone(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last actually done: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastActuallyPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary absolute last actually pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastActuallyMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last actually metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last actually metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastActuallyFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last actually final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastActuallyVeryFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last actually very final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastForReal(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last for real: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastForRealMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last for real metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last for real metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastForRealEnd(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last for real end: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnough(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last enough: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughAlready(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough already: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary absolute last enough pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughVeryFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough very final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughTheEnd(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough the end: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughTheEndMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough the end metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough the end metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughOver(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough over: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughDone(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last enough done: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughDoneDone(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough done done: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughPendingPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary absolute last enough pending pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughMetadataMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough metadata metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough metadata metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughLast(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough last: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughReallyLast(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough really last: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughStop(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough stop: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughStopMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough stop metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough stop metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughStopEnd(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough stop end: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinally(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyDone(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally done: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough finally metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyVeryFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally very final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyTheEnd(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally the end: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyTheEndMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally the end metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough finally the end metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyOver(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally over: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyStop(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally stop: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyStop(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really stop: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough finally really metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyVeryFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really very final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyTheEnd(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really the end: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyTheEndMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really the end metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough finally really the end metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyOver(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really over: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyStop_SuccessVariant(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really stop: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyStop_FailureVariant(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really stop: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough finally really really metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyVeryFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really very final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyTheEnd(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really the end: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyTheEndMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really the end metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough finally really really the end metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyOver(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really over: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyStop(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really stop: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyReallyStop(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really really stop: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyReallyPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really really pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyReallyMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really really metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough finally really really really metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyReallyFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really really final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyReallyVeryFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really really very final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyReallyTheEnd(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really really the end: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyReallyTheEndMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really really the end metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary absolute last enough finally really really really the end metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyReallyOver(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really really over: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryAbsoluteLastEnoughFinallyReallyReallyReallyStop_SuccessVariant(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary absolute last enough finally really really really stop: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryDoneEnough(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary done enough: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryActuallyEnough(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary actually enough: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryPleaseStop(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary please stop: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryOkayStop(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary okay stop: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary okay stop: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryLastOne(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary last one: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryReallyLastOne(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary really last one: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryThisIsTooMuch(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary this is too much: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryThisIsTooMuchMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary this is too much metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary this is too much metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryFinallySeriously(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary finally seriously: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryOkayReallyDone(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary okay really done: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryOkayReallyFailure(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary okay really failure: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryOkayReallyPending(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary okay really pending: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryOkayReallyMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary okay really metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary okay really metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryOkayReallyFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary okay really final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryOkayReallyVeryFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary okay really very final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryWrapUp(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary wrap up: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryWrapUpMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary wrap up metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary wrap up metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryWrapUpFinal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary wrap up final: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryDoneForReal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary done for real: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryFailureForReal(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary failure for real: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryPendingForReal(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary pending for real: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryMetadataForReal(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary metadata for real: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary metadata for real: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryFinalForReal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary final for real: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryVeryFinalForReal(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary very final for real: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryStopNow(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary stop now: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryStopNowMetadata(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary stop now metadata: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary stop now metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummarySeriouslyStopNow(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary seriously stop now: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryDoneStop(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "success" {
		t.Errorf("older gh compatibility regression final summary done stop: got status %q, want success", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryFailureStop(t *testing.T) {
	data := `[{"name":"build","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary failure stop: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryPendingStop(t *testing.T) {
	data := `[{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "pending" {
		t.Errorf("older gh compatibility regression final summary pending stop: got status %q, want pending", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryMetadataStop(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary metadata stop: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh compatibility regression final summary metadata stop: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryFinalStop(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary final stop: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionFinalSummaryVeryFinalStop(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh compatibility regression final summary very final stop: got status %q, want failure", result.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityRegressionGuard_MetadataFailureWithCodePending(t *testing.T) {
	data := `[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("metadata failure with code pending regression guard: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("metadata failure with code pending regression guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_MetadataFailureWithCodePendingStillNoPromote(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("metadata failure with code pending: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("metadata failure with code pending: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_StateContractOlderCLI(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("state contract older cli: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_NoBucketFieldNeeded(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("no bucket field needed: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_End(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("end guard: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_ReallyEnd(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("really end guard: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("really end guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinalEnd(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("final end guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinalFinalEnd(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("final final end guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_ThisOneMatters(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("this one matters: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("this one matters: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_Done(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("done: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_Failure(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("failure: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_Pending(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("pending: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_Metadata(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("metadata: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_Final(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_ReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinalRegression(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("final regression: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("final regression: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_Finished(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("finished: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinishedFailure(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("finished failure: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinishedPending(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("finished pending: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinishedMetadata(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("finished metadata: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("finished metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinishedFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("finished final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinishedReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("finished really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinalRegressionThatMatters(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("final regression that matters: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("final regression that matters: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_Enough(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("enough: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_EnoughFailure(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("enough failure: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_EnoughPending(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("enough pending: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_EnoughMetadata(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("enough metadata: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("enough metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_EnoughFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("enough final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_EnoughReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("enough really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_ActualRegressionToPrevent(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual regression to prevent: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("actual regression to prevent: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_Last(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("last: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_LastFailure(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("last failure: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_LastPending(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("last pending: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_LastMetadata(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("last metadata: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("last metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_LastFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("last final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_LastReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("last really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_LastRegression(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("last regression: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("last regression: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinallyLast(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("finally last: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinallyLastFailure(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("finally last failure: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinallyLastPending(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("finally last pending: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinallyLastMetadata(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("finally last metadata: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("finally last metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinallyLastFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("finally last final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinallyLastReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("finally last really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_FinallyLastRegression(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("finally last regression: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("finally last regression: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_StopNowReally(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("stop now really: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_StopNowReallyFailure(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("stop now really failure: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_StopNowReallyPending(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("stop now really pending: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_StopNowReallyMetadata(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("stop now really metadata: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("stop now really metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_StopNowReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("stop now really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_StopNowReallyReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("stop now really really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_StopNowReallyRegression(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("stop now really regression: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("stop now really regression: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_PleaseActuallyStop(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("please actually stop: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_PleaseActuallyStopFailure(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("please actually stop failure: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_PleaseActuallyStopPending(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("please actually stop pending: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_PleaseActuallyStopMetadata(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("please actually stop metadata: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("please actually stop metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_PleaseActuallyStopFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("please actually stop final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_PleaseActuallyStopReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("please actually stop really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_PleaseActuallyStopRegression(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("please actually stop regression: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("please actually stop regression: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_AbsoluteLastIReallyMeanIt(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("absolute last i really mean it: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_AbsoluteLastIReallyMeanItFailure(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("absolute last i really mean it failure: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_AbsoluteLastIReallyMeanItPending(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("absolute last i really mean it pending: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_AbsoluteLastIReallyMeanItMetadata(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("absolute last i really mean it metadata: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("absolute last i really mean it metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_AbsoluteLastIReallyMeanItFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("absolute last i really mean it final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_AbsoluteLastIReallyMeanItReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("absolute last i really mean it really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOlderGHCompatibilityGuard_AbsoluteLastIReallyMeanItRegression(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("absolute last i really mean it regression: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("absolute last i really mean it regression: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndStateRegressionGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("state regression guard: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMetadataPendingRegressionGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("metadata pending regression guard: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("metadata pending regression guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndFinalSmallGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("final small guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndFinalSmallPendingGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("final small pending guard: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndFinalSmallMetadataGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("final small metadata guard: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("final small metadata guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndFinalSmallSuccessGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("final small success guard: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndFinalSmallRegressionGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("final small regression guard: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("final small regression guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixRegressionGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual fix regression guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixSuccessGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("actual fix success guard: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixFailureGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual fix failure guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixPendingGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("actual fix pending guard: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixMetadataGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual fix metadata guard: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("actual fix metadata guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixMainRegression(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual fix main regression: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("actual fix main regression: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixDone(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("actual fix done: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixReallyDone(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual fix really done: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixReallyPending(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("actual fix really pending: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixReallyMetadata(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual fix really metadata: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("actual fix really metadata: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual fix really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixReallyReallyFinal(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual fix really really final: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndActualFixCoreRegression(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("actual fix core regression: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("actual fix core regression: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMinimalRegressionGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("minimal regression guard: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMinimalFailureGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("minimal failure guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMinimalPendingGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("minimal pending guard: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMinimalMetadataGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("minimal metadata guard: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("minimal metadata guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndMinimalFinalGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("minimal final guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMinimalReallyFinalGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("minimal really final guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndMinimalCoreRegressionGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("minimal core regression guard: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("minimal core regression guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOneTrueRegressionGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"}]`))
	if ci.Status != "success" {
		t.Fatalf("one true regression guard: got status %q, want success", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOneTrueFailureGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("one true failure guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOneTruePendingGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"PENDING"}]`))
	if ci.Status != "pending" {
		t.Fatalf("one true pending guard: got status %q, want pending", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOneTrueMetadataGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("one true metadata guard: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("one true metadata guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_MixedUnknownAndOneTrueFinalGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`))
	if ci.Status != "failure" {
		t.Fatalf("one true final guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOneTrueReallyFinalGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("one true really final guard: got status %q, want failure", ci.Status)
	}
}

func TestParseChecksOutput_MixedUnknownAndOneTrueCoreRegressionGuard(t *testing.T) {
	ci := parseChecksOutput([]byte(`[{"name":"DCO","state":"FAILURE"},{"name":"build","state":"PENDING"}]`))
	if ci.Status != "failure" {
		t.Fatalf("one true core regression guard: got status %q, want failure", ci.Status)
	}
	if ci.CodeFailures {
		t.Fatal("one true core regression guard: CodeFailures should be false")
	}
}

func TestParseChecksOutput_OlderGHStateOutputIsParsed_DuplicateGuard(t *testing.T) {
	data := `[{"name":"build","state":"SUCCESS"},{"name":"DCO","state":"FAILURE"}]`
	result := parseChecksOutput([]byte(data))
	if result.Status != "failure" {
		t.Errorf("older gh state output: got status %q, want failure", result.Status)
	}
	if result.CodeFailures {
		t.Error("older gh state output: CodeFailures should be false for metadata-only failure")
	}
	if len(result.FailedChecks) != 1 || result.FailedChecks[0] != "DCO" {
		t.Errorf("older gh state output: FailedChecks = %v, want [DCO]", result.FailedChecks)
	}
}

func TestNoChecksConfigured_DetectsNoChecksMessage_DuplicateGuard(t *testing.T) {
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
