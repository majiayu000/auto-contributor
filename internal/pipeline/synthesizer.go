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

		// Reload after each stage so the next stage's dedup pool reflects
		// retirements and new rules written by this stage.  Without this,
		// a rule retired by stage N is still present in p.ruleLoader.All()
		// when stage N+1 builds its dedup pool, causing a new candidate to
		// be flagged as possible_duplicate of a file that no longer exists.
		if err := p.ruleLoader.Reload(); err != nil {
			log.WithFields(Fields{"stage": stage, "error": err}).Warn("failed to reload rules after stage synthesis")
		}
		p.syncRuleRetriever("stage_synthesis_" + stage)

		totalNew += applied.newCount
		totalUpdated += applied.updatedCount
		totalRetired += applied.retiredCount

		log.WithFields(Fields{
			"stage":        stage,
			"new":          applied.newCount,
			"updated":      applied.updatedCount,
			"retired":      applied.retiredCount,
			"merged":       applied.mergedCount,
			"possible_dup": applied.possibleDupCount,
			"summary":      result.Summary,
		}).Info("synthesis complete for stage")
	}

	// Reload rules so applyDecay sees the fresh last_validated_at stamps written above
	if err := p.ruleLoader.Reload(); err != nil {
		log.WithFields(Fields{"error": err}).Warn("failed to reload rules after synthesis")
	}
	p.syncRuleRetriever("post_synthesis_reload")

	// Decay confidence on rules without recent evidence (runs on fresh in-memory state)
	p.applyDecay()

	// Reload again so runtime uses post-decay confidence values rather than stale pre-decay ones
	if err := p.ruleLoader.Reload(); err != nil {
		log.WithFields(Fields{"error": err}).Warn("failed to reload rules after decay")
	}
	p.syncRuleRetriever("post_decay_reload")

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
	newCount         int
	updatedCount     int
	retiredCount     int
	mergedCount      int
	possibleDupCount int
}

func (p *Pipeline) applySynthesisResult(stage string, result *SynthesizerResult) applyResult {
	var applied applyResult
	rulesDir := p.ruleLoader.RulesDir()

	// Issue 1: pre-compute the set of rules being retired this pass so they are
	// excluded from the dedup pool. Without this, a candidate could be merged
	// into a rule that is subsequently deleted, causing silent rule loss.
	retiredIDs := make(map[string]struct{}, len(result.RetiredRules))
	for _, rr := range result.RetiredRules {
		existing := p.ruleLoader.ByID(rr.ID)
		if existing == nil || existing.Source == "manual" {
			continue
		}
		retiredIDs[rr.ID] = struct{}{}
	}

	// Pre-compute effective confidence after pending updates so the dedup pool
	// excludes rules that will be downgraded below MinConfidenceForInjection in
	// this same pass.  Without this a candidate can be merged into a rule that
	// is subsequently downgraded, causing the candidate's evidence to be
	// discarded and no active rule to be created.
	pendingConf := make(map[string]float64, len(result.UpdatedRules))
	for _, ur := range result.UpdatedRules {
		// Only pre-apply confidence downgrades for rules that actually exist,
		// are non-manual (manual rules are immutable), and belong to this stage
		// or are global. An attacker-controlled or hallucinated low confidence
		// for a manual/cross-stage rule ID must not evict that rule from the
		// dedup pool, which would cause semantically duplicate new_rules to be
		// written instead of being merged/suppressed.
		existing := p.ruleLoader.ByID(ur.ID)
		if existing == nil {
			continue
		}
		if existing.Source == "manual" {
			continue
		}
		if existing.Stage != stage && existing.Stage != "global" {
			continue
		}
		pendingConf[ur.ID] = ur.NewConfidence
	}

	// Build dedup index: rules belonging to this stage (or global), minus those
	// being retired. Only stage-scoped rules are candidates for dedup because
	// prompt injection is stage-filtered (ForStage uses stage=="<stage>" || "global").
	// Merging a candidate into a rule from a different stage would silently drop
	// the candidate from the originating stage in production.
	// TF vectors are pre-computed once here; new rules are added via idx.Add so
	// that later candidates in the same batch are also checked against them.
	allRules := p.ruleLoader.All()
	initialPool := make([]*rules.Rule, 0, len(allRules))
	for _, r := range allRules {
		if _, retiring := retiredIDs[r.ID]; retiring {
			continue
		}
		if r.Stage != stage && r.Stage != "global" {
			continue
		}
		// Use post-update confidence when available so rules being downgraded
		// below the threshold are not included in the dedup index.
		effectiveConf := r.Confidence
		if newConf, ok := pendingConf[r.ID]; ok {
			effectiveConf = newConf
		}
		if effectiveConf < rules.MinConfidenceForInjection {
			continue
		}
		initialPool = append(initialPool, r)
	}
	dedupIdx := rules.NewDedupIndex(initialPool)

	// writtenIDs tracks IDs written during this batch so that a second entry
	// with the same ID cannot overwrite the first WriteRule output while
	// inflating newCount. p.ruleLoader.ByID() only reflects the loader snapshot
	// from before this batch began and cannot catch within-batch duplicates.
	writtenIDs := make(map[string]struct{})

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
		// Reject IDs that contain path separators or dot-dot sequences; WriteRule
		// uses the ID as a filename component and an unsafe value could escape the
		// rules/{stage} directory via path traversal.
		if strings.ContainsAny(nr.ID, "/\\") || strings.Contains(nr.ID, "..") {
			log.WithFields(Fields{"rule": nr.ID}).Warn("skipping rule with unsafe ID")
			continue
		}
		// Don't overwrite existing rules by ID (loader snapshot).
		if p.ruleLoader.ByID(nr.ID) != nil {
			continue
		}
		// Don't write the same ID twice in this batch — a second WriteRule would
		// silently overwrite the first file while still incrementing newCount.
		if _, seen := writtenIDs[nr.ID]; seen {
			log.WithFields(Fields{"rule": nr.ID}).Warn("synthesis batch: skipping duplicate rule ID")
			continue
		}

		// Pin the candidate's stage to the current synthesis stage.
		// The model may emit a different or 'global' stage value; using it
		// verbatim would cause the dedup index (scoped to 'stage') to mismatch
		// the write target, silently dropping valid candidates or writing rules
		// to the wrong stage directory.
		nr.Stage = stage

		// Dedup check: compare candidate text against the pre-computed index.
		candidateText := nr.Condition + " " + nr.Body
		dedup := dedupIdx.Check(candidateText)
		switch dedup.Action {
		case rules.DedupActionMerge:
			// Use the stage and source captured in the dedup index rather than a
			// global ByID() lookup, which could resolve to the wrong rule file
			// when two stages share the same ID.
			// Manual rules are immutable; suppress the synthesized candidate so it
			// cannot bloat the rule set across repeated synthesis runs.
			if dedup.MatchedRuleSource == "manual" {
				log.WithFields(Fields{
					"candidate": nr.ID,
					"matched":   dedup.MatchedRuleID,
					"score":     fmt.Sprintf("%.3f", dedup.Score),
				}).Info("dedup: candidate matches manual rule, suppressing synthesized duplicate")
				applied.mergedCount++
				continue
			}
			// If IncrementEvidenceCount fails (I/O error, missing file),
			// fall through and create the candidate as a new rule rather than
			// silently dropping it. IncrementEvidenceCount also stamps
			// last_validated_at atomically, preventing applyDecay from treating
			// the just-reinforced rule as stale in the same cycle.
			if err := rules.IncrementEvidenceCount(rulesDir, dedup.MatchedRuleID, dedup.MatchedRuleStage); err != nil {
				log.WithFields(Fields{
					"candidate": nr.ID,
					"matched":   dedup.MatchedRuleID,
					"error":     err,
				}).Warn("dedup merge: failed to increment evidence count, creating as new rule")
				break // fall through to WriteRule below
			}
			log.WithFields(Fields{
				"candidate": nr.ID,
				"matched":   dedup.MatchedRuleID,
				"score":     fmt.Sprintf("%.3f", dedup.Score),
			}).Info("dedup: candidate merged into existing rule")
			applied.mergedCount++
			continue
		case rules.DedupActionPossibleDuplicate:
			// Similar but not identical — skip and flag for human review.
			log.WithFields(Fields{
				"candidate": nr.ID,
				"matched":   dedup.MatchedRuleID,
				"score":     fmt.Sprintf("%.3f", dedup.Score),
			}).Warn("dedup: candidate is a possible duplicate, skipping (human review recommended)")
			applied.possibleDupCount++
			continue
		}

		rule := &rules.Rule{
			ID:              nr.ID,
			Stage:           stage, // use validated stage arg, not model-emitted nr.Stage
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
		// Extend the dedup index with the newly written rule so that
		// later candidates in the same batch are checked against it.
		dedupIdx.Add(rule)
		writtenIDs[nr.ID] = struct{}{}
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
