package executor

import (
	"context"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// Executor defines the interface for AI-powered code execution tools
// Both Claude and Codex implement this interface
type Executor interface {
	// EvaluateComplexity analyzes a project and determines if the issue can be solved with local testing
	EvaluateComplexity(ctx context.Context, repoDir string, issue *models.Issue) (*ComplexityResult, error)

	// Solve runs the AI to fix an issue
	Solve(ctx context.Context, repoDir string, issue *models.Issue, complexity *ComplexityResult) (*Result, error)

	// Review runs the AI to review, fix, and create PR
	Review(ctx context.Context, repoDir string, issue *models.Issue, maxRounds int) (*ReviewResult, error)

	// ValidateCode uses AI to intelligently validate changes based on project's own CI/lint config
	ValidateCode(ctx context.Context, repoDir string) (*ValidationResult, error)

	// RunTests executes tests in the repository
	RunTests(ctx context.Context, repoDir string, framework string) (passed bool, output string, duration time.Duration, err error)

	// Git operations
	CloneRepo(ctx context.Context, repoURL, destDir string) error
	SetupGitConfig(repoDir string) error
	CreateBranch(repoDir, branchName string) error
	PushBranch(repoDir, remote, branchName string) error
	CommitChanges(repoDir, message string) error

	// Output buffer management
	GetOutputBuffer() []OutputLine
	ClearOutputBuffer()
	GetLastOutput() string
}
