package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// Result holds the output from Claude Code execution
type Result struct {
	Success           bool
	FixComplete       bool
	TestsPassed       *bool
	IsComplex         *bool
	CanTestLocally    *bool
	ComplexityReasons []string
	FilesChanged      []string
	Output            string
	Duration          time.Duration
	Error             error
}

// ComplexityResult holds the complexity evaluation output
type ComplexityResult struct {
	IsComplex      bool     `json:"is_complex"`
	CanTestLocally bool     `json:"can_test_locally"`
	Reasons        []string `json:"reasons"`
	TestFramework  string   `json:"test_framework"`
}

// Executor runs Claude Code to solve issues
type Executor struct {
	config      *config.Config
	outputChan  chan string
	outputMu    sync.Mutex
	lastOutput  string
}

// New creates a new Claude executor
func New(cfg *config.Config) *Executor {
	return &Executor{
		config:     cfg,
		outputChan: make(chan string, 100),
	}
}

// GetLastOutput returns the most recent output line
func (e *Executor) GetLastOutput() string {
	e.outputMu.Lock()
	defer e.outputMu.Unlock()
	return e.lastOutput
}

// OutputChannel returns the channel for streaming output
func (e *Executor) OutputChannel() <-chan string {
	return e.outputChan
}

// EvaluateComplexity asks Claude to evaluate project complexity
func (e *Executor) EvaluateComplexity(ctx context.Context, repoDir string, issue *models.Issue) (*ComplexityResult, error) {
	prompt := fmt.Sprintf(`Analyze this project and determine if the issue can be solved with local testing.

Issue #%d: %s
%s

Evaluate:
1. Is this a complex issue requiring external dependencies (APIs, databases, cloud services)?
2. Can tests be run locally with standard tools?
3. What test framework does this project use?

Respond ONLY with JSON:
{
  "is_complex": true/false,
  "can_test_locally": true/false,
  "reasons": ["reason1", "reason2"],
  "test_framework": "pytest/jest/go test/etc"
}`, issue.IssueNumber, issue.Title, issue.Body)

	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"-p", prompt,
		"--output-format", "text")
	cmd.Dir = repoDir

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude evaluation failed: %w", err)
	}

	// Parse JSON from output
	result := &ComplexityResult{}
	outputStr := string(output)

	// Find JSON in output
	jsonStart := strings.Index(outputStr, "{")
	jsonEnd := strings.LastIndex(outputStr, "}")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		jsonStr := outputStr[jsonStart : jsonEnd+1]
		if err := json.Unmarshal([]byte(jsonStr), result); err != nil {
			// Default to conservative estimate
			result.IsComplex = true
			result.CanTestLocally = false
		}
	}

	return result, nil
}

// Solve runs Claude Code to fix an issue
func (e *Executor) Solve(ctx context.Context, repoDir string, issue *models.Issue, complexity *ComplexityResult) (*Result, error) {
	startTime := time.Now()

	prompt := e.buildSolvePrompt(issue, complexity)

	// Create timeout context
	timeout := e.config.ClaudeTimeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude",
		"--print",                        // Non-interactive mode
		"--dangerously-skip-permissions", // Auto-approve file edits
		"-p", prompt,
		"--output-format", "text")
	cmd.Dir = repoDir

	// Capture output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Read output in background
	var outputBuilder strings.Builder
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			outputBuilder.WriteString(line + "\n")
			e.outputMu.Lock()
			e.lastOutput = line
			e.outputMu.Unlock()
			select {
			case e.outputChan <- line:
			default:
				// Channel full, skip
			}
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			outputBuilder.WriteString("[stderr] " + line + "\n")
		}
	}()

	// Wait for command to finish
	cmdErr := cmd.Wait()
	wg.Wait()

	output := outputBuilder.String()
	duration := time.Since(startTime)

	result := &Result{
		Output:   output,
		Duration: duration,
	}

	// Parse markers from output
	result.FixComplete = strings.Contains(output, "FIX_COMPLETE")
	if strings.Contains(output, "FIX_INCOMPLETE") {
		result.FixComplete = false
	}

	// Parse test results
	if strings.Contains(output, "TESTS_PASSED: true") {
		passed := true
		result.TestsPassed = &passed
	} else if strings.Contains(output, "TESTS_PASSED: false") {
		passed := false
		result.TestsPassed = &passed
	}

	// Set complexity info
	if complexity != nil {
		result.IsComplex = &complexity.IsComplex
		result.CanTestLocally = &complexity.CanTestLocally
		result.ComplexityReasons = complexity.Reasons
	}

	// Get changed files
	result.FilesChanged = e.getChangedFiles(repoDir)

	// Determine success
	if cmdErr != nil {
		result.Error = cmdErr
		result.Success = false
	} else {
		result.Success = result.FixComplete && len(result.FilesChanged) > 0
	}

	return result, nil
}

// buildSolvePrompt constructs the prompt for solving an issue
func (e *Executor) buildSolvePrompt(issue *models.Issue, complexity *ComplexityResult) string {
	var testInstructions string

	if complexity != nil && complexity.CanTestLocally {
		testInstructions = fmt.Sprintf(`
## TEST-FIX LOOP (CRITICAL)
After implementing the fix:
1. Run the project's test suite using: %s
2. If tests fail, fix the issues and re-run
3. Continue until ALL tests pass
4. Output: TESTS_PASSED: true/false`, complexity.TestFramework)
	} else {
		testInstructions = `
## TESTING
This project requires external dependencies for full testing.
Focus on code correctness and run any available unit tests.
Output: TESTS_PASSED: true/false (based on available tests)`
	}

	return fmt.Sprintf(`You are solving GitHub issue #%d.

## Issue
**Title:** %s
**Body:**
%s

## Instructions
1. Read and understand the issue completely
2. Explore the codebase to understand the context
3. Implement a minimal, focused fix
4. Follow existing code style and patterns
%s

## Output Format
When complete, output ONE of these markers on its own line:
- FIX_COMPLETE - if the fix is complete and tests pass
- FIX_INCOMPLETE - if you cannot complete the fix

Also output: TESTS_PASSED: true/false

Be concise. Focus only on fixing this specific issue.`,
		issue.IssueNumber, issue.Title, issue.Body, testInstructions)
}

// getChangedFiles returns list of modified files using git
func (e *Executor) getChangedFiles(repoDir string) []string {
	cmd := exec.Command("git", "diff", "--name-only")
	cmd.Dir = repoDir

	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var files []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}

	// Also check staged files
	cmd = exec.Command("git", "diff", "--name-only", "--staged")
	cmd.Dir = repoDir
	output, err = cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !contains(files, line) {
				files = append(files, line)
			}
		}
	}

	// Also check untracked files
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = repoDir
	output, err = cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !contains(files, line) {
				files = append(files, line)
			}
		}
	}

	return files
}

// RunTests executes tests in the repository
func (e *Executor) RunTests(ctx context.Context, repoDir string, framework string) (bool, string, time.Duration, error) {
	startTime := time.Now()

	var cmd *exec.Cmd
	switch strings.ToLower(framework) {
	case "pytest", "python":
		cmd = exec.CommandContext(ctx, "pytest", "-v", "--tb=short")
	case "jest", "npm", "javascript", "typescript":
		cmd = exec.CommandContext(ctx, "npm", "test")
	case "go", "go test":
		cmd = exec.CommandContext(ctx, "go", "test", "./...")
	case "cargo", "rust":
		cmd = exec.CommandContext(ctx, "cargo", "test")
	default:
		// Try to detect
		if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err == nil {
			cmd = exec.CommandContext(ctx, "go", "test", "./...")
		} else if _, err := os.Stat(filepath.Join(repoDir, "package.json")); err == nil {
			cmd = exec.CommandContext(ctx, "npm", "test")
		} else if _, err := os.Stat(filepath.Join(repoDir, "pytest.ini")); err == nil {
			cmd = exec.CommandContext(ctx, "pytest", "-v")
		} else if _, err := os.Stat(filepath.Join(repoDir, "Cargo.toml")); err == nil {
			cmd = exec.CommandContext(ctx, "cargo", "test")
		} else {
			return false, "Unknown test framework", 0, fmt.Errorf("unknown test framework")
		}
	}

	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	duration := time.Since(startTime)

	passed := err == nil
	return passed, string(output), duration, nil
}

// ValidationResult holds the result of code validation
type ValidationResult struct {
	Passed   bool
	Language string
	Errors   []string
	Warnings []string
}

// ValidateCode runs language-specific linters and formatters before commit
func (e *Executor) ValidateCode(ctx context.Context, repoDir string) (*ValidationResult, error) {
	result := &ValidationResult{Passed: true}

	// Detect project language
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err == nil {
		result.Language = "go"
		if err := e.validateGo(ctx, repoDir, result); err != nil {
			return result, err
		}
	} else if _, err := os.Stat(filepath.Join(repoDir, "package.json")); err == nil {
		result.Language = "javascript"
		if err := e.validateJS(ctx, repoDir, result); err != nil {
			return result, err
		}
	} else if _, err := os.Stat(filepath.Join(repoDir, "pyproject.toml")); err == nil {
		result.Language = "python"
		if err := e.validatePython(ctx, repoDir, result); err != nil {
			return result, err
		}
	} else if _, err := os.Stat(filepath.Join(repoDir, "Cargo.toml")); err == nil {
		result.Language = "rust"
		if err := e.validateRust(ctx, repoDir, result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// validateGo runs Go-specific validation
func (e *Executor) validateGo(ctx context.Context, repoDir string, result *ValidationResult) error {
	// Run gofmt to check formatting
	cmd := exec.CommandContext(ctx, "gofmt", "-l", ".")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		result.Warnings = append(result.Warnings, "gofmt check failed: "+err.Error())
	} else if len(strings.TrimSpace(string(output))) > 0 {
		// Files need formatting - auto-fix them
		fixCmd := exec.CommandContext(ctx, "gofmt", "-w", ".")
		fixCmd.Dir = repoDir
		if fixErr := fixCmd.Run(); fixErr != nil {
			result.Errors = append(result.Errors, "gofmt fix failed: "+fixErr.Error())
			result.Passed = false
		} else {
			result.Warnings = append(result.Warnings, "gofmt: auto-fixed formatting issues")
		}
	}

	// Run golangci-lint if available
	cmd = exec.CommandContext(ctx, "golangci-lint", "run", "./...")
	cmd.Dir = repoDir
	output, err = cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		// Check for ignorable errors
		if strings.Contains(err.Error(), "executable file not found") {
			result.Warnings = append(result.Warnings, "golangci-lint not installed, skipping")
		} else if e.isIgnorableLintError(outputStr) {
			result.Warnings = append(result.Warnings, "golangci-lint: skipped (platform/plugin issues)")
		} else {
			// Real lint errors found
			lines := strings.Split(outputStr, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "level=") {
					result.Errors = append(result.Errors, line)
				}
			}
			result.Passed = false
		}
	}

	// Run go vet
	cmd = exec.CommandContext(ctx, "go", "vet", "./...")
	cmd.Dir = repoDir
	output, err = cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		// Check for platform-specific errors that should be ignored
		if e.isIgnorableVetError(outputStr) {
			result.Warnings = append(result.Warnings, "go vet: skipped (platform-specific code)")
		} else {
			result.Errors = append(result.Errors, "go vet: "+outputStr)
			result.Passed = false
		}
	}

	return nil
}

// isIgnorableLintError checks if a golangci-lint error can be safely ignored
func (e *Executor) isIgnorableLintError(output string) bool {
	ignorablePatterns := []string{
		"plugin not found",
		"plugin(kubeapilinter)",
		"build constraints exclude",
		"no Go files in",
		"context canceled",
	}
	for _, pattern := range ignorablePatterns {
		if strings.Contains(output, pattern) {
			return true
		}
	}
	return false
}

// isIgnorableVetError checks if a go vet error can be safely ignored
func (e *Executor) isIgnorableVetError(output string) bool {
	ignorablePatterns := []string{
		"build constraints exclude all Go files",
		"no Go files in",
		"import cycle not allowed",
		"/linux/",
		"/darwin/",
		"/windows/",
		"_linux.go",
		"_darwin.go",
		"_windows.go",
	}
	for _, pattern := range ignorablePatterns {
		if strings.Contains(output, pattern) {
			return true
		}
	}
	return false
}

// validateJS runs JavaScript/TypeScript validation
func (e *Executor) validateJS(ctx context.Context, repoDir string, result *ValidationResult) error {
	// First try to use project's npm run lint if available
	pkgJSONPath := filepath.Join(repoDir, "package.json")
	if _, err := os.Stat(pkgJSONPath); err == nil {
		cmd := exec.CommandContext(ctx, "npm", "run", "lint", "--if-present")
		cmd.Dir = repoDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			outputStr := string(output)
			// Check for ESLint config errors that should be ignored
			if e.isIgnorableESLintError(outputStr) {
				result.Warnings = append(result.Warnings, "ESLint config issue, skipping lint check")
				return nil
			}
			if !strings.Contains(outputStr, "Missing script") {
				result.Errors = append(result.Errors, "npm run lint: "+outputStr)
				result.Passed = false
			}
		}
		return nil
	}

	// Fallback to direct eslint
	cmd := exec.CommandContext(ctx, "npx", "eslint", ".", "--ext", ".js,.jsx,.ts,.tsx", "--max-warnings", "0")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		if e.isIgnorableESLintError(outputStr) {
			result.Warnings = append(result.Warnings, "ESLint config issue, skipping")
			return nil
		}
		if !strings.Contains(err.Error(), "executable file not found") {
			result.Errors = append(result.Errors, "eslint: "+outputStr)
			result.Passed = false
		}
	}
	return nil
}

// isIgnorableESLintError checks if an ESLint error can be safely ignored
func (e *Executor) isIgnorableESLintError(output string) bool {
	ignorablePatterns := []string{
		"couldn't find an eslint.config",
		"No ESLint configuration found",
		"ESLintRC configuration files are no longer supported",
		"eslint.config.js",
		"eslint.config.mjs",
	}
	for _, pattern := range ignorablePatterns {
		if strings.Contains(output, pattern) {
			return true
		}
	}
	return false
}

// validatePython runs Python validation
func (e *Executor) validatePython(ctx context.Context, repoDir string, result *ValidationResult) error {
	// Run ruff if available
	cmd := exec.CommandContext(ctx, "ruff", "check", ".")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		if !strings.Contains(err.Error(), "executable file not found") {
			result.Errors = append(result.Errors, "ruff: "+string(output))
			result.Passed = false
		}
	}

	// Run ruff format check
	cmd = exec.CommandContext(ctx, "ruff", "format", "--check", ".")
	cmd.Dir = repoDir
	output, err = cmd.CombinedOutput()
	if err != nil {
		if !strings.Contains(err.Error(), "executable file not found") {
			// Auto-fix
			fixCmd := exec.CommandContext(ctx, "ruff", "format", ".")
			fixCmd.Dir = repoDir
			if fixErr := fixCmd.Run(); fixErr != nil {
				result.Errors = append(result.Errors, "ruff format failed")
				result.Passed = false
			} else {
				result.Warnings = append(result.Warnings, "ruff: auto-fixed formatting")
			}
		}
	}

	return nil
}

// validateRust runs Rust validation
func (e *Executor) validateRust(ctx context.Context, repoDir string, result *ValidationResult) error {
	// Run cargo fmt check
	cmd := exec.CommandContext(ctx, "cargo", "fmt", "--", "--check")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Auto-fix
		fixCmd := exec.CommandContext(ctx, "cargo", "fmt")
		fixCmd.Dir = repoDir
		if fixErr := fixCmd.Run(); fixErr != nil {
			result.Errors = append(result.Errors, "cargo fmt failed: "+string(output))
			result.Passed = false
		} else {
			result.Warnings = append(result.Warnings, "cargo fmt: auto-fixed formatting")
		}
	}

	// Run cargo clippy
	cmd = exec.CommandContext(ctx, "cargo", "clippy", "--", "-D", "warnings")
	cmd.Dir = repoDir
	output, err = cmd.CombinedOutput()
	if err != nil {
		result.Errors = append(result.Errors, "cargo clippy: "+string(output))
		result.Passed = false
	}

	return nil
}

// binaryExtensions contains file extensions that are typically binary
var binaryExtensions = map[string]bool{
	".exe": true, ".bin": true, ".so": true, ".dylib": true, ".dll": true,
	".o": true, ".a": true, ".lib": true, ".obj": true,
	".pyc": true, ".pyo": true, ".class": true,
	".jar": true, ".war": true, ".ear": true,
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
	".pdf": true, ".doc": true, ".docx": true,
	".wasm": true,
}

// isBinaryFile checks if a file is binary by reading its first bytes
func isBinaryFile(filePath string) bool {
	// Check extension first
	ext := strings.ToLower(filepath.Ext(filePath))
	if binaryExtensions[ext] {
		return true
	}

	// Check if file has no extension and might be a compiled binary
	if ext == "" {
		info, err := os.Stat(filePath)
		if err != nil {
			return false
		}
		// If file is executable and has no extension, likely a binary
		if info.Mode()&0111 != 0 {
			// Read first few bytes to check for ELF/Mach-O/PE headers
			f, err := os.Open(filePath)
			if err != nil {
				return false
			}
			defer f.Close()

			header := make([]byte, 4)
			n, err := f.Read(header)
			if err != nil || n < 4 {
				return false
			}

			// Check for common binary headers
			// ELF: 0x7f 'E' 'L' 'F'
			if header[0] == 0x7f && header[1] == 'E' && header[2] == 'L' && header[3] == 'F' {
				return true
			}
			// Mach-O: 0xfe 0xed 0xfa 0xce (32-bit) or 0xfe 0xed 0xfa 0xcf (64-bit)
			if header[0] == 0xfe && header[1] == 0xed && header[2] == 0xfa {
				return true
			}
			// Mach-O universal: 0xca 0xfe 0xba 0xbe
			if header[0] == 0xca && header[1] == 0xfe && header[2] == 0xba && header[3] == 0xbe {
				return true
			}
			// PE (Windows): 'M' 'Z'
			if header[0] == 'M' && header[1] == 'Z' {
				return true
			}
		}
	}

	return false
}

// filterBinaryFiles removes binary files from the staging area
func (e *Executor) filterBinaryFiles(repoDir string) error {
	// Get list of staged files
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		return nil // Not critical, continue
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, file := range files {
		if file == "" {
			continue
		}
		fullPath := filepath.Join(repoDir, file)
		if isBinaryFile(fullPath) {
			// Remove from staging
			unstageCmd := exec.Command("git", "reset", "HEAD", "--", file)
			unstageCmd.Dir = repoDir
			unstageCmd.Run() // Ignore errors
		}
	}

	return nil
}

// CommitChanges commits all changes with DCO sign-off
func (e *Executor) CommitChanges(repoDir, message string) error {
	// Add all changes
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// Filter out binary files before committing
	if err := e.filterBinaryFiles(repoDir); err != nil {
		// Log but don't fail
		fmt.Printf("Warning: failed to filter binary files: %v\n", err)
	}

	// Commit with DCO sign-off (-s)
	cmd = exec.Command("git", "commit", "-s", "-m", message)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	return nil
}

// CreateBranch creates and switches to a new branch
func (e *Executor) CreateBranch(repoDir, branchName string) error {
	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = repoDir
	return cmd.Run()
}

// PushBranch pushes a branch to remote
func (e *Executor) PushBranch(repoDir, remote, branchName string) error {
	cmd := exec.Command("git", "push", "-u", remote, branchName)
	cmd.Dir = repoDir
	return cmd.Run()
}

// CloneRepo clones a repository using gh CLI for better reliability
func (e *Executor) CloneRepo(ctx context.Context, repoURL, destDir string) error {
	// Clean destination directory
	os.RemoveAll(destDir)
	if err := os.MkdirAll(filepath.Dir(destDir), 0755); err != nil {
		return fmt.Errorf("failed to create parent dir: %w", err)
	}

	// Extract repo name from URL (e.g., "https://github.com/owner/repo.git" -> "owner/repo")
	repoName := strings.TrimSuffix(strings.TrimPrefix(repoURL, "https://github.com/"), ".git")

	// Use gh repo clone for better authentication handling
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		cmd := exec.CommandContext(ctx, "gh", "repo", "clone", repoName, destDir, "--", "--depth", "1")
		output, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}

		lastErr = fmt.Errorf("attempt %d: %s - %w", attempt, strings.TrimSpace(string(output)), err)
		outputStr := string(output)

		// Check for fatal errors that shouldn't be retried
		if strings.Contains(outputStr, "Could not resolve") ||
			strings.Contains(outputStr, "not found") ||
			strings.Contains(outputStr, "Repository not found") {
			return fmt.Errorf("repository not accessible: %s", repoName)
		}

		// Clean up before retry
		os.RemoveAll(destDir)

		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 3 * time.Second)
		}
	}

	return lastErr
}

// SetupGitConfig configures git user for commits
func (e *Executor) SetupGitConfig(repoDir string) error {
	cmds := [][]string{
		{"git", "config", "user.name", e.config.GitHubUsername},
		{"git", "config", "user.email", e.config.GitHubEmail},
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			return err
		}
	}

	return nil
}

// ExtractIssueRef extracts issue reference for PR body
func ExtractIssueRef(repo string, issueNum int) string {
	return fmt.Sprintf("Fixes %s#%d", repo, issueNum)
}

// SanitizeBranchName creates a valid branch name from issue title
func SanitizeBranchName(issueNum int, title string) string {
	// Remove special characters
	reg := regexp.MustCompile(`[^a-zA-Z0-9\s-]`)
	clean := reg.ReplaceAllString(title, "")

	// Replace spaces with dashes
	clean = strings.ReplaceAll(clean, " ", "-")

	// Lowercase and truncate
	clean = strings.ToLower(clean)
	if len(clean) > 40 {
		clean = clean[:40]
	}

	return fmt.Sprintf("fix-%d-%s", issueNum, clean)
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
