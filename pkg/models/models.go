package models

import (
	"time"
)

// IssueStatus represents the status of an issue in the pipeline
type IssueStatus string

const (
	IssueStatusDiscovered IssueStatus = "discovered"
	IssueStatusProcessing IssueStatus = "processing"
	IssueStatusPRCreated  IssueStatus = "pr_created"
	IssueStatusMerged     IssueStatus = "merged"
	IssueStatusAbandoned  IssueStatus = "abandoned"
)

// PRStatus represents the status of a pull request
type PRStatus string

const (
	PRStatusOpen   PRStatus = "open"
	PRStatusMerged PRStatus = "merged"
	PRStatusClosed PRStatus = "closed"
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
	case FailureReasonAlreadyHasPR, FailureReasonComplexityHigh:
		return false
	default:
		return false
	}
}

// PullRequest represents a created pull request
type PullRequest struct {
	ID         int64     `db:"id"`
	IssueID    int64     `db:"issue_id"`
	PRURL      string    `db:"pr_url"`
	PRNumber   int       `db:"pr_number"`
	BranchName string    `db:"branch_name"`
	Status     PRStatus  `db:"status"`
	CIStatus   string    `db:"ci_status"`
	RetryCount int       `db:"retry_count"`
	CreatedAt  time.Time `db:"created_at"`
	UpdatedAt  time.Time `db:"updated_at"`
}

// SolveAttempt records each attempt to solve an issue
type SolveAttempt struct {
	ID                  int64         `db:"id"`
	IssueID             int64         `db:"issue_id"`
	AttemptNumber       int           `db:"attempt_number"`
	StartedAt           time.Time     `db:"started_at"`
	CompletedAt         *time.Time    `db:"completed_at"`
	DurationSeconds     float64       `db:"duration_seconds"`
	PromptVersion       string        `db:"prompt_version"`
	ModelUsed           string        `db:"model_used"`
	FilesChanged        string        `db:"files_changed"` // JSON array
	ClaudeOutputPreview string        `db:"claude_output_preview"`
	FixCompleteMarker   bool          `db:"fix_complete_marker"`
	ClaudeTestsPassed   *bool         `db:"claude_tests_passed"`
	IsComplex           *bool         `db:"is_complex"`
	CanTestLocally      *bool         `db:"can_test_locally"`
	ComplexityReasons   string        `db:"complexity_reasons"` // JSON array
	ExternalTestPassed  *bool         `db:"external_test_passed"`
	TestFramework       string        `db:"test_framework"`
	TestDurationSeconds float64       `db:"test_duration_seconds"`
	TestOutputPreview   string        `db:"test_output_preview"`
	Success             bool          `db:"success"`
	FailureReason       FailureReason `db:"failure_reason"`
	ErrorDetails        string        `db:"error_details"`
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

// WorkerState represents the current state of a worker
type WorkerState struct {
	ID             int       `json:"id"`
	Status         string    `json:"status"` // idle, running, error
	CurrentIssue   *Issue    `json:"current_issue,omitempty"`
	Phase          string    `json:"phase"` // cloning, solving, testing, pr_creating
	Progress       float64   `json:"progress"`
	LastOutput     string    `json:"last_output"`
	StartedAt      time.Time `json:"started_at"`
	TasksCompleted int       `json:"tasks_completed"`
	TasksFailed    int       `json:"tasks_failed"`
}

// SystemStats represents overall system statistics
type SystemStats struct {
	ActiveWorkers     int           `json:"active_workers"`
	IdleWorkers       int           `json:"idle_workers"`
	QueueSize         int           `json:"queue_size"`
	TodayAttempted    int           `json:"today_attempted"`
	TodaySolved       int           `json:"today_solved"`
	TodaySuccessRate  float64       `json:"today_success_rate"`
	AvgSolveTime      time.Duration `json:"avg_solve_time"`
	Workers           []WorkerState `json:"workers"`
}
