package pipeline

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/internal/rules"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

var synthesisStages = []string{"scout", "analyst", "engineer", "reviewer", "submitter", "responder"}

// RunSynthesis runs the rule synthesis cycle for all stages.
func (p *Pipeline) RunSynthesis(ctx context.Context) error {
	totalNew, totalUpdated, totalRetired := 0, 0, 0

	for _, stage := range synthesisStages {
		events, err := p.db.GetLabeledEventsByStage(stage, 200)
		if err != nil {
			log.WithFields(Fields{"stage": stage, "error": err}).Warn("failed to query events for synthesis")
			continue
		}
		if len(events) < 5 {
			log.WithFields(Fields{"stage": stage, "events": len(events)}).Debug("not enough labeled events for synthesis")
			continue
		}

		result, err := p.synthesizeForStage(ctx, stage, events)
		if err != nil {
			log.WithFields(Fields{"stage": stage, "error": err}).Warn("synthesis failed for stage")
			continue
		}

		applied := p.applySynthesisResult(stage, result)
		totalNew += applied.newCount
		totalUpdated += applied.updatedCount
		totalRetired += applied.retiredCount

		log.WithFields(Fields{
			"stage":   stage,
			"new":     applied.newCount,
			"updated": applied.updatedCount,
			"retired": applied.retiredCount,
			"summary": result.Summary,
		}).Info("synthesis complete for stage")
	}

	// Reload rules so applyDecay sees the fresh last_validated_at stamps written above
	if err := p.ruleLoader.Reload(); err != nil {
		log.WithFields(Fields{"error": err}).Warn("failed to reload rules after synthesis")
	}

	// Decay confidence on rules without recent evidence (runs on fresh in-memory state)
	p.applyDecay()

	// Reload again so runtime uses post-decay confidence values rather than stale pre-decay ones
	if err := p.ruleLoader.Reload(); err != nil {
		log.WithFields(Fields{"error": err}).Warn("failed to reload rules after decay")
	}

	log.WithFields(Fields{
		"new":     totalNew,
		"updated": totalUpdated,
		"retired": totalRetired,
	}).Info("synthesis cycle complete")

	return nil
}

func (p *Pipeline) synthesizeForStage(ctx context.Context, stage string, events []*models.PipelineEvent) (*SynthesizerResult, error) {
	eventsText := formatEventsForSynthesis(events)
	existingRules := p.ruleLoader.FormatForPrompt(stage)

	// Count outcomes
	merged, rejected, autoClosed := 0, 0, 0
	for _, e := range events {
		switch {
		case e.OutcomeLabel == OutcomeMerged:
			merged++
		case e.OutcomeLabel == OutcomeAutoClosed:
			autoClosed++
		case strings.HasPrefix(e.OutcomeLabel, "rejected"):
			rejected++
		}
	}

	successRate := 0.0
	if len(events) > 0 {
		successRate = float64(merged) / float64(len(events)) * 100
	}

	tmplCtx := map[string]any{
		"Stage":           stage,
		"EventsText":      eventsText,
		"ExistingRules":   existingRules,
		"ExistingRuleIDs": p.ruleLoader.IDSummaryForStage(stage),
		"TotalEvents":     len(events),
		"MergedCount":     merged,
		"RejectedCount":   rejected,
		"AutoClosedCount": autoClosed,
		"SuccessRate":     fmt.Sprintf("%.1f", successRate),
	}

	var result SynthesizerResult
	if _, err := p.runner.RunJSON(ctx, "synthesizer", p.cfg.WorkspaceDir, tmplCtx, &result); err != nil {
		return nil, fmt.Errorf("synthesizer agent failed: %w", err)
	}

	return &result, nil
}

type applyResult struct {
	newCount     int
	updatedCount int
	retiredCount int
}

func (p *Pipeline) applySynthesisResult(stage string, result *SynthesizerResult) applyResult {
	var applied applyResult
	rulesDir := p.ruleLoader.RulesDir()

	// Write new rules (validate first)
	for _, nr := range result.NewRules {
		if nr.EvidenceCount < 3 {
			continue
		}
		if nr.Confidence < 0.3 {
			continue
		}
		if nr.ID == "" || nr.Body == "" {
			continue
		}
		// Don't overwrite existing rules (exact ID match)
		if p.ruleLoader.ByID(nr.ID) != nil {
			continue
		}
		// Skip semantically duplicate rules to prevent rule explosion.
		// Use the trusted function-arg `stage` instead of nr.Stage (model output) to
		// ensure dedup always runs against the correct stage's rule set.
		if match, matchID := p.ruleLoader.HasSemanticMatch(nr.ID, nr.Tags, stage); match {
			log.WithFields(Fields{"rule": nr.ID, "existingMatch": matchID}).Info("skipping semantically duplicate rule")
			continue
		}

		rule := &rules.Rule{
			ID:              nr.ID,
			Stage:           nr.Stage,
			Severity:        nr.Severity,
			Confidence:      normalizeNewRuleConfidence(nr.Confidence),
			Source:          "synthesized",
			CreatedAt:       time.Now().Format("2006-01-02"),
			LastValidatedAt: time.Now().Format("2006-01-02"),
			EvidenceCount:   nr.EvidenceCount,
			Tags:            nr.Tags,
			Condition:       nr.Condition,
			Body:            nr.Body,
		}

		if err := rules.WriteRule(rulesDir, rule); err != nil {
			log.WithFields(Fields{"rule": nr.ID, "error": err}).Warn("failed to write synthesized rule")
			continue
		}
		applied.newCount++
	}

	// Update existing rules
	for _, ur := range result.UpdatedRules {
		existing := p.ruleLoader.ByID(ur.ID)
		if existing == nil {
			continue
		}
		// Never update manual rules
		if existing.Source == "manual" {
			continue
		}
		if err := rules.UpdateRuleConfidence(rulesDir, ur.ID, existing.Stage, ur.NewConfidence); err != nil {
			log.WithFields(Fields{"rule": ur.ID, "error": err}).Warn("failed to update rule confidence")
			continue
		}
		today := time.Now().Format("2006-01-02")
		if err := rules.UpdateRuleLastValidatedAt(rulesDir, ur.ID, existing.Stage, today); err != nil {
			log.WithFields(Fields{"rule": ur.ID, "error": err}).Warn("failed to update rule last_validated_at")
			continue
		}
		applied.updatedCount++
	}

	// Retire rules
	for _, rr := range result.RetiredRules {
		existing := p.ruleLoader.ByID(rr.ID)
		if existing == nil {
			continue
		}
		// Never retire manual rules
		if existing.Source == "manual" {
			continue
		}
		if err := rules.DeleteRule(rulesDir, rr.ID, existing.Stage); err != nil {
			log.WithFields(Fields{"rule": rr.ID, "error": err}).Warn("failed to delete retired rule")
			continue
		}
		applied.retiredCount++
	}

	return applied
}

// applyDecay reduces confidence on synthesized rules without recent evidence.
// Each rule's last_validated_at is read from disk inside DecayRuleIfStale under
// fileMu, so a concurrent stampRuleValidation write is never missed by a stale
// in-memory snapshot.
func (p *Pipeline) applyDecay() {
	rulesDir := p.ruleLoader.RulesDir()
	for _, r := range p.ruleLoader.All() {
		if r.Source != "synthesized" {
			continue
		}
		if r.Confidence <= 0.1 {
			continue // already at floor, skip disk round-trip
		}
		if err := rules.DecayRuleIfStale(rulesDir, r.ID, r.Stage, 0.9, 0.1, 30); err != nil {
			log.WithFields(Fields{"rule": r.ID, "error": err}).Warn("failed to apply decay")
		}
	}
}

// normalizeNewRuleConfidence rounds confidence to the nearest 0.1 in [0.3, 0.9].
// This prevents new rules from inheriting degraded floating-point values from existing ones.
func normalizeNewRuleConfidence(c float64) float64 {
	if c < 0.3 {
		c = 0.5
	} else if c > 0.9 {
		c = 0.9
	}
	return math.Round(c*10) / 10
}

func formatEventsForSynthesis(events []*models.PipelineEvent) string {
	var sb strings.Builder
	for i, e := range events {
		sb.WriteString(fmt.Sprintf("%d. [%s] repo=%s issue=#%d verdict=%s outcome=%s",
			i+1, e.Stage, e.Repo, e.IssueNumber, e.Verdict, e.OutcomeLabel))
		if e.OutputSummary != "" {
			sb.WriteString(fmt.Sprintf(" summary=%s", e.OutputSummary))
		}
		if e.ErrorMessage != "" {
			sb.WriteString(fmt.Sprintf(" error=%s", e.ErrorMessage))
		}
		sb.WriteString(fmt.Sprintf(" duration=%.0fs\n", e.DurationSeconds))
	}
	return sb.String()
}
