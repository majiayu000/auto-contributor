"""CI status monitoring and failure handling."""

import asyncio
import json
from dataclasses import dataclass, field
from enum import Enum
from pathlib import Path

import httpx
import structlog

from auto_contributor.config import Settings
from auto_contributor.core.exceptions import CIError
from auto_contributor.solver import ClaudeSolver

logger = structlog.get_logger(__name__)


class CICheckStatus(str, Enum):
    """Status of a CI check."""

    QUEUED = "queued"
    IN_PROGRESS = "in_progress"
    COMPLETED = "completed"


class CIConclusion(str, Enum):
    """Conclusion of a CI check."""

    SUCCESS = "success"
    FAILURE = "failure"
    NEUTRAL = "neutral"
    CANCELLED = "cancelled"
    SKIPPED = "skipped"
    TIMED_OUT = "timed_out"
    ACTION_REQUIRED = "action_required"


@dataclass
class CICheck:
    """A CI check result."""

    name: str
    status: CICheckStatus
    conclusion: CIConclusion | None = None
    details_url: str | None = None
    started_at: str | None = None
    completed_at: str | None = None


@dataclass
class CIStatus:
    """Overall CI status for a PR."""

    checks: list[CICheck] = field(default_factory=list)
    all_passed: bool = False
    has_failures: bool = False
    is_pending: bool = True

    @property
    def failed_checks(self) -> list[CICheck]:
        """Get list of failed checks."""
        return [c for c in self.checks if c.conclusion == CIConclusion.FAILURE]


class CIMonitor:
    """Monitors CI status and handles failures."""

    def __init__(self, settings: Settings, solver: ClaudeSolver):
        self.settings = settings
        self.solver = solver
        self.max_retries = settings.limits_max_retries_per_pr

    async def check_pr_status(self, pr_url: str) -> CIStatus:
        """
        Check the CI status of a pull request.

        Args:
            pr_url: URL of the pull request

        Returns:
            CIStatus with all check results
        """
        # Use gh CLI to get check status
        # Available fields: bucket, completedAt, description, event, link, name, startedAt, state, workflow
        cmd = [
            "gh", "pr", "checks", pr_url,
            "--json", "name,state,link,startedAt,completedAt,bucket",
        ]

        process = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await process.communicate()

        if process.returncode != 0:
            logger.error("failed_to_get_ci_status", error=stderr.decode())
            raise CIError(f"Failed to get CI status: {stderr.decode()}")

        checks_data = json.loads(stdout.decode())
        checks = []

        for check in checks_data:
            # Map state to our status enum
            # gh returns: pass, fail, pending, skipping
            state = check.get("state", "pending").lower()
            bucket = check.get("bucket", "").lower()

            if state in ["pass", "success"]:
                status = CICheckStatus.COMPLETED
                conclusion = CIConclusion.SUCCESS
            elif state in ["fail", "failure"]:
                status = CICheckStatus.COMPLETED
                conclusion = CIConclusion.FAILURE
            elif state in ["pending", "waiting", "queued"]:
                status = CICheckStatus.IN_PROGRESS if bucket == "in_progress" else CICheckStatus.QUEUED
                conclusion = None
            elif state in ["skipping", "skipped"]:
                status = CICheckStatus.COMPLETED
                conclusion = CIConclusion.SKIPPED
            else:
                status = CICheckStatus.IN_PROGRESS
                conclusion = None

            checks.append(CICheck(
                name=check["name"],
                status=status,
                conclusion=conclusion,
                details_url=check.get("link"),
                started_at=check.get("startedAt"),
                completed_at=check.get("completedAt"),
            ))

        # Determine overall status
        all_completed = all(c.status == CICheckStatus.COMPLETED for c in checks)
        has_failures = any(c.conclusion == CIConclusion.FAILURE for c in checks)
        all_passed = all_completed and not has_failures and len(checks) > 0

        return CIStatus(
            checks=checks,
            all_passed=all_passed,
            has_failures=has_failures,
            is_pending=not all_completed,
        )

    async def get_failure_logs(self, check: CICheck) -> str:
        """
        Get logs for a failed CI check.

        Args:
            check: The failed CI check

        Returns:
            Log content as string
        """
        if not check.details_url:
            return "No details URL available"

        # For GitHub Actions, we can try to get logs via gh CLI
        # Extract run ID from URL if it's a GitHub Actions URL
        if "github.com" in check.details_url and "/actions/runs/" in check.details_url:
            return await self._get_github_actions_logs(check.details_url)

        return f"Logs available at: {check.details_url}"

    async def _get_github_actions_logs(self, details_url: str) -> str:
        """Get logs from a GitHub Actions run."""
        # Extract repo and run ID from URL
        # URL format: https://github.com/owner/repo/actions/runs/123456/job/789
        import re

        match = re.search(r"github\.com/([^/]+/[^/]+)/actions/runs/(\d+)", details_url)
        if not match:
            return f"Could not parse URL: {details_url}"

        repo = match.group(1)
        run_id = match.group(2)

        # Get failed job logs
        cmd = [
            "gh", "run", "view", run_id,
            "--repo", repo,
            "--log-failed",
        ]

        process = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await process.communicate()

        if process.returncode != 0:
            return f"Failed to get logs: {stderr.decode()}"

        logs = stdout.decode()

        # Truncate if too long (keep last 5000 chars which usually contain the error)
        if len(logs) > 10000:
            logs = "... (truncated)\n" + logs[-10000:]

        return logs

    async def handle_failure(
        self,
        pr_url: str,
        repo_path: Path,
        retry_count: int,
    ) -> tuple[bool, str]:
        """
        Attempt to fix CI failures.

        Args:
            pr_url: URL of the PR
            repo_path: Path to the repository
            retry_count: Current retry count

        Returns:
            Tuple of (success, message)
        """
        if retry_count >= self.max_retries:
            logger.warning("max_retries_exceeded", pr_url=pr_url, retries=retry_count)
            return False, f"Max retries ({self.max_retries}) exceeded"

        # Get current CI status
        ci_status = await self.check_pr_status(pr_url)

        if ci_status.all_passed:
            return True, "All checks passed"

        if not ci_status.has_failures:
            return False, "CI still pending"

        # Get logs for failed checks and attempt fixes
        for check in ci_status.failed_checks:
            logger.info("attempting_fix", check=check.name, retry=retry_count + 1)

            logs = await self.get_failure_logs(check)

            # Use Claude to fix the failure
            result = await self.solver.fix_ci_failure(
                repo_path=repo_path,
                check_name=check.name,
                logs=logs,
            )

            if result.success:
                logger.info("fix_generated", check=check.name, files=result.files_changed)
                return True, f"Generated fix for {check.name}"
            else:
                logger.warning("fix_failed", check=check.name, error=result.error)

        return False, "Could not fix CI failures"

    async def wait_for_ci(
        self,
        pr_url: str,
        timeout: int = 600,
        poll_interval: int = 30,
    ) -> CIStatus:
        """
        Wait for CI to complete.

        Args:
            pr_url: URL of the PR
            timeout: Maximum time to wait in seconds
            poll_interval: Time between polls in seconds

        Returns:
            Final CIStatus
        """
        elapsed = 0

        while elapsed < timeout:
            status = await self.check_pr_status(pr_url)

            if not status.is_pending:
                return status

            logger.debug("ci_pending", elapsed=elapsed, checks=len(status.checks))
            await asyncio.sleep(poll_interval)
            elapsed += poll_interval

        # Return last known status on timeout
        return await self.check_pr_status(pr_url)

    async def add_comment(self, pr_url: str, comment: str) -> None:
        """Add a comment to a PR."""
        cmd = ["gh", "pr", "comment", pr_url, "--body", comment]

        process = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        await process.communicate()
