package pipeline

import (
	"context"
	"fmt"
	"strings"

	ghclient "github.com/majiayu000/auto-contributor/internal/github"
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

	// Get inline comments
	comments, err := p.gh.GetPRReviewComments(ctx, prRepo, pr.PRNumber)
	if err != nil {
		log.WithError(err).WithField("pr", pr.PRURL).Warn("failed to fetch comments for lesson extraction")
		comments = nil
	}

	lessons := extractLessons(pr, prRepo, prInfo.Reviews, comments)
	if len(lessons) == 0 {
		return
	}

	saved := 0
	for _, l := range lessons {
		if err := p.db.SaveReviewLesson(l); err != nil {
			log.WithError(err).Warn("failed to save review lesson")
			continue
		}
		saved++
	}

	log.WithFields(Fields{
		"pr":      pr.PRURL,
		"lessons": saved,
	}).Info("extracted review lessons")
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
