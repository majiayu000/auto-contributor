"""Metrics collector for data flywheel."""

import json
import re
from datetime import datetime
from pathlib import Path

import structlog
from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession

from auto_contributor.models import (
    DailyStats,
    FailureReason,
    Issue,
    IssueMetrics,
    SolveAttempt,
)

logger = structlog.get_logger(__name__)


class MetricsCollector:
    """Collects and records metrics for analysis and ML improvement."""

    def __init__(self, session_factory):
        self.session_factory = session_factory

    async def start_attempt(
        self,
        session: AsyncSession,
        issue: Issue,
        attempt_number: int = 1,
        prompt_version: str = "v2",
        model_used: str = "claude-sonnet-4",
    ) -> SolveAttempt:
        """
        Record the start of a solve attempt.

        Returns the SolveAttempt object for later updates.
        """
        attempt = SolveAttempt(
            issue_id=issue.id,
            attempt_number=attempt_number,
            started_at=datetime.utcnow(),
            prompt_version=prompt_version,
            model_used=model_used,
        )
        session.add(attempt)
        await session.flush()  # Get the ID

        logger.info(
            "attempt_started",
            attempt_id=attempt.id,
            issue_id=issue.id,
            attempt_number=attempt_number,
        )

        return attempt

    async def record_solve_result(
        self,
        session: AsyncSession,
        attempt: SolveAttempt,
        success: bool,
        files_changed: list[str] | None = None,
        claude_output: str | None = None,
        fix_complete_marker: bool = False,
        claude_tests_passed: bool | None = None,
        failure_reason: FailureReason | None = None,
        error_details: str | None = None,
    ) -> None:
        """Record the result of Claude's solve attempt."""
        attempt.completed_at = datetime.utcnow()
        attempt.duration_seconds = (
            attempt.completed_at - attempt.started_at
        ).total_seconds()

        attempt.success = success
        attempt.files_changed = json.dumps(files_changed) if files_changed else None
        attempt.claude_output_preview = claude_output[:2000] if claude_output else None
        attempt.fix_complete_marker = fix_complete_marker
        attempt.claude_tests_passed = claude_tests_passed

        if failure_reason:
            attempt.failure_reason = failure_reason.value
        attempt.error_details = error_details[:1000] if error_details else None

        logger.info(
            "solve_result_recorded",
            attempt_id=attempt.id,
            success=success,
            duration=attempt.duration_seconds,
            failure_reason=attempt.failure_reason,
        )

    async def record_complexity(
        self,
        session: AsyncSession,
        attempt: SolveAttempt,
        complexity: dict,
    ) -> None:
        """Record project complexity evaluation."""
        attempt.is_complex = complexity.get("is_complex", False)
        attempt.can_test_locally = complexity.get("can_test_locally", True)
        attempt.complexity_reasons = json.dumps(complexity.get("reasons", []))

        logger.info(
            "complexity_recorded",
            attempt_id=attempt.id,
            is_complex=attempt.is_complex,
            can_test_locally=attempt.can_test_locally,
        )

    async def record_test_result(
        self,
        session: AsyncSession,
        attempt: SolveAttempt,
        passed: bool,
        framework: str | None = None,
        duration: float | None = None,
        output: str | None = None,
    ) -> None:
        """Record external test result."""
        attempt.external_test_passed = passed
        attempt.test_framework = framework
        attempt.test_duration_seconds = duration
        attempt.test_output_preview = output[:1000] if output else None

        logger.info(
            "test_result_recorded",
            attempt_id=attempt.id,
            passed=passed,
            framework=framework,
            duration=duration,
        )

    async def create_or_update_issue_metrics(
        self,
        session: AsyncSession,
        issue: Issue,
        repo_stars: int | None = None,
        has_contributing: bool = False,
        has_claude_md: bool = False,
    ) -> IssueMetrics:
        """Create or update metrics for an issue."""
        # Check if metrics exist
        result = await session.execute(
            select(IssueMetrics).where(IssueMetrics.issue_id == issue.id)
        )
        metrics = result.scalar_one_or_none()

        if not metrics:
            metrics = IssueMetrics(issue_id=issue.id)
            session.add(metrics)

        # Update metrics
        metrics.estimated_difficulty = issue.difficulty_score
        metrics.repo_language = issue.language
        metrics.repo_stars = repo_stars
        metrics.repo_has_contributing = has_contributing
        metrics.repo_has_claude_md = has_claude_md

        # Issue characteristics
        body = issue.body or ""
        metrics.issue_body_length = len(body)
        metrics.issue_has_code_blocks = "```" in body
        metrics.issue_has_stack_trace = self._detect_stack_trace(body)
        metrics.issue_labels_count = len(issue.labels.split(",")) if issue.labels else 0

        await session.flush()

        logger.info(
            "issue_metrics_updated",
            issue_id=issue.id,
            body_length=metrics.issue_body_length,
            has_code_blocks=metrics.issue_has_code_blocks,
        )

        return metrics

    async def update_issue_metrics_after_attempt(
        self,
        session: AsyncSession,
        issue: Issue,
        attempt: SolveAttempt,
    ) -> None:
        """Update issue metrics after a solve attempt."""
        result = await session.execute(
            select(IssueMetrics).where(IssueMetrics.issue_id == issue.id)
        )
        metrics = result.scalar_one_or_none()

        if not metrics:
            metrics = await self.create_or_update_issue_metrics(session, issue)

        # Update solve statistics
        metrics.total_attempts += 1
        metrics.total_time_spent_seconds += attempt.duration_seconds or 0

        if attempt.success:
            metrics.successful_attempts += 1

        if attempt.attempt_number == 1:
            metrics.first_attempt_success = attempt.success

        # Calculate actual difficulty based on attempts
        if metrics.total_attempts > 0:
            # More attempts = harder issue
            # Success rate influences difficulty
            success_rate = metrics.successful_attempts / metrics.total_attempts
            metrics.actual_difficulty = 1.0 - success_rate

        logger.info(
            "issue_metrics_after_attempt",
            issue_id=issue.id,
            total_attempts=metrics.total_attempts,
            success_rate=metrics.successful_attempts / metrics.total_attempts
            if metrics.total_attempts > 0
            else 0,
        )

    async def update_daily_stats(self, session: AsyncSession) -> DailyStats:
        """Update or create daily statistics for today."""
        today = datetime.utcnow().strftime("%Y-%m-%d")

        result = await session.execute(
            select(DailyStats).where(DailyStats.date == today)
        )
        stats = result.scalar_one_or_none()

        if not stats:
            stats = DailyStats(date=today)
            session.add(stats)

        # Count today's metrics from SolveAttempt
        today_start = datetime.strptime(today, "%Y-%m-%d")

        # Issues attempted today
        attempted = await session.execute(
            select(func.count(func.distinct(SolveAttempt.issue_id))).where(
                SolveAttempt.started_at >= today_start
            )
        )
        stats.issues_attempted = attempted.scalar() or 0

        # Successful solves today
        solved = await session.execute(
            select(func.count(func.distinct(SolveAttempt.issue_id))).where(
                SolveAttempt.started_at >= today_start,
                SolveAttempt.success == True,
            )
        )
        stats.issues_solved = solved.scalar() or 0

        # Average solve time
        avg_time = await session.execute(
            select(func.avg(SolveAttempt.duration_seconds)).where(
                SolveAttempt.started_at >= today_start,
                SolveAttempt.success == True,
            )
        )
        stats.avg_solve_time_seconds = avg_time.scalar()

        # First attempt success rate
        first_attempts = await session.execute(
            select(SolveAttempt).where(
                SolveAttempt.started_at >= today_start,
                SolveAttempt.attempt_number == 1,
            )
        )
        first_list = first_attempts.scalars().all()
        if first_list:
            successful_first = sum(1 for a in first_list if a.success)
            stats.first_attempt_success_rate = successful_first / len(first_list)

        # Failure reasons breakdown
        failure_counts = await session.execute(
            select(SolveAttempt.failure_reason, func.count(SolveAttempt.id))
            .where(
                SolveAttempt.started_at >= today_start,
                SolveAttempt.success == False,
                SolveAttempt.failure_reason != None,
            )
            .group_by(SolveAttempt.failure_reason)
        )
        stats.failure_reasons_count = json.dumps(dict(failure_counts.all()))

        # Stats by language
        lang_stats = await session.execute(
            select(
                Issue.language,
                func.count(SolveAttempt.id).label("attempts"),
                func.sum(SolveAttempt.success).label("successes"),
            )
            .join(Issue)
            .where(SolveAttempt.started_at >= today_start)
            .group_by(Issue.language)
        )
        lang_dict = {}
        for lang, attempts, successes in lang_stats:
            lang_dict[lang or "unknown"] = {
                "attempts": attempts,
                "successes": successes or 0,
                "rate": (successes or 0) / attempts if attempts > 0 else 0,
            }
        stats.stats_by_language = json.dumps(lang_dict)

        logger.info(
            "daily_stats_updated",
            date=today,
            attempted=stats.issues_attempted,
            solved=stats.issues_solved,
        )

        return stats

    def _detect_stack_trace(self, text: str) -> bool:
        """Detect if text contains a stack trace."""
        patterns = [
            r"Traceback \(most recent call last\)",
            r"at .+\(.+:\d+\)",  # Java/JS style
            r"File \".+\", line \d+",  # Python
            r"panic:",  # Go
            r"goroutine \d+",  # Go
            r"Error:.+\n\s+at ",  # Node.js
        ]
        for pattern in patterns:
            if re.search(pattern, text):
                return True
        return False

    async def get_stats_summary(self, session: AsyncSession, days: int = 7) -> dict:
        """Get summary statistics for the last N days."""
        cutoff = datetime.utcnow().replace(hour=0, minute=0, second=0, microsecond=0)

        # Overall stats
        total_attempts = await session.execute(
            select(func.count(SolveAttempt.id)).where(
                SolveAttempt.started_at >= cutoff
            )
        )
        successful = await session.execute(
            select(func.count(SolveAttempt.id)).where(
                SolveAttempt.started_at >= cutoff,
                SolveAttempt.success == True,
            )
        )
        avg_duration = await session.execute(
            select(func.avg(SolveAttempt.duration_seconds)).where(
                SolveAttempt.started_at >= cutoff,
                SolveAttempt.duration_seconds != None,
            )
        )

        total = total_attempts.scalar() or 0
        success = successful.scalar() or 0

        return {
            "period_days": days,
            "total_attempts": total,
            "successful_attempts": success,
            "success_rate": success / total if total > 0 else 0,
            "avg_duration_seconds": avg_duration.scalar() or 0,
        }
