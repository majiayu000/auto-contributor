package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type ClaudeErrorClass string

const (
	ClaudeErrorClassQuotaBilling ClaudeErrorClass = "quota_billing"
	ClaudeErrorClassThrottle     ClaudeErrorClass = "throttle"
)

// ClaudeExecutionError marks Claude CLI failures that callers may handle specially.
type ClaudeExecutionError struct {
	Class  ClaudeErrorClass
	Stderr string
	Err    error
}

func (e *ClaudeExecutionError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	if msg == "" {
		msg = "unknown claude failure"
	}
	return fmt.Sprintf("claude %s: %s", e.Class, msg)
}

func (e *ClaudeExecutionError) Unwrap() error {
	return e.Err
}

func IsClaudeQuotaBillingError(err error) bool {
	var claudeErr *ClaudeExecutionError
	return errors.As(err, &claudeErr) && claudeErr.Class == ClaudeErrorClassQuotaBilling
}

func IsClaudeThrottleError(err error) bool {
	var claudeErr *ClaudeExecutionError
	return errors.As(err, &claudeErr) && claudeErr.Class == ClaudeErrorClassThrottle
}

// ClaudeRuntime executes prompts via Claude Code CLI.
type ClaudeRuntime struct {
	cliPath string
}

func NewClaude(cliPath string) *ClaudeRuntime {
	if cliPath == "" {
		cliPath = "claude"
	}
	return &ClaudeRuntime{cliPath: cliPath}
}

func (r *ClaudeRuntime) Name() string { return "claude" }

func (r *ClaudeRuntime) Execute(ctx context.Context, workDir string, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, r.cliPath,
		"--print",
		"--dangerously-skip-permissions",
		"-p", prompt,
		"--output-format", "text",
	)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), wrapClaudeCommandError(ctx, err, stderr.String())
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		if err := wrapClaudeEmptyOutputError(stderr.String()); err != nil {
			return "", err
		}
		return "", fmt.Errorf("claude returned empty output (silent failure)")
	}
	return output, nil
}

// ExecuteJSON runs a prompt with a JSON schema constraint for reliable structured output.
// Uses --output-format json --json-schema for constrained decoding (~99% reliability).
// Returns the structured_output field from the JSON envelope, falling back to result.
func (r *ClaudeRuntime) ExecuteJSON(ctx context.Context, workDir string, prompt string, jsonSchema string) (string, error) {
	cmd := exec.CommandContext(ctx, r.cliPath,
		"--print",
		"--dangerously-skip-permissions",
		"-p", prompt,
		"--output-format", "json",
		"--json-schema", jsonSchema,
	)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), wrapClaudeCommandError(ctx, err, stderr.String())
	}

	raw := stdout.String()
	if strings.TrimSpace(raw) == "" {
		if err := wrapClaudeEmptyOutputError(stderr.String()); err != nil {
			return "", err
		}
		return "", fmt.Errorf("claude returned empty output")
	}

	// Extract structured_output from the JSON envelope
	var envelope struct {
		StructuredOutput json.RawMessage `json:"structured_output"`
		Result           string          `json:"result"`
		IsError          bool            `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		// Not a JSON envelope — return raw for caller to parse
		return raw, nil
	}
	if envelope.IsError {
		return "", fmt.Errorf("claude returned error in JSON envelope")
	}
	if len(envelope.StructuredOutput) > 0 {
		return string(envelope.StructuredOutput), nil
	}
	if envelope.Result != "" {
		return envelope.Result, nil
	}
	return raw, nil
}

func (r *ClaudeRuntime) ExecuteStdin(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, r.cliPath,
		"--print",
		"--dangerously-skip-permissions",
		"--output-format", "text",
	)
	cmd.Stdin = strings.NewReader(prompt)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("claude timed out: %w", ctx.Err())
		}
		return "", wrapClaudeCommandError(ctx, err, string(output))
	}
	return string(output), nil
}

func wrapClaudeCommandError(ctx context.Context, runErr error, stderr string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("claude timed out: %w", ctx.Err())
	}

	stderrMsg := strings.TrimSpace(stderr)
	switch classifyClaudeError(stderrMsg) {
	case ClaudeErrorClassQuotaBilling:
		return &ClaudeExecutionError{Class: ClaudeErrorClassQuotaBilling, Stderr: stderrMsg, Err: runErr}
	case ClaudeErrorClassThrottle:
		return &ClaudeExecutionError{Class: ClaudeErrorClassThrottle, Stderr: stderrMsg, Err: runErr}
	}

	if stderrMsg == "" {
		return fmt.Errorf("claude exited with error: %w", runErr)
	}
	return fmt.Errorf("claude exited with error: %w\nstderr: %s", runErr, stderrMsg)
}

func wrapClaudeEmptyOutputError(stderr string) error {
	stderrMsg := strings.TrimSpace(stderr)
	switch classifyClaudeError(stderrMsg) {
	case ClaudeErrorClassQuotaBilling:
		return &ClaudeExecutionError{Class: ClaudeErrorClassQuotaBilling, Stderr: stderrMsg}
	case ClaudeErrorClassThrottle:
		return &ClaudeExecutionError{Class: ClaudeErrorClassThrottle, Stderr: stderrMsg}
	}
	if stderrMsg == "" {
		return nil
	}
	return fmt.Errorf("claude returned empty output, stderr: %s", stderrMsg)
}

func classifyClaudeError(stderr string) ClaudeErrorClass {
	msg := strings.ToLower(strings.TrimSpace(stderr))
	if msg == "" {
		return ""
	}

	for _, needle := range []string{
		"quota exceeded",
		"quota has been exceeded",
		"billing",
		"payment required",
		"usage limit reached",
		"reached your usage limit",
		"insufficient credits",
		"credit balance is too low",
		"purchase more credits",
	} {
		if strings.Contains(msg, needle) {
			return ClaudeErrorClassQuotaBilling
		}
	}

	for _, needle := range []string{
		"429",
		"rate limit",
		"too many requests",
		"overloaded",
		"try again later",
		"throttle",
	} {
		if strings.Contains(msg, needle) {
			return ClaudeErrorClassThrottle
		}
	}

	return ""
}
