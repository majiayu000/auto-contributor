package pipeline

// ScoutResult is the structured output from the scout agent.
type ScoutResult struct {
	Verdict             string `json:"verdict"` // PROCEED or SKIP
	Reason              string `json:"reason"`
	Difficulty          int    `json:"difficulty"`
	HasCompetingPR      bool   `json:"has_competing_pr"`
	HasUpstreamRedirect bool   `json:"has_upstream_redirect"`
	IsAssigned          bool   `json:"is_assigned"`
	IsStale             bool   `json:"is_stale"`
	MaintainerDirection string `json:"maintainer_direction"`
	SuggestedApproach   string `json:"suggested_approach"`
}

// AnalystResult is the structured output from the analyst agent.
type AnalystResult struct {
	CanFix           bool     `json:"can_fix"`
	Reason           string   `json:"reason"`
	BaseBranch       string   `json:"base_branch"`
	CommitFormat     string   `json:"commit_format"`
	BranchName       string   `json:"branch_name"`
	ContributingRules []string `json:"contributing_rules"`
	CICommands       CICommands `json:"ci_commands"`
	FixPlan          FixPlan    `json:"fix_plan"`
}

// CICommands holds the project's CI/CD verification commands.
type CICommands struct {
	Lint      string `json:"lint"`
	Test      string `json:"test"`
	Typecheck string `json:"typecheck"`
	Build     string `json:"build"`
}

// FixPlan describes what the engineer should do.
type FixPlan struct {
	FilesToModify []string `json:"files_to_modify"`
	FilesToAdd    []string `json:"files_to_add"`
	Description   string   `json:"description"`
	TestStrategy  string   `json:"test_strategy"`
}

// CodeReviewResult is the structured output from the reviewer agent.
// Named differently from executor.ReviewResult which serves V1 review flow.
type CodeReviewResult struct {
	Verdict            string        `json:"verdict"` // approve or rework
	Confidence         float64       `json:"confidence"`
	IssuesFound        []ReviewIssue `json:"issues_found"`
	ReworkInstructions string        `json:"rework_instructions"`
	Summary            string        `json:"summary"`
}

// ReviewIssue is a single issue found during review.
type ReviewIssue struct {
	Severity    string `json:"severity"` // critical, major, minor, nit
	Category    string `json:"category"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion"`
}

// SubmitResult is the structured output from the submitter agent.
type SubmitResult struct {
	Status   string `json:"status"` // submitted or aborted
	Reason   string `json:"reason"`
	PRURL    string `json:"pr_url"`
	PRNumber int    `json:"pr_number"`
	IsDraft  bool   `json:"is_draft"`
}

// FeedbackResult is the structured output from the responder agent.
type FeedbackResult struct {
	Action       string          `json:"action"` // addressed, replied_only, no_action, close
	FilesChanged []string        `json:"files_changed"`
	CommitMsg    string          `json:"commit_message"`
	Replies      []FeedbackReply `json:"replies"`
	Summary      string          `json:"summary"`
}

// FeedbackReply is a reply to a specific review comment.
type FeedbackReply struct {
	CommentID int64  `json:"comment_id"`
	Body      string `json:"body"`
}
