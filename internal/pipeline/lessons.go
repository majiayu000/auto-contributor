package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/rules"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// lessonCategory maps keywords to feedback categories.
var lessonCategories = []struct {
	category string
	keywords []string
}{
	{"testing", []string{"test", "mock", "unittest", "coverage", "assert", "spec"}},
	{"scope", []string{"unnecessary", "unrelated", "scope", "minimal", "over-engineer", "extra", "not needed", "remove"}},
	{"style", []string{"naming", "format", "style", "convention", "indent", "whitespace", "lint"}},
	{"docs", []string{"comment", "docstring", "documentation", "readme", "typo"}},
	{"ci", []string{"ci", "build", "compile", "workflow", "action"}},
	{"logic", []string{"bug", "logic", "incorrect", "wrong", "fix", "error", "crash", "race", "nil"}},
	{"repo_structure", []string{"upstream", "belongs in", "other repo", "separate repo", "dependency bump", "go-mysql-server", "the actual change is in", "change is in"}},
}

// categorizeComment returns the best-matching category for a reviewer comment.
func categorizeComment(body string) string {
	lower := strings.ToLower(body)
	for _, cat := range lessonCategories {
		for _, kw := range cat.keywords {
			if strings.Contains(lower, kw) {
				return cat.category
			}
		}
	}
	return "other"
}

// extractLessons extracts actionable lessons from PR reviews and inline comments.
// It filters out bot comments and trivial approvals.
func extractLessons(
	pr *models.PullRequest,
	repo string,
	reviews []ghclient.PRReview,
	comments []ghclient.PRReviewComment,
) []*models.ReviewLesson {
	var lessons []*models.ReviewLesson

	// Extract from review-level comments (CHANGES_REQUESTED or substantive COMMENTED)
	for _, r := range reviews {
		if r.Body == "" || len(r.Body) < 20 {
			continue
		}
		if isBot(r.Author) {
			continue
		}
		// Only learn from substantive feedback
		if r.State != "CHANGES_REQUESTED" && r.State != "COMMENTED" {
			continue
		}

		lessons = append(lessons, &models.ReviewLesson{
			PRID:          pr.ID,
			Repo:          repo,
			Category:      categorizeComment(r.Body),
			Lesson:        summarizeToLesson(r.Body),
			SourceComment: truncate(r.Body, 500),
			Reviewer:      r.Author,
		})
	}

	// Extract from inline comments (these are usually the most specific feedback)
	for _, c := range comments {
		if c.Body == "" || len(c.Body) < 10 {
			continue
		}
		if isBot(c.Author) {
			continue
		}

		lesson := summarizeToLesson(c.Body)
		if c.Path != "" {
			lesson = fmt.Sprintf("[%s] %s", c.Path, lesson)
		}

		lessons = append(lessons, &models.ReviewLesson{
			PRID:          pr.ID,
			Repo:          repo,
			Category:      categorizeComment(c.Body),
			Lesson:        lesson,
			SourceComment: truncate(c.Body, 500),
			Reviewer:      c.Author,
		})
	}

	return lessons
}

// extractLessonsFromIssueComments extracts lessons from issue-level comments on a closed PR.
// Maintainers often explain close reasons here (e.g. "fix belongs in upstream dependency X").
func extractLessonsFromIssueComments(
	pr *models.PullRequest,
	repo string,
	comments []ghclient.IssueComment,
) []*models.ReviewLesson {
	var lessons []*models.ReviewLesson
	for _, c := range comments {
		if c.Body == "" || len(c.Body) < 20 {
			continue
		}
		if isBot(c.Author) {
			continue
		}
		category := categorizeComment(c.Body)
		// Only record comments that match a known actionable category
		if category == "other" {
			continue
		}
		lessons = append(lessons, &models.ReviewLesson{
			PRID:          pr.ID,
			Repo:          repo,
			Category:      category,
			Lesson:        summarizeToLesson(c.Body),
			SourceComment: truncate(c.Body, 500),
			Reviewer:      c.Author,
		})
	}
	return lessons
}

// summarizeToLesson trims a reviewer comment to a concise actionable lesson.
func summarizeToLesson(body string) string {
	// Take first sentence or first 200 chars, whichever is shorter
	body = strings.TrimSpace(body)

	// Find first sentence boundary
	for i, ch := range body {
		if (ch == '.' || ch == '。') && i > 10 && i < 200 {
			return body[:i+1]
		}
	}

	if len(body) > 200 {
		return body[:200] + "..."
	}
	return body
}

// isBot returns true for known bot account patterns.
func isCodecovBot(author string) bool {
	lower := strings.ToLower(author)
	return strings.Contains(lower, "codecov")
}

func isCLABot(author string) bool {
	lower := strings.ToLower(author)
	claPatterns := []string{"cla-assistant", "claassistant", "cla-bot", "contributor-assistant"}
	for _, p := range claPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func isBot(author string) bool {
	lower := strings.ToLower(author)
	botPatterns := []string{"bot", "codecov", "netlify", "vercel", "dependabot", "renovate", "github-actions"}
	for _, p := range botPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// extractAndStoreLessons fetches reviews/comments for a PR and stores lessons in DB.
// Called when a PR reaches terminal state (merged/closed) so we learn from the feedback.
func (p *Pipeline) extractAndStoreLessons(ctx context.Context, pr *models.PullRequest, prRepo string, prInfo *ghclient.PRInfo) {
	// Skip if we already extracted lessons for this PR
	count, _ := p.db.CountLessonsByPR(pr.ID)
	if count > 0 {
		return
	}

	// Get inline review comments
	comments, err := p.gh.GetPRReviewComments(ctx, prRepo, pr.PRNumber)
	if err != nil {
		log.WithError(err).WithField("pr", pr.PRURL).Warn("failed to fetch comments for lesson extraction")
		comments = nil
	}

	// Get issue-level comments — maintainers often explain close reasons here
	issueComments, _ := p.gh.GetPRIssueComments(ctx, prRepo, pr.PRNumber)

	lessons := extractLessons(pr, prRepo, prInfo.Reviews, comments)

	// Also extract from issue comments when PR was closed without merge
	if prInfo.State == "CLOSED" {
		lessons = append(lessons, extractLessonsFromIssueComments(pr, prRepo, issueComments)...)
	}

	saved := 0
	for _, l := range lessons {
		if err := p.db.SaveReviewLesson(l); err != nil {
			log.WithError(err).Warn("failed to save review lesson")
			continue
		}
		saved++
	}

	if saved > 0 {
		log.WithFields(Fields{
			"pr":      pr.PRURL,
			"lessons": saved,
		}).Info("extracted review lessons")
	}

	// Label all pipeline events for this PR/issue with the outcome
	label := ClassifyOutcome(prInfo, issueComments, pr)
	if err := p.db.LabelEventsByIssue(pr.IssueID, label); err != nil {
		log.WithFields(Fields{"error": err}).Warn("failed to label pipeline events")
	} else {
		log.WithFields(Fields{"pr": pr.PRURL, "outcome": label}).Info("labeled pipeline events")
	}

	// Stamp last_validated_at on synthesized rules when a PR merges.
	// Merged PRs confirm that the rules guiding those stages produced good output.
	if label == OutcomeMerged {
		p.stampRuleValidation(pr)
	}
}

// stampRuleValidation sets last_validated_at on all synthesized rules for the pipeline
// stages that were active during this PR's lifecycle. Called only on merged outcomes.
func (p *Pipeline) stampRuleValidation(pr *models.PullRequest) {
	events, err := p.db.GetEventsByIssue(pr.IssueID)
	if err != nil {
		log.WithError(err).Warn("failed to fetch events for rule validation stamp")
		return
	}

	today := time.Now().Format("2006-01-02")
	rulesDir := p.ruleLoader.RulesDir()
	seenStages := make(map[string]bool)

	for _, e := range events {
		if seenStages[e.Stage] {
			continue
		}
		seenStages[e.Stage] = true

		for _, r := range p.ruleLoader.All() {
			if r.Stage != e.Stage || r.Source != "synthesized" {
				continue
			}
			// Only stamp rules that were actually eligible for injection during this PR.
			// Rules below the injection threshold were never used, so a merged PR is not
			// evidence that they produced good output; stamping them would prevent decay
			// of low-quality rules and let them persist indefinitely.
			if r.Confidence < rules.MinConfidenceForInjection {
				continue
			}
			if err := rules.UpdateRuleLastValidatedAt(rulesDir, r.ID, r.Stage, today); err != nil {
				log.WithFields(Fields{"rule": r.ID, "error": err}).Warn("failed to stamp rule last_validated_at")
			}
		}
	}

	log.WithFields(Fields{
		"pr":     pr.PRURL,
		"stages": len(seenStages),
	}).Info("stamped last_validated_at on synthesized rules for merged PR")
}

// isNonActionable returns true for reviews that are just approvals/LGTM with no actionable content.
func isNonActionable(body string) bool {
	lower := strings.ToLower(strings.TrimSpace(body))
	if len(lower) < 30 {
		nonActionable := []string{"lgtm", "looks good", "approved", "ship it", "+1", "👍", "🚀"}
		for _, p := range nonActionable {
			if strings.Contains(lower, p) {
				return true
			}
		}
	}
	return false
}

// formatLessonsForPrompt formats stored lessons into text suitable for injection into agent prompts.
func formatLessonsForPrompt(lessons []*models.ReviewLesson) string {
	if len(lessons) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Lessons from Past Reviews\n\n")
	b.WriteString("Previous contributions received this feedback from upstream reviewers. Avoid repeating these mistakes:\n\n")

	seen := make(map[string]bool)
	for _, l := range lessons {
		// Deduplicate by lesson text
		key := l.Category + ":" + l.Lesson
		if seen[key] {
			continue
		}
		seen[key] = true

		fmt.Fprintf(&b, "- [%s] %s (from %s)\n", l.Category, l.Lesson, l.Repo)
	}

	return b.String()
}
