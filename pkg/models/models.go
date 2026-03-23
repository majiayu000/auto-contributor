package models

import (
	"time"
)

// IssueStatus represents the status of an issue in the pipeline
type IssueStatus string

const (
	IssueStatusDiscovered IssueStatus = "discovered" // Newly found, ready for pipeline
	IssueStatusCompleted  IssueStatus = "completed"  // Successfully created PR
	IssueStatusPRCreated  IssueStatus = "pr_created" // Legacy: same as completed
	IssueStatusFailed     IssueStatus = "failed"     // Failed, won't retry
	IssueStatusMerged     IssueStatus = "merged"     // PR was merged
	IssueStatusAbandoned  IssueStatus = "abandoned"  // Manually abandoned

	// V2 pipeline statuses
	IssueStatusAnalyzing   IssueStatus = "analyzing"   // Analyst agent running
	IssueStatusEngineering IssueStatus = "engineering"  // Engineer agent running
	IssueStatusReviewing   IssueStatus = "reviewing"    // Reviewer agent running
	IssueStatusRework      IssueStatus = "rework"       // Engineer reworking after review
	IssueStatusSubmitting  IssueStatus = "submitting"   // Submitter agent running
)

// PRStatus represents the status of a pull request
type PRStatus string

const (
	PRStatusDraft      PRStatus = "draft" // Draft PR, waiting for CI
	PRStatusOpen       PRStatus = "open"  // Ready for review, checking feedback
	PRStatusMerged     PRStatus = "merged"
	PRStatusClosed     PRStatus = "closed"
	PRStatusResponding     PRStatus = "responding"
	PRStatusNeedsAttention PRStatus = "needs_attention" // Requires manual action (e.g. CLA signing)
)

// FailureReason categorizes why an attempt failed
type FailureReason string

const (
	FailureReasonTimeout          FailureReason = "timeout"
	FailureReasonNoChanges        FailureReason = "no_changes"
	FailureReasonTestsFailed      FailureReason = "tests_failed"
	FailureReasonCIFailed         FailureReason = "ci_failed"
	FailureReasonCloneFailed      FailureReason = "clone_failed"
	FailureReasonPRFailed         FailureReason = "pr_failed"
	FailureReasonComplexityHigh   FailureReason = "complexity_too_high"
	FailureReasonAlreadyHasPR     FailureReason = "already_has_pr"
	FailureReasonAlreadyFixed     FailureReason = "already_fixed"
	FailureReasonUnknown          FailureReason = "unknown"
)

// Issue represents a discovered GitHub issue
type Issue struct {
	ID              int64       `db:"id" json:"id"`
	Repo            string      `db:"repo" json:"repo"`
	IssueNumber     int         `db:"issue_number" json:"issue_number"`
	Title           string      `db:"title" json:"title"`
	Body            string      `db:"body" json:"body,omitempty"`
	Labels          string      `db:"labels" json:"labels,omitempty"` // JSON array
	Language        string      `db:"language" json:"language"`
	DifficultyScore float64     `db:"difficulty_score" json:"difficulty_score"`
	Status          IssueStatus `db:"status" json:"status"`
	ErrorMessage    string      `db:"error_message" json:"error_message,omitempty"`
	RetryCount      int         `db:"retry_count" json:"retry_count"`
	DiscoveredAt    time.Time   `db:"discovered_at" json:"discovered_at"`
	UpdatedAt       time.Time   `db:"updated_at" json:"updated_at"`
}

// MaxRetries is the maximum number of retry attempts for retryable failures
const MaxRetries = 2

// IsRetryable returns true if this failure reason can be retried
func (r FailureReason) IsRetryable() bool {
	switch r {
	case FailureReasonTestsFailed, FailureReasonTimeout, FailureReasonCloneFailed, FailureReasonNoChanges:
		return true
	case FailureReasonAlreadyHasPR, FailureReasonComplexityHigh, FailureReasonAlreadyFixed:
		return false
	default:
		return false
	}
}

// PullRequest represents a created pull request
type PullRequest struct {
	ID                 int64      `db:"id" json:"id"`
	IssueID            int64      `db:"issue_id" json:"issue_id"`
	PRURL              string     `db:"pr_url" json:"pr_url"`
	PRNumber           int        `db:"pr_number" json:"pr_number"`
	BranchName         string     `db:"branch_name" json:"branch_name"`
	Status             PRStatus   `db:"status" json:"status"`
	CIStatus           string     `db:"ci_status" json:"ci_status"`
	RetryCount         int        `db:"retry_count" json:"retry_count"`
	MergedAt           *time.Time `db:"merged_at" json:"merged_at,omitempty"`
	ClosedAt           *time.Time `db:"closed_at" json:"closed_at,omitempty"`
	ReviewCommentCount int        `db:"review_comment_count" json:"review_comment_count"`
	FirstReviewAt      *time.Time `db:"first_review_at" json:"first_review_at,omitempty"`
	CreatedAt            time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at" json:"updated_at"`
	LastFeedbackCheckAt  *time.Time `db:"last_feedback_check_at" json:"last_feedback_check_at,omitempty"`
	FeedbackRound        int        `db:"feedback_round" json:"feedback_round"`
}

// TimeToMerge returns the duration from PR creation to merge
func (pr *PullRequest) TimeToMerge() *time.Duration {
	if pr.MergedAt == nil {
		return nil
	}
	d := pr.MergedAt.Sub(pr.CreatedAt)
	return &d
}

// TimeToFirstReview returns the duration from PR creation to first review
func (pr *PullRequest) TimeToFirstReview() *time.Duration {
	if pr.FirstReviewAt == nil {
		return nil
	}
	d := pr.FirstReviewAt.Sub(pr.CreatedAt)
	return &d
}

// SolveAttempt records each attempt to solve an issue
type SolveAttempt struct {
	ID                  int64         `db:"id" json:"id"`
	IssueID             int64         `db:"issue_id" json:"issue_id"`
	AttemptNumber       int           `db:"attempt_number" json:"attempt_number"`
	StartedAt           time.Time     `db:"started_at" json:"started_at"`
	CompletedAt         *time.Time    `db:"completed_at" json:"completed_at,omitempty"`
	DurationSeconds     float64       `db:"duration_seconds" json:"duration_seconds"`
	PromptVersion       string        `db:"prompt_version" json:"prompt_version"`
	ModelUsed           string        `db:"model_used" json:"model_used"`
	FilesChanged        string        `db:"files_changed" json:"files_changed,omitempty"`
	ClaudeOutputPreview string        `db:"claude_output_preview" json:"claude_output_preview,omitempty"`
	FixCompleteMarker   bool          `db:"fix_complete_marker" json:"fix_complete_marker"`
	ClaudeTestsPassed   *bool         `db:"claude_tests_passed" json:"claude_tests_passed,omitempty"`
	IsComplex           *bool         `db:"is_complex" json:"is_complex,omitempty"`
	CanTestLocally      *bool         `db:"can_test_locally" json:"can_test_locally,omitempty"`
	ComplexityReasons   string        `db:"complexity_reasons" json:"complexity_reasons,omitempty"`
	ExternalTestPassed  *bool         `db:"external_test_passed" json:"external_test_passed,omitempty"`
	TestFramework       string        `db:"test_framework" json:"test_framework,omitempty"`
	TestDurationSeconds float64       `db:"test_duration_seconds" json:"test_duration_seconds"`
	TestOutputPreview   string        `db:"test_output_preview" json:"test_output_preview,omitempty"`
	Success             bool          `db:"success" json:"success"`
	FailureReason       FailureReason `db:"failure_reason" json:"failure_reason,omitempty"`
	ErrorDetails        string        `db:"error_details" json:"error_details,omitempty"`
	// Token usage metrics
	PromptTokens     int `db:"prompt_tokens" json:"prompt_tokens"`
	CompletionTokens int `db:"completion_tokens" json:"completion_tokens"`
	TotalTokens      int `db:"total_tokens" json:"total_tokens"`
	// Code change metrics
	LinesAdded   int `db:"lines_added" json:"lines_added"`
	LinesDeleted int `db:"lines_deleted" json:"lines_deleted"`
}

// IssueMetrics aggregates metrics for an issue
type IssueMetrics struct {
	ID                    int64     `db:"id"`
	IssueID               int64     `db:"issue_id"`
	EstimatedDifficulty   float64   `db:"estimated_difficulty"`
	ActualDifficulty      *float64  `db:"actual_difficulty"`
	RepoStars             int       `db:"repo_stars"`
	RepoLanguage          string    `db:"repo_language"`
	RepoHasContributing   bool      `db:"repo_has_contributing"`
	RepoHasClaudeMD       bool      `db:"repo_has_claude_md"`
	RepoTestFramework     string    `db:"repo_test_framework"`
	IssueBodyLength       int       `db:"issue_body_length"`
	IssueHasCodeBlocks    bool      `db:"issue_has_code_blocks"`
	IssueHasStackTrace    bool      `db:"issue_has_stack_trace"`
	IssueLabelsCount      int       `db:"issue_labels_count"`
	TotalAttempts         int       `db:"total_attempts"`
	SuccessfulAttempts    int       `db:"successful_attempts"`
	TotalTimeSpentSeconds float64   `db:"total_time_spent_seconds"`
	FirstAttemptSuccess   *bool     `db:"first_attempt_success"`
	CreatedAt             time.Time `db:"created_at"`
	UpdatedAt             time.Time `db:"updated_at"`
}

// DailyStats holds daily aggregated statistics
type DailyStats struct {
	ID                     int64     `db:"id"`
	Date                   string    `db:"date"` // YYYY-MM-DD
	IssuesDiscovered       int       `db:"issues_discovered"`
	IssuesAttempted        int       `db:"issues_attempted"`
	IssuesSolved           int       `db:"issues_solved"`
	PRsCreated             int       `db:"prs_created"`
	PRsMerged              int       `db:"prs_merged"`
	PRsClosed              int       `db:"prs_closed"`
	AvgSolveTimeSeconds    *float64  `db:"avg_solve_time_seconds"`
	AvgAttemptsPerIssue    *float64  `db:"avg_attempts_per_issue"`
	FirstAttemptSuccessRate *float64 `db:"first_attempt_success_rate"`
	OverallSuccessRate     *float64  `db:"overall_success_rate"`
	StatsByLanguage        string    `db:"stats_by_language"`     // JSON
	StatsByRepo            string    `db:"stats_by_repo"`         // JSON
	FailureReasonsCount    string    `db:"failure_reasons_count"` // JSON
	CreatedAt              time.Time `db:"created_at"`
}

// RepoMetrics holds aggregated metrics per repository
type RepoMetrics struct {
	ID                 int64     `db:"id" json:"id"`
	Repo               string    `db:"repo" json:"repo"`
	Language           string    `db:"language" json:"language"`
	Stars              int       `db:"stars" json:"stars"`
	SuccessCount       int       `db:"success_count" json:"success_count"`
	AttemptCount       int       `db:"attempt_count" json:"attempt_count"`
	PRsCreated         int       `db:"prs_created" json:"prs_created"`
	PRsMerged          int       `db:"prs_merged" json:"prs_merged"`
	PRsClosed          int       `db:"prs_closed" json:"prs_closed"`
	AvgDurationSeconds float64   `db:"avg_duration_seconds" json:"avg_duration_seconds"`
	AvgTimeToMerge     float64   `db:"avg_time_to_merge" json:"avg_time_to_merge"` // seconds
	TotalTokensUsed    int       `db:"total_tokens_used" json:"total_tokens_used"`
	TotalLinesChanged  int       `db:"total_lines_changed" json:"total_lines_changed"`
	HasContributing    bool      `db:"has_contributing" json:"has_contributing"`
	HasClaudeMD        bool      `db:"has_claude_md" json:"has_claude_md"`
	CreatedAt          time.Time `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time `db:"updated_at" json:"updated_at"`
}

// SuccessRate returns the success rate for this repository
func (r *RepoMetrics) SuccessRate() float64 {
	if r.AttemptCount == 0 {
		return 0
	}
	return float64(r.SuccessCount) / float64(r.AttemptCount)
}

// MergeRate returns the PR merge rate for this repository
func (r *RepoMetrics) MergeRate() float64 {
	if r.PRsCreated == 0 {
		return 0
	}
	return float64(r.PRsMerged) / float64(r.PRsCreated)
}

// LanguageMetrics holds aggregated metrics per programming language
type LanguageMetrics struct {
	Language           string  `json:"language"`
	SuccessCount       int     `json:"success_count"`
	AttemptCount       int     `json:"attempt_count"`
	SuccessRate        float64 `json:"success_rate"`
	PRsCreated         int     `json:"prs_created"`
	PRsMerged          int     `json:"prs_merged"`
	MergeRate          float64 `json:"merge_rate"`
	AvgDurationSeconds float64 `json:"avg_duration_seconds"`
	TotalTokensUsed    int     `json:"total_tokens_used"`
}

// PRMetrics holds PR-related aggregate statistics
type PRMetrics struct {
	TotalPRs           int     `json:"total_prs"`
	OpenPRs            int     `json:"open_prs"`
	MergedPRs          int     `json:"merged_prs"`
	ClosedPRs          int     `json:"closed_prs"`
	MergeRate          float64 `json:"merge_rate"`
	AvgTimeToMerge     float64 `json:"avg_time_to_merge_hours"`
	AvgTimeToFirstReview float64 `json:"avg_time_to_first_review_hours"`
	AvgReviewComments  float64 `json:"avg_review_comments"`
}

// BlacklistEntry represents a blacklisted repository
type BlacklistEntry struct {
	ID      int64     `db:"id" json:"id"`
	Repo    string    `db:"repo" json:"repo"`
	Reason  string    `db:"reason" json:"reason,omitempty"`
	AddedAt time.Time `db:"added_at" json:"added_at"`
}

// ReviewLesson represents a lesson learned from upstream reviewer feedback.
// These are extracted from PR reviews and used to improve future contributions.
type ReviewLesson struct {
	ID            int64     `db:"id" json:"id"`
	PRID          int64     `db:"pr_id" json:"pr_id"`
	Repo          string    `db:"repo" json:"repo"`
	Category      string    `db:"category" json:"category"`           // testing, style, scope, docs, ci, logic
	Lesson        string    `db:"lesson" json:"lesson"`               // extracted actionable lesson
	SourceComment string    `db:"source_comment" json:"source_comment"` // original reviewer text
	Reviewer      string    `db:"reviewer" json:"reviewer"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

// PipelineEvent records a single agent invocation in the pipeline.
type PipelineEvent struct {
	ID               int64      `db:"id" json:"id"`
	IssueID          int64      `db:"issue_id" json:"issue_id"`
	PRID             *int64     `db:"pr_id" json:"pr_id,omitempty"`
	Repo             string     `db:"repo" json:"repo"`
	IssueNumber      int        `db:"issue_number" json:"issue_number"`
	Stage            string     `db:"stage" json:"stage"`
	Round            int        `db:"round" json:"round"`
	StartedAt        time.Time  `db:"started_at" json:"started_at"`
	CompletedAt      *time.Time `db:"completed_at" json:"completed_at,omitempty"`
	DurationSeconds  float64    `db:"duration_seconds" json:"duration_seconds"`
	OutputSummary    string     `db:"output_summary" json:"output_summary"`
	Verdict          string     `db:"verdict" json:"verdict"`
	Success          bool       `db:"success" json:"success"`
	ErrorMessage     string     `db:"error_message" json:"error_message,omitempty"`
	OutcomeLabel     string     `db:"outcome_label" json:"outcome_label,omitempty"`
	CreatedAt        time.Time  `db:"created_at" json:"created_at"`
}
