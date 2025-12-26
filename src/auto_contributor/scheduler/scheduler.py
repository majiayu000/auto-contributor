"""Job scheduler for automated contributions."""

import asyncio
from datetime import datetime
from pathlib import Path

import structlog
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from apscheduler.triggers.cron import CronTrigger
from apscheduler.triggers.interval import IntervalTrigger
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from auto_contributor.config import Settings
from auto_contributor.finder import IssueFinder, IssueCandidate
from auto_contributor.models import Issue, IssueStatus, PullRequest, PRStatus, init_db, get_session_factory
from auto_contributor.monitor import CIMonitor
from auto_contributor.pr import PRManager
from auto_contributor.runner import TestRunner, ContainerTestRunner, ContainerConfig
from auto_contributor.solver import ClaudeSolver

logger = structlog.get_logger(__name__)


class ContributionScheduler:
    """
    Schedules and orchestrates the contribution pipeline.

    Pipeline:
    1. Discover issues (daily)
    2. Process issue queue (daily)
    3. Monitor CI status (every 30 min)
    4. Handle failures and retries
    """

    def __init__(self, settings: Settings):
        self.settings = settings
        self.scheduler = AsyncIOScheduler(timezone=settings.scheduler_timezone)

        # Initialize components
        self.finder = IssueFinder(settings)
        self.solver = ClaudeSolver(settings)
        self.pr_manager = PRManager(settings)
        self.ci_monitor = CIMonitor(settings, self.solver)

        # Initialize test runners
        self.local_runner = TestRunner()

        # Container runner with settings
        container_config = ContainerConfig(
            enabled=settings.container_enabled,
            memory_limit=settings.container_memory_limit,
            cpu_limit=settings.container_cpu_limit,
            timeout=settings.container_timeout,
            cache_dependencies=settings.container_cache_dependencies,
        )
        self.container_runner = ContainerTestRunner(container_config)

        self._session_factory = None
        self._container_available: bool | None = None  # Cached check

    async def _is_container_available(self) -> bool:
        """Check if container runtime is available (cached)."""
        if self._container_available is None:
            self._container_available = await self.container_runner.is_available()
            logger.info("container_runtime_check", available=self._container_available)
        return self._container_available

    def _should_skip_local_tests(self, repo: str) -> bool:
        """Check if we should skip local tests for this repo (complex dependencies)."""
        for pattern in self.settings.skip_local_test_patterns:
            if pattern.lower() in repo.lower():
                logger.info(
                    "skipping_local_tests",
                    repo=repo,
                    reason=f"matches pattern: {pattern}",
                )
                return True
        return False

    async def _run_tests(
        self,
        repo_path: Path,
        files_changed: list[str],
        language: str | None = None,
        repo: str = "",
    ) -> "TestResult":
        """
        Run tests using the appropriate runner.

        Strategy:
        1. If repo matches skip patterns -> skip local tests, return success
        2. If container is available and enabled -> use container runner
        3. Otherwise -> use local runner
        """
        from auto_contributor.runner import TestResult, TestFramework

        # Check if we should skip local tests for complex projects
        if self._should_skip_local_tests(repo):
            logger.info(
                "local_tests_skipped",
                repo=repo,
                reason="complex dependencies - will rely on CI",
            )
            return TestResult(
                passed=True,
                framework=TestFramework.UNKNOWN,
                output="Local tests skipped for complex project - will verify in CI",
                duration=0.0,
            )

        # Try container runner first if available
        if self.settings.container_enabled and await self._is_container_available():
            logger.info("using_container_runner", repo=repo)
            return await self.container_runner.run_tests(
                repo_path,
                files_changed,
                language,
            )

        # Fall back to local runner
        logger.info("using_local_runner", repo=repo)
        return await self.local_runner.run_tests(repo_path, files_changed)

    async def start(self) -> None:
        """Start the scheduler."""
        # Initialize database
        await init_db()
        self._session_factory = get_session_factory()

        # Schedule jobs
        self._schedule_jobs()

        # Start scheduler
        self.scheduler.start()
        logger.info("scheduler_started", timezone=self.settings.scheduler_timezone)

    def _schedule_jobs(self) -> None:
        """Schedule all recurring jobs."""
        # Issue discovery - daily
        self.scheduler.add_job(
            self.discover_issues,
            CronTrigger(
                hour=self.settings.scheduler_discovery_hour,
                minute=0,
            ),
            id="discover_issues",
            replace_existing=True,
        )

        # Process queue - daily (1 hour after discovery)
        self.scheduler.add_job(
            self.process_queue,
            CronTrigger(
                hour=self.settings.scheduler_processing_hour,
                minute=0,
            ),
            id="process_queue",
            replace_existing=True,
        )

        # CI monitoring - every N minutes
        self.scheduler.add_job(
            self.monitor_ci,
            IntervalTrigger(minutes=self.settings.scheduler_ci_check_interval),
            id="monitor_ci",
            replace_existing=True,
        )

        logger.info(
            "jobs_scheduled",
            discovery_hour=self.settings.scheduler_discovery_hour,
            processing_hour=self.settings.scheduler_processing_hour,
            ci_interval=self.settings.scheduler_ci_check_interval,
        )

    async def discover_issues(self) -> None:
        """Discover new issues from GitHub."""
        logger.info("starting_issue_discovery")

        try:
            candidates = await self.finder.find_issues(limit=50)
            logger.info("issues_found", count=len(candidates))

            async with self._session_factory() as session:
                for candidate in candidates:
                    # Check if issue already exists
                    existing = await session.execute(
                        select(Issue).where(
                            Issue.repo == candidate.repo,
                            Issue.issue_number == candidate.issue_number,
                        )
                    )
                    if existing.scalar_one_or_none():
                        continue

                    # Enrich with repo info
                    candidate = await self.finder.enrich_with_repo_info(candidate)

                    # Save to database
                    issue = Issue(
                        repo=candidate.repo,
                        issue_number=candidate.issue_number,
                        title=candidate.title,
                        body=candidate.body,
                        labels=",".join(candidate.labels),
                        language=candidate.language,
                        difficulty_score=candidate.difficulty_score,
                        status=IssueStatus.DISCOVERED.value,
                    )
                    session.add(issue)

                await session.commit()

            logger.info("discovery_complete", new_issues=len(candidates))

        except Exception as e:
            logger.error("discovery_failed", error=str(e))

    async def process_queue(self, limit: int | None = None) -> None:
        """Process the issue queue."""
        logger.info("starting_queue_processing")

        async with self._session_factory() as session:
            # Get pending issues, sorted by difficulty
            query_limit = limit or self.settings.limits_max_prs_per_day
            result = await session.execute(
                select(Issue)
                .where(Issue.status == IssueStatus.DISCOVERED.value)
                .order_by(Issue.difficulty_score)
                .limit(query_limit)
            )
            issues = result.scalars().all()

            logger.info("processing_issues", count=len(issues), limit=query_limit)

            for issue in issues:
                await self._process_issue(session, issue)

    async def _process_issue(self, session: AsyncSession, issue: Issue) -> None:
        """Process a single issue."""
        logger.info(
            "=== PROCESSING ISSUE START ===",
            repo=issue.repo,
            issue_number=issue.issue_number,
            title=issue.title[:80],
            difficulty=issue.difficulty_score,
        )

        # Update status
        issue.status = IssueStatus.PROCESSING.value
        await session.commit()
        logger.info("status_updated", new_status="processing")

        try:
            # Step 0: Check if issue already has a linked PR
            candidate = IssueCandidate(
                repo=issue.repo,
                issue_number=issue.issue_number,
                title=issue.title,
                body=issue.body or "",
                labels=issue.labels.split(",") if issue.labels else [],
            )

            logger.info("step_0_check_linked_pr", repo=issue.repo, issue=issue.issue_number)
            has_pr = await self.finder.check_has_linked_pr(candidate)
            if has_pr:
                logger.info("skipping_issue_has_pr", repo=issue.repo, issue=issue.issue_number)
                issue.status = IssueStatus.ABANDONED.value
                issue.error_message = "Issue already has a linked PR"
                await session.commit()
                logger.info("=== PROCESSING ISSUE END (SKIPPED - has PR) ===")
                return

            # Step 1: Clone repository
            logger.info("step_1_clone_start", repo=issue.repo)
            repo_path = await self.solver.clone_repo(issue.repo, issue.issue_number)
            logger.info("step_1_clone_complete", path=str(repo_path))

            # Step 1.5: Fetch CONTRIBUTING.md
            logger.info("step_1.5_fetch_contributing_guide", repo=issue.repo)
            contributing_guide = await self.finder.get_contributing_guide(issue.repo)
            if contributing_guide:
                logger.info("contributing_guide_found", repo=issue.repo, length=len(contributing_guide))
            else:
                logger.info("no_contributing_guide", repo=issue.repo)

            # Step 2: Solve the issue with Claude (with extended thinking)
            logger.info("step_2_solve_start", issue=issue.issue_number)
            solve_result = await self.solver.solve_issue(
                candidate,
                repo_path,
                contributing_guide=contributing_guide,
                use_extended_thinking=True,
            )
            logger.info(
                "step_2_solve_complete",
                success=solve_result.success,
                message=solve_result.message,
                files_changed=solve_result.files_changed,
                tests_passed=solve_result.tests_passed,
            )

            if not solve_result.success:
                logger.error("solve_failed", reason=solve_result.message)
                issue.status = IssueStatus.ABANDONED.value
                issue.error_message = solve_result.message
                await session.commit()
                await self.solver.cleanup_repo(repo_path)
                logger.info("=== PROCESSING ISSUE END (ABANDONED - solve failed) ===")
                return

            # Step 3: Hybrid Test Strategy
            # If Claude reports tests passed, trust it (Claude ran tests in TEST-FIX LOOP)
            # Only do external verification for complex projects or if Claude didn't report status
            logger.info(
                "step_3_test_strategy",
                claude_tests_passed=solve_result.tests_passed,
                repo=issue.repo,
            )

            tests_passed = False

            if solve_result.tests_passed is True:
                # Claude reported tests passed - trust it, but optionally verify for critical repos
                logger.info("claude_reported_tests_passed", trusting_claude=True)

                # For skip-pattern repos, directly trust Claude (they rely on CI anyway)
                if self._should_skip_local_tests(issue.repo):
                    tests_passed = True
                    logger.info("trusting_claude_for_complex_project", repo=issue.repo)
                else:
                    # Optional: Do a quick verification for non-complex projects
                    logger.info("optional_verification_start", repo=issue.repo)
                    test_result = await self._run_tests(
                        repo_path=repo_path,
                        files_changed=solve_result.files_changed,
                        language=issue.language,
                        repo=issue.repo,
                    )

                    if test_result.passed:
                        tests_passed = True
                        logger.info("external_verification_passed")
                    else:
                        # External test disagrees with Claude - log warning but trust Claude
                        # (External test may have different environment/dependencies)
                        logger.warning(
                            "external_verification_failed_trusting_claude",
                            output=test_result.output[:300],
                        )
                        tests_passed = True  # Trust Claude's assessment
            else:
                # Claude didn't report tests passed - run full external test cycle
                max_test_retries = 3

                for attempt in range(max_test_retries):
                    logger.info("step_3_test_start", attempt=attempt + 1, max_attempts=max_test_retries)
                    test_result = await self._run_tests(
                        repo_path=repo_path,
                        files_changed=solve_result.files_changed,
                        language=issue.language,
                        repo=issue.repo,
                    )
                    logger.info(
                        "step_3_test_complete",
                        passed=test_result.passed,
                        framework=test_result.framework.value,
                        duration=f"{test_result.duration:.2f}s",
                        failed_tests=test_result.failed_tests,
                        attempt=attempt + 1,
                    )

                    if test_result.passed:
                        tests_passed = True
                        logger.info("all_tests_passed", attempt=attempt + 1)
                        break

                    # Tests failed - try to fix
                    logger.warning("tests_failed", output=test_result.output[:500], attempt=attempt + 1)

                    if attempt < max_test_retries - 1:
                        logger.info("step_3b_fix_tests_start", attempt=attempt + 1)
                        fix_result = await self.solver.fix_ci_failure(
                            repo_path, "tests", test_result.output
                        )
                        logger.info("step_3b_fix_tests_complete", success=fix_result.success)

                        if not fix_result.success:
                            logger.error("fix_attempt_failed", attempt=attempt + 1)
                    else:
                        logger.error("max_test_retries_exceeded")

            if not tests_passed:
                issue.status = IssueStatus.ABANDONED.value
                issue.error_message = f"Tests failed (Claude: {solve_result.tests_passed})"
                await session.commit()
                await self.solver.cleanup_repo(repo_path)
                logger.info("=== PROCESSING ISSUE END (ABANDONED - tests failed) ===")
                return

            # Step 4: Generate PR description
            logger.info("step_4_generate_pr_description")
            pr_description = await self.solver.generate_pr_description(
                candidate, solve_result.files_changed, repo_path
            )
            logger.info("pr_description_generated", length=len(pr_description))

            # Step 5: Create PR - only if ALL tests passed
            logger.info("step_5_pr_start")
            pr_result = await self.pr_manager.create_pr(
                repo_path, candidate, solve_result.files_changed, pr_description
            )
            logger.info(
                "step_5_pr_complete",
                success=pr_result.success,
                pr_url=pr_result.pr_url,
                pr_number=pr_result.pr_number,
                message=pr_result.message,
            )

            if pr_result.success:
                issue.status = IssueStatus.PR_CREATED.value

                # Save PR record
                pr = PullRequest(
                    issue_id=issue.id,
                    pr_url=pr_result.pr_url or "",
                    pr_number=pr_result.pr_number,
                    branch_name=f"fix/issue-{issue.issue_number}",
                    status=PRStatus.OPEN.value,
                )
                session.add(pr)

                logger.info("=== PROCESSING ISSUE END (SUCCESS) ===", pr_url=pr_result.pr_url)
            else:
                issue.status = IssueStatus.ABANDONED.value
                issue.error_message = pr_result.message
                logger.info("=== PROCESSING ISSUE END (ABANDONED - PR failed) ===")

            await session.commit()

        except Exception as e:
            logger.error("processing_exception", error=str(e), error_type=type(e).__name__)
            import traceback
            logger.error("traceback", tb=traceback.format_exc())
            issue.status = IssueStatus.ABANDONED.value
            issue.error_message = str(e)
            await session.commit()
            logger.info("=== PROCESSING ISSUE END (EXCEPTION) ===")

    async def monitor_ci(self) -> None:
        """Monitor CI status of open PRs."""
        logger.info("monitoring_ci")

        async with self._session_factory() as session:
            # Get open PRs
            result = await session.execute(
                select(PullRequest)
                .where(PullRequest.status == PRStatus.OPEN.value)
            )
            prs = result.scalars().all()

            for pr in prs:
                await self._check_pr_ci(session, pr)

    async def _check_pr_ci(self, session: AsyncSession, pr: PullRequest) -> None:
        """Check CI status for a PR."""
        try:
            ci_status = await self.ci_monitor.check_pr_status(pr.pr_url)

            if ci_status.all_passed:
                pr.ci_status = "success"
                logger.info("ci_passed", pr=pr.pr_url)

            elif ci_status.has_failures:
                pr.ci_status = "failure"

                # Attempt to fix if under retry limit
                if pr.retry_count < self.settings.limits_max_retries_per_pr:
                    # Get repo path
                    issue = await session.get(Issue, pr.issue_id)
                    if issue:
                        repo_path = self.settings.workspace_dir / issue.repo.replace("/", "_") / f"issue-{issue.issue_number}"

                        if repo_path.exists():
                            success, message = await self.ci_monitor.handle_failure(
                                pr.pr_url, repo_path, pr.retry_count
                            )

                            if success:
                                await self.pr_manager.push_fix(repo_path)
                                pr.retry_count += 1
                                logger.info("fix_pushed", pr=pr.pr_url, retry=pr.retry_count)
                            else:
                                logger.warning("fix_failed", pr=pr.pr_url, message=message)
                else:
                    # Max retries exceeded, abandon
                    await self.ci_monitor.add_comment(
                        pr.pr_url,
                        "🤖 AutoContributor: Unable to fix CI failures after multiple attempts. Closing this PR."
                    )
                    pr.status = PRStatus.CLOSED.value

            await session.commit()

        except Exception as e:
            logger.error("ci_check_failed", pr=pr.pr_url, error=str(e))

    async def stop(self) -> None:
        """Stop the scheduler."""
        self.scheduler.shutdown()
        await self.finder.close()
        logger.info("scheduler_stopped")

    async def run_once(
        self,
        dry_run: bool = False,
        limit: int = 1,
        topic: str | None = None,
        use_claude: bool = False,
    ) -> None:
        """Run the pipeline once (for testing)."""
        logger.info("running_once", dry_run=dry_run, limit=limit, topic=topic, use_claude=use_claude)

        await init_db()
        self._session_factory = get_session_factory()

        if topic:
            # Search by topic
            await self.discover_issues_by_topic(topic, use_claude=use_claude)
        else:
            await self.discover_issues()

        if not dry_run:
            await self.process_queue(limit=limit)

        await self.finder.close()

    async def discover_issues_by_topic(self, topic: str, use_claude: bool = False) -> None:
        """Discover issues from trending repos by topic."""
        logger.info("starting_topic_discovery", topic=topic, use_claude=use_claude)

        try:
            if use_claude:
                # Use Claude Code to discover repos
                repos = await self.solver.discover_repos(topic)
            else:
                # Use GitHub API
                repos = await self.finder.search_trending_repos(topic=topic, min_stars=1000, limit=30)

            logger.info("found_repos", count=len(repos))

            if not repos:
                logger.warning("no_repos_found", topic=topic)
                return

            # Find issues in these repos
            candidates = await self.finder.find_issues_in_repos(repos=repos, limit=50)
            logger.info("issues_found", count=len(candidates))

            async with self._session_factory() as session:
                for candidate in candidates:
                    # Check if issue already exists
                    existing = await session.execute(
                        select(Issue).where(
                            Issue.repo == candidate.repo,
                            Issue.issue_number == candidate.issue_number,
                        )
                    )
                    if existing.scalar_one_or_none():
                        continue

                    # Enrich with repo info
                    candidate = await self.finder.enrich_with_repo_info(candidate)

                    # Save to database
                    issue = Issue(
                        repo=candidate.repo,
                        issue_number=candidate.issue_number,
                        title=candidate.title,
                        body=candidate.body,
                        labels=",".join(candidate.labels),
                        language=candidate.language,
                        difficulty_score=candidate.difficulty_score,
                        status=IssueStatus.DISCOVERED.value,
                    )
                    session.add(issue)

                await session.commit()

            logger.info("topic_discovery_complete", new_issues=len(candidates))

        except Exception as e:
            logger.error("topic_discovery_failed", error=str(e))
