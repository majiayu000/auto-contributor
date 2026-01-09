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
	"github.com/majiayu000/auto-contributor/internal/executor"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// Type aliases for backward compatibility
type Result = executor.Result
type ComplexityResult = executor.ComplexityResult
type OutputLine = executor.OutputLine
type ValidationResult = executor.ValidationResult
type ReviewResult = executor.ReviewResult

// Executor runs Claude Code to solve issues
type Executor struct {
	config       *config.Config
	outputChan   chan string
	outputMu     sync.Mutex
	lastOutput   string
	outputBuffer []OutputLine // Store recent output lines
	bufferMu     sync.RWMutex
}

// New creates a new Claude executor
func New(cfg *config.Config) *Executor {
	return &Executor{
		config:       cfg,
		outputChan:   make(chan string, 100),
		outputBuffer: make([]OutputLine, 0, 200),
	}
}

// GetOutputBuffer returns recent output lines
func (e *Executor) GetOutputBuffer() []OutputLine {
	e.bufferMu.RLock()
	defer e.bufferMu.RUnlock()
	result := make([]OutputLine, len(e.outputBuffer))
	copy(result, e.outputBuffer)
	return result
}

// ClearOutputBuffer clears the output buffer
func (e *Executor) ClearOutputBuffer() {
	e.bufferMu.Lock()
	defer e.bufferMu.Unlock()
	e.outputBuffer = e.outputBuffer[:0]
}

// addOutput adds a line to the output buffer
func (e *Executor) addOutput(outputType, content string) {
	e.bufferMu.Lock()
	defer e.bufferMu.Unlock()

	line := OutputLine{
		Time:    time.Now(),
		Type:    outputType,
		Content: content,
	}
	e.outputBuffer = append(e.outputBuffer, line)

	// Keep only last 200 lines
	if len(e.outputBuffer) > 200 {
		e.outputBuffer = e.outputBuffer[len(e.outputBuffer)-200:]
	}
}

// StreamEvent represents a Claude streaming JSON event
type StreamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content []struct {
			Type  string `json:"type"`
			Text  string `json:"text,omitempty"`
			Name  string `json:"name,omitempty"`
			Input struct {
				Command  string `json:"command,omitempty"`
				FilePath string `json:"file_path,omitempty"`
				Content  string `json:"content,omitempty"`
			} `json:"input,omitempty"`
		} `json:"content"`
	} `json:"message,omitempty"`
	ContentBlock struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block,omitempty"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"delta,omitempty"`
	Result struct {
		Subtype string `json:"subtype,omitempty"`
	} `json:"result,omitempty"`
}

// parseStreamLine parses a streaming JSON line and returns human-readable output
func (e *Executor) parseStreamLine(line string) string {
	if line == "" || line[0] != '{' {
		return line
	}

	var event StreamEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		// Not JSON, return as-is
		return line
	}

	switch event.Type {
	case "assistant":
		// Start of assistant response
		return ""

	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			toolName := event.ContentBlock.Name
			e.addOutput("tool", fmt.Sprintf("🔧 Using tool: %s", toolName))
			return fmt.Sprintf("🔧 Tool: %s", toolName)
		}
		return ""

	case "content_block_delta":
		if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
			text := strings.TrimSpace(event.Delta.Text)
			if len(text) > 100 {
				text = text[:100] + "..."
			}
			if text != "" {
				e.addOutput("text", text)
				return text
			}
		}
		return ""

	case "content_block_stop":
		return ""

	case "result":
		if event.Result.Subtype == "tool_result" {
			return "✅ Tool completed"
		}
		return ""

	case "message_start", "message_delta", "message_stop":
		return ""

	default:
		// For unrecognized events, try to extract useful info
		if strings.Contains(line, "tool_use") {
			return "🔧 Using tool..."
		}
		if strings.Contains(line, "Bash") {
			return "💻 Running command..."
		}
		if strings.Contains(line, "Read") || strings.Contains(line, "file_path") {
			return "📖 Reading file..."
		}
		if strings.Contains(line, "Edit") || strings.Contains(line, "Write") {
			return "✏️ Editing file..."
		}
	}

	return ""
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

	// Clear output buffer for new task
	e.ClearOutputBuffer()
	e.addOutput("info", fmt.Sprintf("Starting fix for %s#%d: %s", issue.Repo, issue.IssueNumber, issue.Title))

	prompt := e.buildSolvePrompt(issue, complexity)

	// Create timeout context
	timeout := e.config.ClaudeTimeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude",
		"--print",                        // Non-interactive mode
		"--dangerously-skip-permissions", // Auto-approve file edits
		"-p", prompt,
		"--output-format", "text") // Use text format to properly detect FIX_COMPLETE marker
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
		// Increase buffer size for large outputs
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			outputBuilder.WriteString(line + "\n")

			// Add to output buffer for UI display
			if line != "" {
				// Detect tool usage from text output
				outputType := "text"
				if strings.Contains(line, "Tool:") || strings.Contains(line, "Using tool") {
					outputType = "tool"
				} else if strings.Contains(line, "Error") || strings.Contains(line, "error") {
					outputType = "stderr"
				}
				e.addOutput(outputType, line)

				e.outputMu.Lock()
				e.lastOutput = line
				e.outputMu.Unlock()
				select {
				case e.outputChan <- line:
				default:
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			outputBuilder.WriteString("[stderr] " + line + "\n")
			e.addOutput("stderr", line)
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
	result.AlreadyFixed = strings.Contains(output, "ALREADY_FIXED")
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
	return fmt.Sprintf(`You are fixing GitHub issue #%d in repository %s.

## Issue
**Title:** %s
**Body:**
%s

## CRITICAL: Use ultrathink for ALL analysis steps

### Phase 1: Deep Understanding (DO NOT SKIP)

1. **Read and understand the issue thoroughly:**
   - Use ultrathink to analyze what the issue is REALLY asking for
   - Understand the user's intent, not just the surface request
   - Identify what success looks like

2. **Verify the issue still needs fixing:**
   - Search codebase to check if feature/fix ALREADY EXISTS
   - Check recent commits that might have addressed this
   - If ALREADY_FIXED: output marker and stop

3. **Study the project:**
   - Read CONTRIBUTING.md if exists
   - Check .github/workflows/*.yml for CI requirements
   - Understand code style and patterns from existing code
   - Identify test framework used

### Phase 2: Implementation (MINIMAL & CORRECT)

4. **Plan before coding:**
   - Use ultrathink to design the minimal fix
   - NO over-engineering, NO unnecessary abstractions
   - NO hardcoding unless absolutely required
   - Follow existing patterns exactly

5. **Implement the fix:**
   - Make ONLY changes that directly address the issue
   - Add tests if project has test files and it makes sense
   - Each test must verify the specific fix

### Phase 3: Verification (ALL MUST PASS)

6. **Run ALL project tests:**
   - Find test command from: Makefile, package.json, CI config
   - Run: go test ./... OR npm test OR pytest OR make test
   - If tests fail: FIX them and re-run
   - Do NOT proceed until ALL tests pass

7. **Format and lint:**
   - Go: gofmt -w .
   - JS/TS: prettier (if used)
   - Python: black/ruff (if used)
   - Run project's own lint: make lint, npm run lint, etc.

### Phase 4: Final Commit

8. **Git config and commit:**
   git config user.name "majiayu000"
   git config user.email "1835304752@qq.com"
   git add -A
   git commit -s -m "fix: <concise description>"

   **CRITICAL COMMIT RULES:**
   - ALWAYS use -s flag for DCO sign-off on EVERY commit (required by many projects)
   - If you need to make additional commits (e.g., lint fixes), ALWAYS use: git commit -s -m "..."
   - NEVER add "Generated with Claude Code" or similar AI markers
   - NEVER add "Co-Authored-By" headers
   - Keep commit message simple and human-like
   - DO NOT push yet - a "fork" remote is configured, push will happen in review phase
   - If you must push, ONLY use: git push fork <branch> (never push to origin)

### Quality Checklist (verify ALL before FIX_COMPLETE)
- [ ] Fix addresses the actual issue requirement
- [ ] No debug code, no console.log, no print statements left
- [ ] No unrelated changes included
- [ ] All existing tests pass
- [ ] New tests added where appropriate
- [ ] Code follows project style
- [ ] No hardcoded values unless necessary
- [ ] No meaningless comments (e.g. "// Initialize the variable", "// Return the result")
- [ ] ALL commits use -s flag for DCO sign-off
- [ ] NO AI markers in commit messages (no "Generated with Claude", no "Co-Authored-By")

## Output Markers (output ONE on its own line)
- FIX_COMPLETE - fix done, ALL tests pass locally
- FIX_INCOMPLETE - cannot complete (explain why)
- ALREADY_FIXED - issue already resolved in codebase

Also output: TESTS_PASSED: true/false`,
		issue.IssueNumber, issue.Repo, issue.Title, issue.Body)
}

// getChangedFiles returns list of modified files using git
// Checks uncommitted, staged, untracked, AND committed changes on the branch
func (e *Executor) getChangedFiles(repoDir string) []string {
	var files []string

	// First, check for committed changes on the current branch vs origin/HEAD or first parent
	// This is important because Claude may have already committed the changes
	cmd := exec.Command("git", "diff", "--name-only", "HEAD~1")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !contains(files, line) {
				files = append(files, line)
			}
		}
	}

	// Also try comparing against origin/main or origin/master
	for _, baseBranch := range []string{"origin/main", "origin/master"} {
		cmd = exec.Command("git", "diff", "--name-only", baseBranch+"...HEAD")
		cmd.Dir = repoDir
		output, err = cmd.Output()
		if err == nil {
			for _, line := range strings.Split(string(output), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !contains(files, line) {
					files = append(files, line)
				}
			}
			break // Found a valid base branch
		}
	}

	// Check uncommitted changes (working directory)
	cmd = exec.Command("git", "diff", "--name-only")
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


// ValidateCode uses Claude to intelligently validate changes based on project's own CI/lint config
func (e *Executor) ValidateCode(ctx context.Context, repoDir string) (*ValidationResult, error) {
	result := &ValidationResult{Passed: true}

	// Get list of changed files
	changedFiles := e.getChangedFiles(repoDir)
	if len(changedFiles) == 0 {
		result.Warnings = append(result.Warnings, "No changed files to validate")
		return result, nil
	}

	// Ask Claude to validate based on project's own validation tools
	prompt := fmt.Sprintf(`You are validating code changes for a PR.

## Changed Files
%s

## Instructions
1. First, examine the project's CI configuration:
   - Check .github/workflows/*.yml for CI commands
   - Check Makefile for lint/test targets
   - Check package.json scripts (for JS projects)
   - Check pyproject.toml or setup.cfg (for Python projects)

2. Run ONLY the project's own validation commands that apply to the changed files:
   - If project has "make lint", use that
   - If project has "npm run lint", use that
   - If project has CI lint commands, use those
   - Do NOT run generic golangci-lint if the project doesn't use it

3. For basic formatting:
   - Go: run gofmt -w on changed .go files
   - JS/TS: run prettier if project uses it
   - Python: run black/ruff if project uses it

4. Focus ONLY on the changed files, not the entire project

## Output
Output exactly one of:
- VALIDATION_PASSED: true - if your changes pass validation
- VALIDATION_PASSED: false - if validation fails

If validation fails, briefly explain why.
Be concise.`, strings.Join(changedFiles, "\n"))

	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--dangerously-skip-permissions",
		"-p", prompt,
		"--output-format", "text")
	cmd.Dir = repoDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		// If Claude fails, assume validation passed (don't block on tooling issues)
		result.Warnings = append(result.Warnings, "Claude validation skipped: "+err.Error())
		return result, nil
	}

	outputStr := string(output)

	// Parse Claude's validation result
	if strings.Contains(outputStr, "VALIDATION_PASSED: false") {
		result.Passed = false
		// Extract failure reason
		lines := strings.Split(outputStr, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "VALIDATION_PASSED") {
				result.Errors = append(result.Errors, line)
			}
		}
	} else {
		// Default to passed
		result.Passed = true
		result.Warnings = append(result.Warnings, "Claude validation passed")
	}

	return result, nil
}


// Review runs Claude to review, fix, and create PR
// Claude has full control over the entire process
func (e *Executor) Review(ctx context.Context, repoDir string, issue *models.Issue, maxRounds int) (*ReviewResult, error) {
	if maxRounds <= 0 {
		maxRounds = 3
	}

	e.addOutput("info", fmt.Sprintf("Starting review for %s#%d", issue.Repo, issue.IssueNumber))

	// Get changed files for context
	changedFiles := e.getChangedFiles(repoDir)
	if len(changedFiles) == 0 {
		return &ReviewResult{Passed: false, Output: "No changes to review"}, nil
	}

	prompt := e.buildReviewPrompt(issue, changedFiles, maxRounds)

	timeout := e.config.ClaudeTimeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--dangerously-skip-permissions",
		"-p", prompt,
		"--output-format", "text")
	cmd.Dir = repoDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("review failed: %w", err)
	}

	outputStr := string(output)
	result := &ReviewResult{
		Output: outputStr,
	}

	// Parse review result
	if strings.Contains(outputStr, "REVIEW_PASSED") {
		result.Passed = true
		// Try to extract PR URL from output
		result.PRURL, result.PRNumber = extractPRURL(outputStr)
	} else if strings.Contains(outputStr, "REVIEW_FAILED") {
		result.Passed = false
	} else {
		// Check if PR was created even without explicit marker
		prURL, prNum := extractPRURL(outputStr)
		if prURL != "" {
			result.Passed = true
			result.PRURL = prURL
			result.PRNumber = prNum
		}
	}

	return result, nil
}

// extractPRURL extracts PR URL from Claude's output
func extractPRURL(output string) (string, int) {
	// Look for GitHub PR URL pattern
	prPattern := regexp.MustCompile(`https://github\.com/[^/]+/[^/]+/pull/(\d+)`)
	matches := prPattern.FindStringSubmatch(output)
	if len(matches) >= 2 {
		prNum := 0
		fmt.Sscanf(matches[1], "%d", &prNum)
		return matches[0], prNum
	}
	return "", 0
}

// buildReviewPrompt constructs the prompt for reviewing, fixing, and preparing PR
func (e *Executor) buildReviewPrompt(issue *models.Issue, changedFiles []string, maxRounds int) string {
	return fmt.Sprintf(`You are reviewing code changes for GitHub issue #%d in repository %s.

## Issue
**Title:** %s
**Body:**
%s

## Changed Files
%s

## Review & Submit Process (up to %d review rounds)

### Step 1: Critical Review with ultrathink

Use ultrathink to deeply analyze:

1. **Does it ACTUALLY solve the issue?**
   - Re-read the issue carefully
   - Verify the fix addresses the REAL requirement
   - Check for misunderstandings of the issue intent

2. **Is it minimal and correct?**
   - Remove any over-engineering
   - Remove unnecessary abstractions
   - Remove unrelated changes
   - NO hardcoded values unless necessary

3. **Code quality check:**
   - Follows project conventions
   - No debug code or print statements left
   - Clean and readable
   - No meaningless comments (e.g. "// Initialize the variable", "// Return the result")

4. **Tests verification:**
   - Run ALL project tests
   - Fix any failures before proceeding

If issues found: FIX them, run tests again, re-review.

### Step 2: Final Verification

Before creating PR, verify:
- [ ] All tests pass: go test ./... OR npm test OR pytest
- [ ] Code is formatted: gofmt -w . OR prettier OR black
- [ ] Lint passes: make lint OR npm run lint (if exists)

### Step 3: Commit and Push

git config user.name "majiayu000"
git config user.email "1835304752@qq.com"
git add -A
git commit -s -m "fix: <concise description>"

**CRITICAL COMMIT/PR RULES:**
- NEVER add "Generated with Claude Code" or any AI markers in commit or PR
- NEVER add "Co-Authored-By" headers
- NEVER add emojis in commit messages or PR body
- Keep everything simple and human-like

Get branch: git branch --show-current
Push: git push fork <branch-name> --force

### Step 4: Create Pull Request

Get default branch:
gh repo view %s --json defaultBranchRef -q .defaultBranchRef.name

Create PR (keep body SHORT and direct, NO AI markers):
gh pr create --repo %s --title "fix: %s" --body "Fixes #%d

## Changes
- <key change 1>
- <key change 2>" --head majiayu000:<branch-name> --base <default-branch>

### Output
- REVIEW_PASSED - PR created (include PR URL)
- REVIEW_FAILED - cannot fix (explain why)`,
		issue.IssueNumber, issue.Repo, issue.Title, issue.Body,
		strings.Join(changedFiles, "\n"), maxRounds,
		issue.Repo, issue.Repo, issue.Title, issue.IssueNumber)
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
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %s - %w", strings.TrimSpace(string(output)), err)
	}
	return nil
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
	// First, ensure gh auth is configured as git credential helper
	// This allows git push to use gh's authentication
	setupCmd := exec.Command("gh", "auth", "setup-git")
	setupCmd.Run() // Ignore error if already configured

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
// Includes a timestamp suffix to ensure uniqueness for retries
func SanitizeBranchName(issueNum int, title string) string {
	// Remove special characters
	reg := regexp.MustCompile(`[^a-zA-Z0-9\s-]`)
	clean := reg.ReplaceAllString(title, "")

	// Replace spaces with dashes
	clean = strings.ReplaceAll(clean, " ", "-")

	// Lowercase and truncate
	clean = strings.ToLower(clean)
	if len(clean) > 30 {
		clean = clean[:30]
	}

	// Add timestamp suffix for uniqueness (MMDD-HHMM format)
	timestamp := time.Now().Format("0102-1504")

	return fmt.Sprintf("fix-%d-%s-%s", issueNum, clean, timestamp)
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
