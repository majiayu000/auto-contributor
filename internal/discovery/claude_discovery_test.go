package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimepkg "github.com/majiayu000/auto-contributor/internal/runtime"
)

type stubRuntime struct {
	outputs []stubRuntimeResult
	calls   int
}

type stubRuntimeResult struct {
	output string
	err    error
}

func (r *stubRuntime) Name() string {
	return "stub"
}

func (r *stubRuntime) Execute(ctx context.Context, workDir string, prompt string) (string, error) {
	return "", errors.New("not implemented")
}

func (r *stubRuntime) ExecuteStdin(ctx context.Context, prompt string) (string, error) {
	if r.calls >= len(r.outputs) {
		return "", errors.New("unexpected runtime call")
	}
	result := r.outputs[r.calls]
	r.calls++
	return result.output, result.err
}

func TestClaudeDiscovererRunClaude_TransientErrorThenSuccess(t *testing.T) {
	t.Parallel()

	rt := &stubRuntime{
		outputs: []stubRuntimeResult{
			{err: throttleError("rate limit reached")},
			{output: `{"issues":[],"metadata":{"selected":0}}`},
		},
	}

	discoverer := NewClaudeDiscoverer(rt, time.Minute, 1)
	var slept []time.Duration
	discoverer.sleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}

	output, err := discoverer.runClaude(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("runClaude() error = %v", err)
	}
	if output == "" {
		t.Fatal("runClaude() returned empty output")
	}
	if rt.calls != 2 {
		t.Fatalf("runtime calls = %d, want 2", rt.calls)
	}
	if len(slept) != 1 || slept[0] != 5*time.Second {
		t.Fatalf("backoff = %v, want [5s]", slept)
	}
}

func TestClaudeDiscovererRunClaude_TransientErrorExhaustsRetries(t *testing.T) {
	t.Parallel()

	rt := &stubRuntime{
		outputs: []stubRuntimeResult{
			{err: throttleError("429 too many requests")},
			{err: throttleError("429 too many requests")},
			{err: throttleError("429 too many requests")},
		},
	}

	discoverer := NewClaudeDiscoverer(rt, time.Minute, 2)
	var slept []time.Duration
	discoverer.sleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}

	_, err := discoverer.runClaude(context.Background(), "prompt")
	if err == nil {
		t.Fatal("runClaude() error = nil, want retry exhaustion")
	}
	if !runtimepkg.IsClaudeThrottleError(err) {
		t.Fatalf("expected throttle error, got %v", err)
	}
	if rt.calls != 3 {
		t.Fatalf("runtime calls = %d, want 3", rt.calls)
	}
	if len(slept) != 2 || slept[0] != 5*time.Second || slept[1] != 10*time.Second {
		t.Fatalf("backoff = %v, want [5s 10s]", slept)
	}
}

func TestClaudeDiscovererRunClaude_QuotaErrorDoesNotRetry(t *testing.T) {
	t.Parallel()

	rt := &stubRuntime{
		outputs: []stubRuntimeResult{
			{err: quotaError("billing issue: payment required")},
		},
	}

	discoverer := NewClaudeDiscoverer(rt, time.Minute, 0)
	sleepCalled := false
	discoverer.sleep = func(ctx context.Context, delay time.Duration) error {
		sleepCalled = true
		return nil
	}

	_, err := discoverer.runClaude(context.Background(), "prompt")
	if err == nil {
		t.Fatal("runClaude() error = nil, want quota failure")
	}
	if !runtimepkg.IsClaudeQuotaBillingError(err) {
		t.Fatalf("expected quota error, got %v", err)
	}
	if rt.calls != 1 {
		t.Fatalf("runtime calls = %d, want 1", rt.calls)
	}
	if sleepCalled {
		t.Fatal("sleep should not be called for quota failures")
	}
}

func TestClaudeDiscovererRunClaude_GenericErrorDoesNotRetry(t *testing.T) {
	t.Parallel()

	rt := &stubRuntime{
		outputs: []stubRuntimeResult{
			{err: errors.New("boom")},
		},
	}

	discoverer := NewClaudeDiscoverer(rt, time.Minute, 3)
	sleepCalled := false
	discoverer.sleep = func(ctx context.Context, delay time.Duration) error {
		sleepCalled = true
		return nil
	}

	_, err := discoverer.runClaude(context.Background(), "prompt")
	if err == nil {
		t.Fatal("runClaude() error = nil, want generic failure")
	}
	if runtimepkg.IsClaudeThrottleError(err) || runtimepkg.IsClaudeQuotaBillingError(err) {
		t.Fatalf("generic error was misclassified: %v", err)
	}
	if rt.calls != 1 {
		t.Fatalf("runtime calls = %d, want 1", rt.calls)
	}
	if sleepCalled {
		t.Fatal("sleep should not be called for generic failures")
	}
}

func TestClaudeDiscovererRunClaude_ContextCanceledDuringBackoff(t *testing.T) {
	t.Parallel()

	rt := &stubRuntime{
		outputs: []stubRuntimeResult{
			{err: throttleError("overloaded, try again later")},
		},
	}

	discoverer := NewClaudeDiscoverer(rt, time.Minute, 1)
	ctx, cancel := context.WithCancel(context.Background())
	discoverer.sleep = func(ctx context.Context, delay time.Duration) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}

	_, err := discoverer.runClaude(ctx, "prompt")
	if err == nil {
		t.Fatal("runClaude() error = nil, want canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if rt.calls != 1 {
		t.Fatalf("runtime calls = %d, want 1", rt.calls)
	}
}

func quotaError(stderr string) error {
	return &runtimepkg.ClaudeExecutionError{
		Class:  runtimepkg.ClaudeErrorClassQuotaBilling,
		Stderr: stderr,
		Err:    errors.New("exit status 1"),
	}
}

func throttleError(stderr string) error {
	return &runtimepkg.ClaudeExecutionError{
		Class:  runtimepkg.ClaudeErrorClassThrottle,
		Stderr: stderr,
		Err:    errors.New("exit status 1"),
	}
}
