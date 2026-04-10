package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// sanitizeForPrompt strips characters and patterns from user-controlled text that could
// be used to inject instructions into an LLM prompt. It collapses newlines to spaces
// and removes sequences that look like markdown headings or role markers.
func sanitizeForPrompt(s string) string {
	// Replace newlines/tabs with a space so multi-line content cannot introduce
	// new prompt sections.
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)

	// Remove markdown heading markers (###, ##, #) that could introduce fake sections.
	for strings.Contains(s, "##") || strings.HasPrefix(strings.TrimSpace(s), "#") {
		s = strings.ReplaceAll(s, "##", "")
		if strings.HasPrefix(strings.TrimSpace(s), "#") {
			s = strings.TrimLeft(s, "#")
		}
	}

	// Remove common role-marker prefixes used in prompt injection.
	for _, prefix := range []string{"SYSTEM:", "ASSISTANT:", "USER:", "HUMAN:", "AI:", "<|im_start|>", "<|im_end|>"} {
		s = strings.ReplaceAll(s, prefix, "")
	}

	// Collapse multiple spaces.
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}

	return strings.TrimSpace(s)
}

// stopWords is a minimal set of common English words to exclude from keyword extraction.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
	"in": true, "on": true, "at": true, "to": true, "for": true, "of": true,
	"with": true, "by": true, "from": true, "is": true, "are": true, "was": true,
	"be": true, "been": true, "it": true, "its": true, "this": true, "that": true,
	"as": true, "not": true, "no": true, "can": true, "we": true, "i": true,
	"if": true, "so": true, "do": true, "get": true, "when": true, "how": true,
	"use": true, "used": true, "using": true, "into": true, "have": true,
	"has": true, "had": true, "will": true, "would": true, "should": true,
	"could": true, "may": true, "might": true, "which": true, "what": true,
	"also": true, "more": true, "than": true, "then": true, "up": true,
	"out": true, "after": true, "before": true, "all": true, "each": true,
	"just": true, "any": true, "some": true, "about": true, "like": true,
	"see": true, "new": true, "go": true, "make": true, "set": true,
}

// extractKeywords tokenizes title + body into a deduplicated sorted keyword list.
// Tokens shorter than 3 runes or in stopWords are skipped.
// Uses unicode-aware letter/digit detection so non-ASCII issue text is handled correctly.
func extractKeywords(title, body string) string {
	combined := strings.ToLower(title + " " + body)

	seen := make(map[string]bool)
	var tokens []string

	inWord := false
	wordStart := 0
	for i, r := range combined {
		isAlnum := unicode.IsLetter(r) || unicode.IsDigit(r)
		if isAlnum {
			if !inWord {
				inWord = true
				wordStart = i
			}
		} else {
			if inWord {
				tok := combined[wordStart:i]
				inWord = false
				if utf8.RuneCountInString(tok) >= 3 && !stopWords[tok] && !seen[tok] {
					seen[tok] = true
					tokens = append(tokens, tok)
				}
			}
		}
	}
	// Handle word at end of string
	if inWord {
		tok := combined[wordStart:]
		if utf8.RuneCountInString(tok) >= 3 && !stopWords[tok] && !seen[tok] {
			seen[tok] = true
			tokens = append(tokens, tok)
		}
	}

	sort.Strings(tokens)
	return strings.Join(tokens, " ")
}

// jaccardSimilarity returns the Jaccard similarity between two space-separated keyword strings.
// Returns a value in [0, 1].
func jaccardSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	setA := make(map[string]bool)
	for _, tok := range strings.Fields(a) {
		setA[tok] = true
	}
	intersection := 0
	union := len(setA)
	for _, tok := range strings.Fields(b) {
		if setA[tok] {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// rankTrajectories returns up to limit trajectories sorted by descending Jaccard similarity
// to queryKeywords. Only trajectories with similarity > 0 are returned.
func rankTrajectories(candidates []*models.Trajectory, queryKeywords string, limit int) []*models.Trajectory {
	type scored struct {
		t     *models.Trajectory
		score float64
	}
	var ranked []scored
	for _, t := range candidates {
		s := jaccardSimilarity(queryKeywords, t.Keywords)
		if s > 0 {
			ranked = append(ranked, scored{t, s})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	result := make([]*models.Trajectory, len(ranked))
	for i, r := range ranked {
		result[i] = r.t
	}
	return result
}

// getSimilarTrajectories retrieves the top-k successful trajectories most similar to the given issue.
func (p *Pipeline) getSimilarTrajectories(issue *models.Issue, k int) []*models.Trajectory {
	candidates, err := p.db.GetSuccessfulTrajectories(100)
	if err != nil || len(candidates) == 0 {
		return nil
	}
	queryKeywords := extractKeywords(issue.Title, issue.Body)
	return rankTrajectories(candidates, queryKeywords, k)
}

// buildTrajectory constructs a Trajectory record from pipeline stage results.
func buildTrajectory(
	issue *models.Issue,
	scout *ScoutResult,
	analyst *AnalystResult,
	reviewRounds int,
	reviewSummary string,
) *models.Trajectory {
	keywords := extractKeywords(issue.Title, issue.Body)

	var analystPlanJSON string
	if analyst != nil {
		b, _ := json.Marshal(analyst.FixPlan)
		analystPlanJSON = string(b)
	}

	t := &models.Trajectory{
		IssueID:       issue.ID,
		Repo:          issue.Repo,
		IssueNumber:   issue.IssueNumber,
		IssueTitle:    issue.Title,
		IssueBody:     truncate(issue.Body, 1000),
		Keywords:      keywords,
		ReviewRounds:  reviewRounds,
		ReviewSummary: truncate(reviewSummary, 500),
		AnalystPlan:   analystPlanJSON,
	}

	if scout != nil {
		t.ScoutVerdict = scout.Verdict
		t.ScoutApproach = scout.SuggestedApproach
	}

	return t
}

// formatTrajectoriesForPrompt formats similar trajectories as few-shot examples for agent prompts.
// Successful trajectories are labeled as positive examples; failed ones as negative.
func formatTrajectoriesForPrompt(trajectories []*models.Trajectory) string {
	if len(trajectories) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Similar Past Trajectories (Experience Replay)\n\n")
	b.WriteString("The following trajectories from similar issues may guide your approach:\n\n")

	for i, t := range trajectories {
		label := "SUCCESS"
		if !t.Success {
			label = "FAILED"
			if t.OutcomeLabel != "" {
				label = fmt.Sprintf("FAILED (%s)", t.OutcomeLabel)
			}
		}

		fmt.Fprintf(&b, "Trajectory %d [%s] - %s#%d\n", i+1, label, sanitizeForPrompt(t.Repo), t.IssueNumber)
		fmt.Fprintf(&b, "Issue: %s\n", sanitizeForPrompt(t.IssueTitle))

		if t.ScoutApproach != "" {
			fmt.Fprintf(&b, "Approach taken: %s\n", sanitizeForPrompt(t.ScoutApproach))
		}

		if t.AnalystPlan != "" {
			var plan FixPlan
			if err := json.Unmarshal([]byte(t.AnalystPlan), &plan); err == nil {
				if plan.Description != "" {
					fmt.Fprintf(&b, "Fix plan: %s\n", sanitizeForPrompt(plan.Description))
				}
				if len(plan.FilesToModify) > 0 {
					fmt.Fprintf(&b, "Files modified: %s\n", strings.Join(plan.FilesToModify, ", "))
				}
			}
		}

		if t.ReviewRounds > 0 {
			fmt.Fprintf(&b, "Review rounds: %d\n", t.ReviewRounds)
		}

		if t.ReviewSummary != "" {
			fmt.Fprintf(&b, "Review summary: %s\n", sanitizeForPrompt(t.ReviewSummary))
		}

		b.WriteString("\n")
	}

	return b.String()
}
