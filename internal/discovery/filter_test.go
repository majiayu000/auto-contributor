package discovery

import (
	"context"
	"errors"
	"testing"
)

type fakePR struct {
	hasPR bool
	err   error
	calls int
}

func (f *fakePR) HasExistingPR(_ context.Context, _ string, _ int) (bool, error) {
	f.calls++
	return f.hasPR, f.err
}

type fakeBlacklist struct {
	black bool
	err   error
}

func (f *fakeBlacklist) IsBlacklisted(_ string) (bool, error) {
	return f.black, f.err
}

func candidate(repo string, num int, score float64) DiscoveredIssue {
	return DiscoveredIssue{
		Repo:             repo,
		IssueNumber:      num,
		Title:            "T",
		SuitabilityScore: score,
		RepoContext:      RepoContext{Stars: 5000, Language: "Go"},
	}
}

func TestFilter_AcceptsClean(t *testing.T) {
	t.Parallel()

	pr := &fakePR{}
	bl := &fakeBlacklist{}
	f := NewFilter(FilterConfig{MinSuitability: 0.4, MinStars: 1000}, pr, bl)

	results := f.Apply(context.Background(), []DiscoveredIssue{candidate("owner/repo", 1, 0.9)})
	if len(results) != 1 {
		t.Fatalf("len(results)=%d want 1", len(results))
	}
	if !results[0].Accepted() {
		t.Fatalf("expected accepted, got reason=%q detail=%q", results[0].Reason, results[0].Detail)
	}
	if pr.calls != 1 {
		t.Fatalf("expected one PR check, got %d", pr.calls)
	}
}

func TestFilter_RejectsLowSuitability(t *testing.T) {
	t.Parallel()

	pr := &fakePR{}
	f := NewFilter(FilterConfig{MinSuitability: 0.4}, pr, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{candidate("owner/repo", 1, 0.39)})
	if results[0].Reason != ReasonLowSuitability {
		t.Fatalf("got reason=%q want %q", results[0].Reason, ReasonLowSuitability)
	}
	// Confirm we short-circuit before the network call.
	if pr.calls != 0 {
		t.Fatalf("PR checker called %d times — should short-circuit on cheap gate", pr.calls)
	}
}

func TestFilter_RejectsLowStars(t *testing.T) {
	t.Parallel()

	c := candidate("owner/repo", 1, 0.9)
	c.RepoContext.Stars = 50
	f := NewFilter(FilterConfig{MinStars: 1000}, nil, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{c})
	if results[0].Reason != ReasonLowStars {
		t.Fatalf("got reason=%q want %q", results[0].Reason, ReasonLowStars)
	}
}

func TestFilter_StarsZeroDoesNotGate(t *testing.T) {
	// Per the contract: Stars==0 means "unknown", fail-open.
	t.Parallel()

	c := candidate("owner/repo", 1, 0.9)
	c.RepoContext.Stars = 0
	f := NewFilter(FilterConfig{MinStars: 1000}, nil, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{c})
	if !results[0].Accepted() {
		t.Fatalf("expected accept on unknown stars, got %q", results[0].Reason)
	}
}

func TestFilter_RejectsExcludedRepo(t *testing.T) {
	t.Parallel()

	f := NewFilter(FilterConfig{ExcludeRepos: []string{"Owner/Repo"}}, nil, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{candidate("owner/repo", 1, 0.9)})
	if results[0].Reason != ReasonExcludedRepo {
		t.Fatalf("got reason=%q want %q", results[0].Reason, ReasonExcludedRepo)
	}
}

func TestFilter_LanguageMismatch(t *testing.T) {
	t.Parallel()

	c := candidate("owner/repo", 1, 0.9)
	c.RepoContext.Language = "Cobol"
	f := NewFilter(FilterConfig{Languages: []string{"go", "python"}}, nil, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{c})
	if results[0].Reason != ReasonLanguageMismatch {
		t.Fatalf("got reason=%q want %q", results[0].Reason, ReasonLanguageMismatch)
	}
}

func TestFilter_LanguageEmptyPasses(t *testing.T) {
	// Unknown language must fail-open per the contract.
	t.Parallel()

	c := candidate("owner/repo", 1, 0.9)
	c.RepoContext.Language = ""
	f := NewFilter(FilterConfig{Languages: []string{"go"}}, nil, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{c})
	if !results[0].Accepted() {
		t.Fatalf("expected accept on empty language, got %q", results[0].Reason)
	}
}

func TestFilter_BlacklistRejects(t *testing.T) {
	t.Parallel()

	bl := &fakeBlacklist{black: true}
	f := NewFilter(FilterConfig{}, &fakePR{}, bl)
	results := f.Apply(context.Background(), []DiscoveredIssue{candidate("owner/repo", 1, 0.9)})
	if results[0].Reason != ReasonBlacklisted {
		t.Fatalf("got reason=%q want %q", results[0].Reason, ReasonBlacklisted)
	}
}

func TestFilter_BlacklistCheckErrorIsDistinct(t *testing.T) {
	// A degraded blacklist DB must not be misreported as "intentionally
	// banned" — operators tuning the blacklist need a separate signal.
	t.Parallel()

	bl := &fakeBlacklist{err: errors.New("db connection refused")}
	pr := &fakePR{}
	f := NewFilter(FilterConfig{}, pr, bl)
	results := f.Apply(context.Background(), []DiscoveredIssue{candidate("owner/repo", 1, 0.9)})
	if results[0].Reason != ReasonBlacklistCheckFailed {
		t.Fatalf("got reason=%q want %q", results[0].Reason, ReasonBlacklistCheckFailed)
	}
	if results[0].Detail == "" {
		t.Fatalf("expected the underlying DB error to surface in Detail")
	}
	// We must also fail closed: no PR check should run when the upstream
	// blacklist gate is degraded, otherwise a banned repo could leak
	// through if the blacklist DB started returning errors.
	if pr.calls != 0 {
		t.Fatalf("PR checker called %d times — must short-circuit on blacklist failure", pr.calls)
	}
}

func TestFilter_PRExistsRejects(t *testing.T) {
	t.Parallel()

	pr := &fakePR{hasPR: true}
	f := NewFilter(FilterConfig{}, pr, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{candidate("owner/repo", 1, 0.9)})
	if results[0].Reason != ReasonExistingPR {
		t.Fatalf("got reason=%q want %q", results[0].Reason, ReasonExistingPR)
	}
}

func TestFilter_PRCheckErrorFailsClosed(t *testing.T) {
	t.Parallel()

	pr := &fakePR{err: errors.New("api 502")}
	f := NewFilter(FilterConfig{}, pr, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{candidate("owner/repo", 1, 0.9)})
	if results[0].Reason != ReasonPRCheckFailed {
		t.Fatalf("got reason=%q want %q", results[0].Reason, ReasonPRCheckFailed)
	}
	if results[0].Detail == "" {
		t.Fatalf("expected detail to surface the underlying error")
	}
}

func TestFilter_RejectsInvalidCandidate(t *testing.T) {
	t.Parallel()

	f := NewFilter(FilterConfig{}, nil, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{
		{Repo: "", IssueNumber: 0, SuitabilityScore: 0.9},
		{Repo: "owner/repo", IssueNumber: 0, SuitabilityScore: 0.9},
	})
	for i, r := range results {
		if r.Reason != ReasonInvalid {
			t.Fatalf("results[%d] reason=%q want %q", i, r.Reason, ReasonInvalid)
		}
	}
}

func TestFilter_NilCheckersAreNoops(t *testing.T) {
	t.Parallel()

	f := NewFilter(FilterConfig{}, nil, nil)
	results := f.Apply(context.Background(), []DiscoveredIssue{candidate("owner/repo", 1, 0.5)})
	if !results[0].Accepted() {
		t.Fatalf("expected accept with nil checkers, got %q", results[0].Reason)
	}
}

func TestFilter_GateOrderIsCheapBeforeExpensive(t *testing.T) {
	// The cheap gates (suitability/stars/exclude/language) must run
	// BEFORE the network call so a degraded run is still fast.
	t.Parallel()

	pr := &fakePR{}
	bl := &fakeBlacklist{}
	f := NewFilter(FilterConfig{
		MinSuitability: 0.5,
		MinStars:       10000,
		ExcludeRepos:   []string{"banned/repo"},
		Languages:      []string{"go"},
	}, pr, bl)

	cases := []DiscoveredIssue{
		// low suitability -> short-circuit
		func() DiscoveredIssue { c := candidate("owner/a", 1, 0.1); return c }(),
		// low stars -> short-circuit
		func() DiscoveredIssue { c := candidate("owner/b", 2, 0.9); c.RepoContext.Stars = 1; return c }(),
		// excluded -> short-circuit
		candidate("banned/repo", 3, 0.9),
		// language mismatch -> short-circuit
		func() DiscoveredIssue { c := candidate("owner/c", 4, 0.9); c.RepoContext.Language = "rust"; return c }(),
	}
	f.Apply(context.Background(), cases)
	if pr.calls != 0 {
		t.Fatalf("PR checker called %d times — every input should short-circuit on a cheap gate", pr.calls)
	}
}

func TestSummarize(t *testing.T) {
	t.Parallel()

	results := []FilterResult{
		{Reason: ReasonAccepted},
		{Reason: ReasonAccepted},
		{Reason: ReasonLowSuitability},
		{Reason: ReasonExistingPR},
		{Reason: ReasonExistingPR},
	}
	stats := Summarize(results)
	if stats.Accepted != 2 {
		t.Fatalf("Accepted=%d want 2", stats.Accepted)
	}
	if stats.Skipped != 3 {
		t.Fatalf("Skipped=%d want 3", stats.Skipped)
	}
	if stats.Counts[ReasonExistingPR] != 2 {
		t.Fatalf("ReasonExistingPR=%d want 2", stats.Counts[ReasonExistingPR])
	}
	if stats.Counts[ReasonLowSuitability] != 1 {
		t.Fatalf("ReasonLowSuitability=%d want 1", stats.Counts[ReasonLowSuitability])
	}
	if _, present := stats.Counts[ReasonAccepted]; present {
		t.Fatalf("ReasonAccepted must not appear in Counts")
	}

	// Order is deterministic.
	keys := stats.SkipReasons()
	if len(keys) != 2 {
		t.Fatalf("len(keys)=%d want 2", len(keys))
	}
	if string(keys[0]) > string(keys[1]) {
		t.Fatalf("SkipReasons must be sorted ascending; got %v", keys)
	}
}

func TestSuitabilityToDifficulty_KnownPoints(t *testing.T) {
	// This test pins the canonical convention defined by
	// internal/github.estimateDifficulty: higher difficulty = harder.
	// Two cmd/auto-contributor sites previously disagreed; this is the
	// regression guard.
	t.Parallel()

	cases := []struct {
		in, want float64
	}{
		{0.0, 1.0}, // unsuitable -> hardest
		{1.0, 0.0}, // perfectly suitable -> easiest
		{0.5, 0.5},
		// Out-of-range inputs are clamped.
		{-0.5, 1.0},
		{1.5, 0.0},
	}
	for _, tc := range cases {
		got := SuitabilityToDifficulty(tc.in)
		if got != tc.want {
			t.Errorf("SuitabilityToDifficulty(%v)=%v want %v", tc.in, got, tc.want)
		}
	}
}
