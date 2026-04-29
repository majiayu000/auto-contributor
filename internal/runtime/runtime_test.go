package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRecorderCLI(t *testing.T) (string, string, string) {
	t.Helper()

	dir := t.TempDir()
	cliPath := filepath.Join(dir, "record-cli.sh")
	argsFile := filepath.Join(dir, "args.txt")
	stdinFile := filepath.Join(dir, "stdin.txt")

	script := `#!/bin/sh
printf '%s\n' "$@" > "$ARGS_FILE"
cat > "$STDIN_FILE"
printf 'ok'
`
	if err := os.WriteFile(cliPath, []byte(script), 0755); err != nil {
		t.Fatalf("write recorder cli: %v", err)
	}

	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("STDIN_FILE", stdinFile)
	return cliPath, argsFile, stdinFile
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestClaudeExecute_UntrustedOmitsDangerousFlag(t *testing.T) {
	cliPath, argsFile, _ := writeRecorderCLI(t)
	rt := NewClaude(cliPath)

	if _, err := rt.Execute(context.Background(), t.TempDir(), "prompt", ExecutionPolicyUntrusted); err != nil {
		t.Fatalf("execute: %v", err)
	}

	args := readFile(t, argsFile)
	if strings.Contains(args, "--dangerously-skip-permissions") {
		t.Fatalf("unexpected dangerous flag in args: %s", args)
	}
}

func TestClaudeExecute_TrustedAddsDangerousFlag(t *testing.T) {
	cliPath, argsFile, _ := writeRecorderCLI(t)
	rt := NewClaude(cliPath)

	if _, err := rt.Execute(context.Background(), t.TempDir(), "prompt", ExecutionPolicyTrusted); err != nil {
		t.Fatalf("execute: %v", err)
	}

	args := readFile(t, argsFile)
	if !strings.Contains(args, "--dangerously-skip-permissions") {
		t.Fatalf("expected dangerous flag in args: %s", args)
	}
}

func TestClaudeExecuteStdin_UsesRestrictedPolicyAndStdin(t *testing.T) {
	cliPath, argsFile, stdinFile := writeRecorderCLI(t)
	rt := NewClaude(cliPath)

	const prompt = "issue data"
	if _, err := rt.ExecuteStdin(context.Background(), prompt, ExecutionPolicyUntrusted); err != nil {
		t.Fatalf("execute stdin: %v", err)
	}

	args := readFile(t, argsFile)
	if strings.Contains(args, "--dangerously-skip-permissions") {
		t.Fatalf("unexpected dangerous flag in args: %s", args)
	}
	if got := readFile(t, stdinFile); got != prompt {
		t.Fatalf("stdin = %q, want %q", got, prompt)
	}
}

func TestCodexExecute_UntrustedOmitsDangerousFlag(t *testing.T) {
	cliPath, argsFile, _ := writeRecorderCLI(t)
	rt := NewCodex(cliPath)

	if _, err := rt.Execute(context.Background(), t.TempDir(), "prompt", ExecutionPolicyUntrusted); err != nil {
		t.Fatalf("execute: %v", err)
	}

	args := readFile(t, argsFile)
	if strings.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("unexpected dangerous flag in args: %s", args)
	}
}

func TestCodexExecute_TrustedAddsDangerousFlag(t *testing.T) {
	cliPath, argsFile, _ := writeRecorderCLI(t)
	rt := NewCodex(cliPath)

	if _, err := rt.Execute(context.Background(), t.TempDir(), "prompt", ExecutionPolicyTrusted); err != nil {
		t.Fatalf("execute: %v", err)
	}

	args := readFile(t, argsFile)
	if !strings.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("expected dangerous flag in args: %s", args)
	}
}

func TestCodexExecuteStdin_UsesRequestedPolicy(t *testing.T) {
	cliPath, argsFile, _ := writeRecorderCLI(t)
	rt := NewCodex(cliPath)

	if _, err := rt.ExecuteStdin(context.Background(), "prompt", ExecutionPolicyUntrusted); err != nil {
		t.Fatalf("execute stdin: %v", err)
	}

	args := readFile(t, argsFile)
	if strings.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("unexpected dangerous flag in args: %s", args)
	}
	if !strings.Contains(args, "prompt") {
		t.Fatalf("expected prompt argument in args: %s", args)
	}
}
