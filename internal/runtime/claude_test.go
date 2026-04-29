package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWrapClaudeCommandError_ClassifiesKnownFailures(t *testing.T) {
	t.Parallel()

	baseErr := errors.New("exit status 1")

	tests := []struct {
		name         string
		stderr       string
		wantQuota    bool
		wantThrottle bool
		wantText     string
	}{
		{
			name:      "hard quota",
			stderr:    "Error: quota exceeded for this workspace",
			wantQuota: true,
		},
		{
			name:      "billing payment required",
			stderr:    "Billing issue: payment required before continuing",
			wantQuota: true,
		},
		{
			name:         "429",
			stderr:       "Request failed with status 429",
			wantThrottle: true,
		},
		{
			name:         "rate limit",
			stderr:       "Rate limit reached, please retry later",
			wantThrottle: true,
		},
		{
			name:         "overloaded",
			stderr:       "Claude is overloaded, try again later",
			wantThrottle: true,
		},
		{
			name:     "generic cli failure",
			stderr:   "permission denied",
			wantText: "permission denied",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := wrapClaudeCommandError(context.Background(), baseErr, tt.stderr)
			if got := IsClaudeQuotaBillingError(err); got != tt.wantQuota {
				t.Fatalf("IsClaudeQuotaBillingError() = %v, want %v (err=%v)", got, tt.wantQuota, err)
			}
			if got := IsClaudeThrottleError(err); got != tt.wantThrottle {
				t.Fatalf("IsClaudeThrottleError() = %v, want %v (err=%v)", got, tt.wantThrottle, err)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantText)
			}
		})
	}
}

func TestWrapClaudeEmptyOutputError_ClassifiesKnownFailures(t *testing.T) {
	t.Parallel()

	err := wrapClaudeEmptyOutputError("usage limit reached for your account")
	if !IsClaudeQuotaBillingError(err) {
		t.Fatalf("expected quota/billing classification, got %v", err)
	}

	err = wrapClaudeEmptyOutputError("try again later due to rate limit")
	if !IsClaudeThrottleError(err) {
		t.Fatalf("expected throttle classification, got %v", err)
	}
}

func TestWrapClaudeCommandError_PreservesContextTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := wrapClaudeCommandError(ctx, errors.New("signal: killed"), "rate limit reached")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation passthrough, got %v", err)
	}
	if IsClaudeThrottleError(err) || IsClaudeQuotaBillingError(err) {
		t.Fatalf("expected timeout/cancel error to bypass classification, got %v", err)
	}
}
