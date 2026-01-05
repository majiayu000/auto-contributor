package executor

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Result holds the output from AI code execution
type Result struct {
	Success           bool
	FixComplete       bool
	AlreadyFixed      bool
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

// OutputLine represents a single output event
type OutputLine struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`    // "stdout", "stderr", "tool", "thinking"
	Content string    `json:"content"`
}

// ValidationResult holds the result of code validation
type ValidationResult struct {
	Passed   bool
	Language string
	Errors   []string
	Warnings []string
}

// ReviewResult holds the result of code review
type ReviewResult struct {
	Passed   bool
	PRURL    string
	PRNumber int
	Output   string
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
