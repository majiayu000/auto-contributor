package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	jsonrepair "github.com/RealAlexandreAI/json-repair"
	"github.com/majiayu000/auto-contributor/internal/prompt"
)

// AgentRunner executes Claude CLI with a rendered prompt and parses JSON output.
type AgentRunner struct {
	prompts *prompt.Store
	timeout time.Duration
}

// NewAgentRunner creates a runner bound to the given prompt store.
func NewAgentRunner(ps *prompt.Store, timeout time.Duration) *AgentRunner {
	return &AgentRunner{prompts: ps, timeout: timeout}
}

// Run renders the named prompt template with ctx, invokes Claude CLI in workDir,
// and returns the raw output string.
func (r *AgentRunner) Run(ctx context.Context, agentName string, workDir string, tmplCtx map[string]any) (string, error) {
	rendered, err := r.prompts.Render(agentName, tmplCtx)
	if err != nil {
		return "", fmt.Errorf("render prompt %s: %w", agentName, err)
	}
	return r.runWithPrompt(ctx, agentName, workDir, rendered)
}

// RunJSON is like Run but also extracts a JSON object from the output.
// If the first attempt fails to parse JSON, it retries once with an explicit
// "output only JSON" instruction appended to the prompt.
func (r *AgentRunner) RunJSON(ctx context.Context, agentName string, workDir string, tmplCtx map[string]any, dest any) (string, error) {
	rendered, err := r.prompts.Render(agentName, tmplCtx)
	if err != nil {
		return "", fmt.Errorf("render prompt %s: %w", agentName, err)
	}

	raw, err := r.runWithPrompt(ctx, agentName, workDir, rendered)
	if err != nil {
		return raw, err
	}

	if err := extractJSON(raw, dest); err == nil {
		return raw, nil
	}

	// Retry with explicit JSON-only instruction
	retryPrompt := rendered + "\n\nOutput ONLY the JSON object, no markdown, no explanation."
	raw, err = r.runWithPrompt(ctx, agentName, workDir, retryPrompt)
	if err != nil {
		return raw, err
	}

	if err := extractJSON(raw, dest); err != nil {
		return raw, fmt.Errorf("parse %s JSON output: %w", agentName, err)
	}
	return raw, nil
}

// runWithPrompt invokes Claude CLI with an already-rendered prompt string.
func (r *AgentRunner) runWithPrompt(ctx context.Context, agentName, workDir, rendered string) (string, error) {
	timeout := r.timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	log.WithFields(Fields{
		"agent":   agentName,
		"workdir": workDir,
	}).Info("running agent")

	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--dangerously-skip-permissions",
		"-p", rendered,
		"--output-format", "text")
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude for %s: %w", agentName, err)
	}

	var out strings.Builder
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		out.WriteString(line + "\n")
	}

	if err := cmd.Wait(); err != nil {
		return out.String(), fmt.Errorf("agent %s exited with error: %w\noutput: %s", agentName, err, truncate(out.String(), 500))
	}

	return out.String(), nil
}

// extractJSON finds and unmarshals a JSON object from text using multiple strategies:
// 1. JSON inside markdown code fences (```json ... ```)
// 2. The last complete, valid JSON object in the text
// 3. The first complete JSON object (original behavior)
// 4. json-repair: attempt to fix malformed JSON from LLM output
func extractJSON(text string, dest any) error {
	// Strategy 1: markdown code fence
	if s := extractFromCodeFence(text); s != "" {
		if err := json.Unmarshal([]byte(s), dest); err == nil {
			return nil
		}
	}

	// Strategy 2: last valid JSON object
	if s := extractLastJSONObject(text); s != "" {
		if err := json.Unmarshal([]byte(s), dest); err == nil {
			return nil
		}
	}

	// Strategy 3: first JSON object (original behavior)
	s := extractFirstJSONObject(text)
	if s != "" {
		if err := json.Unmarshal([]byte(s), dest); err == nil {
			return nil
		}
	}

	// Strategy 4: json-repair on the entire text (handles truncated/malformed JSON from LLMs)
	if repaired, err := jsonrepair.RepairJSON(text); err == nil && repaired != "" {
		// Extract object from repaired text (repair may return valid JSON wrapped in text)
		if json.Unmarshal([]byte(repaired), dest) == nil {
			return nil
		}
		// Try extracting object from repaired output
		if obj := extractFirstJSONObject(repaired); obj != "" {
			if json.Unmarshal([]byte(obj), dest) == nil {
				return nil
			}
		}
	}

	// Strategy 4b: repair just the extracted fragment
	if s != "" {
		if repaired, err := jsonrepair.RepairJSON(s); err == nil {
			if json.Unmarshal([]byte(repaired), dest) == nil {
				return nil
			}
		}
	}

	if s == "" {
		return fmt.Errorf("no JSON object found in output")
	}
	return fmt.Errorf("JSON found but could not be parsed or repaired")
}

// extractFromCodeFence extracts content from ```json ... ``` or ``` ... ``` fences.
func extractFromCodeFence(text string) string {
	for _, marker := range []string{"```json\n", "```JSON\n", "```\n{"} {
		start := strings.Index(text, marker)
		if start < 0 {
			continue
		}
		// For "```\n{" we don't consume the "{" as part of the marker
		offset := len(marker)
		if marker == "```\n{" {
			offset = 4 // just "```\n", leave "{" in content
		}
		content := text[start+offset:]
		end := strings.Index(content, "```")
		if end >= 0 {
			return strings.TrimSpace(content[:end])
		}
	}
	return ""
}

// extractLastJSONObject scans the text and returns the last syntactically valid JSON object.
func extractLastJSONObject(text string) string {
	last := ""
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		s := extractObjectAt(text, i)
		if s == "" {
			continue
		}
		var raw json.RawMessage
		if json.Unmarshal([]byte(s), &raw) == nil {
			last = s
			i += len(s) - 1
		}
	}
	return last
}

// extractFirstJSONObject returns the first syntactically complete JSON object in text.
func extractFirstJSONObject(text string) string {
	start := strings.Index(text, "{")
	if start < 0 {
		return ""
	}
	return extractObjectAt(text, start)
}

// extractObjectAt returns the JSON object starting at position start, or "" if incomplete.
// It respects quoted strings so braces inside strings are not counted.
func extractObjectAt(text string, start int) string {
	if start >= len(text) || text[start] != '{' {
		return ""
	}
	depth := 0
	inString := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			if c == '\\' {
				i++ // skip escaped character
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
