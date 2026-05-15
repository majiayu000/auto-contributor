package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// SkipReason names a single rejection cause for a discovered issue.
// Reasons are stable strings so they can be used as log fields, metric
// labels, and database keys without translation.
type SkipReason string

const (
	// ReasonAccepted is the sentinel value used in FilterResult to mark
	// issues that passed every gate.
	ReasonAccepted SkipReason = ""

	// ReasonLowSuitability is emitted when SuitabilityScore is below
	// FilterConfig.MinSuitability.
	ReasonLowSuitability SkipReason = "low_suitability"

	// ReasonLowStars is emitted when RepoContext.Stars is below
	// FilterConfig.MinStars.
	ReasonLowStars SkipReason = "low_stars"

	// ReasonLanguageMismatch is emitted when FilterConfig.Languages is
	// non-empty and the issue's repo language is not in the allowlist.
	// Per-issue language is best-effort; if the discoverer did not record
	// a language, this gate is a no-op for that issue (fail-open).
	ReasonLanguageMismatch SkipReason = "language_mismatch"

	// ReasonExcludedRepo is emitted when the issue's repo is on the
	// configured exclude list.
	ReasonExcludedRepo SkipReason = "excluded_repo"

	// ReasonBlacklisted is emitted when database.IsBlacklisted returns true.
	ReasonBlacklisted SkipReason = "blacklisted"

	// ReasonBlacklistCheckFailed is emitted when the blacklist lookup
	// itself errored. Distinct from ReasonBlacklisted so dashboards can
	// tell "intentionally banned" apart from "DB degraded" — a sustained
	// rise in this counter is an SRE signal, not a content signal.
	// We fail closed (skip the issue) so that a degraded blacklist DB
	// can never accidentally surface a banned repo.
	ReasonBlacklistCheckFailed SkipReason = "blacklist_check_failed"

	// ReasonExistingPR is emitted when ghClient.HasExistingPR returns true.
	ReasonExistingPR SkipReason = "existing_pr"

	// ReasonPRCheckFailed is emitted when the PR check itself fails. We
	// fail closed (skip the issue) to avoid double-PRs when the upstream
	// API is degraded — matches the existing behavior in cmd_loop.go.
	ReasonPRCheckFailed SkipReason = "pr_check_failed"

	// ReasonInvalid is emitted when the candidate is missing required
	// fields (e.g. empty repo or zero issue number).
	ReasonInvalid SkipReason = "invalid"
)

// FilterConfig groups every threshold the filter consults. Zero values
// disable the corresponding gate, except MinSuitability which defaults
// to 0 (i.e. accept any score) and MinStars which defaults to 0.
type FilterConfig struct {
	MinSuitability float64
	MinStars       int
	// Languages is the language allowlist (lowercased on Apply). Empty
	// means "do not gate on language".
	Languages    []string
	ExcludeRepos []string
}

// PRChecker is the minimal interface the filter needs from the GitHub
// client. Defining it locally keeps internal/discovery free of a hard
// dependency on internal/github and makes the filter trivially mockable.
type PRChecker interface {
	HasExistingPR(ctx context.Context, repoFullName string, issueNum int) (bool, error)
}

// BlacklistChecker abstracts the database blacklist lookup. As above,
// we depend on the contract, not on internal/db.
type BlacklistChecker interface {
	IsBlacklisted(repo string) (bool, error)
}

// noopPRChecker is used when the caller does not provide a checker;
// every issue passes the existing-PR gate.
type noopPRChecker struct{}

func (noopPRChecker) HasExistingPR(_ context.Context, _ string, _ int) (bool, error) {
	return false, nil
}

// noopBlacklistChecker is used when no blacklist source is configured.
type noopBlacklistChecker struct{}

func (noopBlacklistChecker) IsBlacklisted(_ string) (bool, error) { return false, nil }

// Filter runs a deterministic chain of gates against DiscoveredIssue
// candidates and emits a structured FilterResult per input. Cheap
// in-memory gates run first so we never call the GitHub API for a
// candidate that is going to be rejected anyway.
type Filter struct {
	cfg          FilterConfig
	pr           PRChecker
	blacklist    BlacklistChecker
	excludeIndex map[string]struct{}
	langIndex    map[string]struct{}
}

// NewFilter constructs a Filter. Pass nil for either checker to disable
// the matching gate.
func NewFilter(cfg FilterConfig, pr PRChecker, blacklist BlacklistChecker) *Filter {
	if pr == nil {
		pr = noopPRChecker{}
	}
	if blacklist == nil {
		blacklist = noopBlacklistChecker{}
	}

	excludeIndex := make(map[string]struct{}, len(cfg.ExcludeRepos))
	for _, r := range cfg.ExcludeRepos {
		key := strings.ToLower(strings.TrimSpace(r))
		if key == "" {
			continue
		}
		excludeIndex[key] = struct{}{}
	}

	langIndex := make(map[string]struct{}, len(cfg.Languages))
	for _, l := range cfg.Languages {
		key := strings.ToLower(strings.TrimSpace(l))
		if key == "" {
			continue
		}
		langIndex[key] = struct{}{}
	}

	return &Filter{
		cfg:          cfg,
		pr:           pr,
		blacklist:    blacklist,
		excludeIndex: excludeIndex,
		langIndex:    langIndex,
	}
}

// FilterResult records the outcome of running one issue through the
// filter chain.
type FilterResult struct {
	Issue  DiscoveredIssue
	Reason SkipReason
	// Detail carries supplementary context that is useful for log lines
	// (e.g. "score=0.32 < 0.40" or the underlying API error message).
	// It is never the source of truth — Reason is.
	Detail string
}

// Accepted reports whether this result passed every gate.
func (r FilterResult) Accepted() bool { return r.Reason == ReasonAccepted }

// Apply runs the filter chain over the candidates and returns one
// FilterResult per input. Order is preserved so callers can correlate
// results with the original slice.
//
// The function never returns an error: per-issue failures are encoded
// in FilterResult.Reason. This is deliberate — discovery already runs
// in a long-lived loop and we don't want one degraded check to abort
// the whole cycle.
func (f *Filter) Apply(ctx context.Context, issues []DiscoveredIssue) []FilterResult {
	results := make([]FilterResult, 0, len(issues))
	for _, issue := range issues {
		results = append(results, f.applyOne(ctx, issue))
	}
	return results
}

func (f *Filter) applyOne(ctx context.Context, issue DiscoveredIssue) FilterResult {
	repoName := strings.TrimSpace(issue.Repo)
	repoKey := strings.ToLower(repoName)
	if repoName == "" || issue.IssueNumber <= 0 {
		return FilterResult{Issue: issue, Reason: ReasonInvalid, Detail: "missing repo or issue number"}
	}

	// Cheap, deterministic gates first.
	if f.cfg.MinSuitability > 0 && issue.SuitabilityScore < f.cfg.MinSuitability {
		return FilterResult{
			Issue:  issue,
			Reason: ReasonLowSuitability,
			Detail: fmt.Sprintf("score=%.2f<%.2f", issue.SuitabilityScore, f.cfg.MinSuitability),
		}
	}

	if f.cfg.MinStars > 0 && issue.RepoContext.Stars > 0 && issue.RepoContext.Stars < f.cfg.MinStars {
		return FilterResult{
			Issue:  issue,
			Reason: ReasonLowStars,
			Detail: fmt.Sprintf("stars=%d<%d", issue.RepoContext.Stars, f.cfg.MinStars),
		}
	}

	if _, excluded := f.excludeIndex[repoKey]; excluded {
		return FilterResult{Issue: issue, Reason: ReasonExcludedRepo, Detail: repoKey}
	}

	if len(f.langIndex) > 0 && issue.RepoContext.Language != "" {
		if _, ok := f.langIndex[strings.ToLower(issue.RepoContext.Language)]; !ok {
			return FilterResult{
				Issue:  issue,
				Reason: ReasonLanguageMismatch,
				Detail: issue.RepoContext.Language,
			}
		}
	}

	// Network/database gates last. Both fail closed: an error here means
	// we don't know the answer, and the existing pipeline policy is to
	// skip rather than risk a duplicate PR.
	if blacklisted, err := f.blacklist.IsBlacklisted(repoName); err != nil {
		return FilterResult{Issue: issue, Reason: ReasonBlacklistCheckFailed, Detail: err.Error()}
	} else if blacklisted {
		return FilterResult{Issue: issue, Reason: ReasonBlacklisted, Detail: repoName}
	}

	hasPR, err := f.pr.HasExistingPR(ctx, issue.Repo, issue.IssueNumber)
	if err != nil {
		return FilterResult{Issue: issue, Reason: ReasonPRCheckFailed, Detail: err.Error()}
	}
	if hasPR {
		return FilterResult{Issue: issue, Reason: ReasonExistingPR}
	}

	return FilterResult{Issue: issue, Reason: ReasonAccepted}
}

// FilterStats aggregates a slice of FilterResult into Accepted vs
// per-reason skip counts. The counts map never contains ReasonAccepted.
type FilterStats struct {
	Accepted int
	Skipped  int
	Counts   map[SkipReason]int
}

// Summarize aggregates FilterResults into a FilterStats. The order of
// keys returned by SkipReasons() is deterministic (lexicographic) so
// log lines and tests are stable.
func Summarize(results []FilterResult) FilterStats {
	stats := FilterStats{Counts: make(map[SkipReason]int)}
	for _, r := range results {
		if r.Accepted() {
			stats.Accepted++
			continue
		}
		stats.Skipped++
		stats.Counts[r.Reason]++
	}
	return stats
}

// SkipReasons returns the skip-reason keys in lexicographic order.
func (s FilterStats) SkipReasons() []SkipReason {
	keys := make([]SkipReason, 0, len(s.Counts))
	for k := range s.Counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return string(keys[i]) < string(keys[j]) })
	return keys
}

// SuitabilityToDifficulty maps a suitability score (higher = better
// candidate) onto a difficulty score (higher = harder issue). The
// canonical convention used by internal/github.estimateDifficulty is
// "higher = harder". Two call sites in cmd/auto-contributor disagreed
// before this helper existed — see the package tests for the regression.
func SuitabilityToDifficulty(suitability float64) float64 {
	d := 1.0 - suitability
	if d < 0 {
		return 0
	}
	if d > 1 {
		return 1
	}
	return d
}
