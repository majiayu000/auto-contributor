"""Command-line interface for AutoContributor."""

import asyncio
from pathlib import Path

import typer
from rich.console import Console
from rich.table import Table

from auto_contributor import __version__
from auto_contributor.config import get_settings
from auto_contributor.core.logging import setup_logging, get_logger
from auto_contributor.scheduler import ContributionScheduler

app = typer.Typer(
    name="auto-contributor",
    help="Automated GitHub contribution bot using Claude Code CLI",
    no_args_is_help=True,
)

console = Console()


def version_callback(value: bool) -> None:
    """Print version and exit."""
    if value:
        console.print(f"auto-contributor v{__version__}")
        raise typer.Exit()


@app.callback()
def main(
    version: bool = typer.Option(
        False,
        "--version",
        "-v",
        callback=version_callback,
        is_eager=True,
        help="Show version and exit",
    ),
    debug: bool = typer.Option(
        False,
        "--debug",
        "-d",
        help="Enable debug logging",
    ),
) -> None:
    """AutoContributor - Automated GitHub contribution bot."""
    setup_logging(debug=debug)


@app.command()
def run(
    daemon: bool = typer.Option(
        False,
        "--daemon",
        help="Run as a daemon (continuous scheduling)",
    ),
    dry_run: bool = typer.Option(
        False,
        "--dry-run",
        help="Discover issues but don't create PRs",
    ),
    limit: int = typer.Option(
        1,
        "--limit",
        "-l",
        help="Number of issues to process (default: 1)",
    ),
    topic: str = typer.Option(
        None,
        "--topic",
        "-t",
        help="Search trending repos by topic (e.g., 'golang', 'ai', 'rust')",
    ),
    use_claude: bool = typer.Option(
        False,
        "--use-claude",
        help="Use Claude Code to discover suitable repos",
    ),
) -> None:
    """Run the contribution bot."""
    # Disable SQLAlchemy logging unless debug
    import logging
    logging.getLogger("sqlalchemy.engine").setLevel(logging.WARNING)

    settings = get_settings()
    scheduler = ContributionScheduler(settings)

    if daemon:
        console.print("[green]Starting AutoContributor daemon...[/green]")
        console.print(f"Workspace: {settings.workspace_dir}")
        console.print(f"Database: {settings.database_url}")

        async def run_daemon():
            await scheduler.start()
            try:
                # Keep running
                while True:
                    await asyncio.sleep(3600)
            except KeyboardInterrupt:
                await scheduler.stop()

        asyncio.run(run_daemon())
    else:
        if topic:
            console.print(f"[green]Running contribution cycle for topic: {topic} (limit={limit})...[/green]")
        else:
            console.print(f"[green]Running one-time contribution cycle (limit={limit})...[/green]")
        asyncio.run(scheduler.run_once(dry_run=dry_run, limit=limit, topic=topic, use_claude=use_claude))
        console.print("[green]Done![/green]")


@app.command()
def discover(
    limit: int = typer.Option(
        20,
        "--limit",
        "-l",
        help="Maximum number of issues to discover",
    ),
    topic: str = typer.Option(
        None,
        "--topic",
        "-t",
        help="Search trending repos by topic (e.g., 'ai', 'llm', 'machine-learning')",
    ),
    min_stars: int = typer.Option(
        1000,
        "--min-stars",
        help="Minimum stars for trending repo search",
    ),
    use_claude: bool = typer.Option(
        False,
        "--use-claude",
        help="Use Claude Code to discover suitable repos (more intelligent but slower)",
    ),
    check_pr: bool = typer.Option(
        True,
        "--check-pr/--no-check-pr",
        help="Check if issues have linked PRs (slower but more accurate)",
    ),
) -> None:
    """Discover issues without processing them."""
    from auto_contributor.finder import IssueFinder
    from auto_contributor.solver import ClaudeSolver

    settings = get_settings()
    finder = IssueFinder(settings)
    solver = ClaudeSolver(settings)

    async def run_discovery():
        if topic:
            if use_claude:
                # Use Claude Code to discover repos
                console.print(f"[cyan]Using Claude Code to discover repos for topic: {topic}...[/cyan]")
                repos = await solver.discover_repos(topic=topic)
            else:
                # Use GitHub API to search trending repos
                console.print(f"[cyan]Searching trending repos for topic: {topic}...[/cyan]")
                repos = await finder.search_trending_repos(topic=topic, min_stars=min_stars, limit=30)

            console.print(f"[cyan]Found {len(repos)} repos, searching for issues...[/cyan]")
            issues = await finder.find_issues_in_repos(repos=repos, limit=limit)
        else:
            issues = await finder.find_issues(limit=limit)

            # Optionally check for linked PRs
            if check_pr:
                console.print("[cyan]Checking for linked PRs...[/cyan]")
                filtered = []
                for issue in issues:
                    has_pr = await finder.check_has_linked_pr(issue)
                    if not has_pr:
                        filtered.append(issue)
                    else:
                        console.print(f"  [yellow]Skipping {issue.repo}#{issue.issue_number} (has PR)[/yellow]")
                issues = filtered

        await finder.close()
        return issues

    issues = asyncio.run(run_discovery())

    # Display results in a table
    table = Table(title=f"Discovered Issues ({len(issues)})")
    table.add_column("Repo", style="cyan")
    table.add_column("#", style="magenta")
    table.add_column("Title", style="green")
    table.add_column("Difficulty", style="yellow")
    table.add_column("Labels", style="blue")
    if topic:
        table.add_column("Maintainer", style="red")

    for issue in issues:
        difficulty = f"{issue.difficulty_score:.2f}"
        labels = ", ".join(issue.labels[:3])
        if len(issue.labels) > 3:
            labels += "..."

        row = [
            issue.repo,
            str(issue.issue_number),
            issue.title[:50] + "..." if len(issue.title) > 50 else issue.title,
            difficulty,
            labels,
        ]
        if topic:
            row.append("Yes" if issue.is_author_maintainer else "No")

        table.add_row(*row)

    console.print(table)


@app.command()
def status() -> None:
    """Show status of issues and PRs."""
    from auto_contributor.models import Issue, PullRequest, get_session_factory, init_db
    from sqlalchemy import select, func

    async def get_status():
        await init_db()
        factory = get_session_factory()

        async with factory() as session:
            # Count issues by status
            issue_counts = await session.execute(
                select(Issue.status, func.count(Issue.id))
                .group_by(Issue.status)
            )
            issues = dict(issue_counts.all())

            # Count PRs by status
            pr_counts = await session.execute(
                select(PullRequest.status, func.count(PullRequest.id))
                .group_by(PullRequest.status)
            )
            prs = dict(pr_counts.all())

            # Get recent PRs
            recent_prs = await session.execute(
                select(PullRequest)
                .order_by(PullRequest.created_at.desc())
                .limit(5)
            )
            recent = recent_prs.scalars().all()

            return issues, prs, recent

    issues, prs, recent = asyncio.run(get_status())

    # Issues table
    console.print("\n[bold]Issues[/bold]")
    issue_table = Table()
    issue_table.add_column("Status")
    issue_table.add_column("Count")

    for status, count in issues.items():
        issue_table.add_row(status, str(count))

    console.print(issue_table)

    # PRs table
    console.print("\n[bold]Pull Requests[/bold]")
    pr_table = Table()
    pr_table.add_column("Status")
    pr_table.add_column("Count")

    for status, count in prs.items():
        pr_table.add_row(status, str(count))

    console.print(pr_table)

    # Recent PRs
    if recent:
        console.print("\n[bold]Recent PRs[/bold]")
        recent_table = Table()
        recent_table.add_column("URL")
        recent_table.add_column("Status")
        recent_table.add_column("CI Status")
        recent_table.add_column("Retries")

        for pr in recent:
            recent_table.add_row(
                pr.pr_url,
                pr.status,
                pr.ci_status,
                str(pr.retry_count),
            )

        console.print(recent_table)


@app.command()
def init() -> None:
    """Initialize the database and configuration."""
    from auto_contributor.models import init_db

    settings = get_settings()

    console.print(f"Initializing AutoContributor...")
    console.print(f"  Workspace: {settings.workspace_dir}")
    console.print(f"  Database: {settings.database_url}")

    asyncio.run(init_db())

    console.print("[green]Initialization complete![/green]")

    # Check for required tools
    import shutil

    tools = {
        "claude": "Claude Code CLI",
        "gh": "GitHub CLI",
        "git": "Git",
    }

    console.print("\n[bold]Required Tools:[/bold]")
    all_found = True
    for cmd, name in tools.items():
        path = shutil.which(cmd)
        if path:
            console.print(f"  [green]✓[/green] {name}: {path}")
        else:
            console.print(f"  [red]✗[/red] {name}: NOT FOUND")
            all_found = False

    if not all_found:
        console.print("\n[yellow]Warning: Some required tools are missing.[/yellow]")


@app.command()
def config() -> None:
    """Show current configuration."""
    settings = get_settings()

    console.print("[bold]Current Configuration[/bold]\n")

    console.print(f"[cyan]GitHub[/cyan]")
    console.print(f"  Username: {settings.github_username}")
    token = settings.github_token
    console.print(f"  Token: {'*' * 8}...{token[-4:] if len(token) > 4 else '****'}")

    console.print(f"\n[cyan]Claude[/cyan]")
    console.print(f"  Timeout: {settings.claude_timeout}s")
    console.print(f"  Max retries: {settings.claude_max_retries}")

    console.print(f"\n[cyan]Scheduler[/cyan]")
    console.print(f"  Timezone: {settings.scheduler_timezone}")
    console.print(f"  Discovery hour: {settings.scheduler_discovery_hour}:00")
    console.print(f"  Processing hour: {settings.scheduler_processing_hour}:00")
    console.print(f"  CI check interval: {settings.scheduler_ci_check_interval} min")

    console.print(f"\n[cyan]Filters[/cyan]")
    console.print(f"  Languages: {', '.join(settings.filter_languages)}")
    console.print(f"  Include labels: {', '.join(settings.filter_include_labels)}")
    console.print(f"  Exclude labels: {', '.join(settings.filter_exclude_labels)}")
    console.print(f"  Exclude repos: {', '.join(settings.filter_exclude_repos) or '(none)'}")
    console.print(f"  Min repo stars: {settings.filter_min_repo_stars}")
    console.print(f"  Max issue age: {settings.filter_max_issue_age_days} days")

    console.print(f"\n[cyan]Search[/cyan]")
    for i, query in enumerate(settings.search_queries, 1):
        console.print(f"  Query {i}: {query[:60]}...")

    console.print(f"\n[cyan]Limits[/cyan]")
    console.print(f"  Max PRs per day: {settings.limits_max_prs_per_day}")
    console.print(f"  Max retries per PR: {settings.limits_max_retries_per_pr}")
    console.print(f"  Max concurrent solves: {settings.limits_max_concurrent_solves}")


@app.command()
def loop(
    interval: int = typer.Option(
        10,
        "--interval",
        "-i",
        help="Interval in minutes between each cycle",
    ),
    topic: str = typer.Option(
        None,
        "--topic",
        "-t",
        help="Search trending repos by topic",
    ),
    check_ci: bool = typer.Option(
        True,
        "--check-ci/--no-check-ci",
        help="Check CI status of existing PRs",
    ),
) -> None:
    """Run contribution loop: solve issues and check PRs every N minutes."""
    import logging
    from datetime import datetime
    from auto_contributor.monitor import CIMonitor

    # Disable SQLAlchemy logging unless debug
    logging.getLogger("sqlalchemy.engine").setLevel(logging.WARNING)

    settings = get_settings()
    scheduler = ContributionScheduler(settings)
    ci_monitor = CIMonitor(settings, scheduler.solver)

    console.print(f"[green]Starting contribution loop (every {interval} minutes)...[/green]")
    console.print(f"  Topic: {topic or 'default search'}")
    console.print(f"  Check CI: {check_ci}")
    console.print(f"  Press Ctrl+C to stop\n")

    async def run_loop():
        from auto_contributor.models import init_db, get_session_factory, PullRequest, PRStatus
        from sqlalchemy import select

        await init_db()
        session_factory = get_session_factory()
        cycle = 0

        while True:
            cycle += 1
            now = datetime.now().strftime("%H:%M:%S")
            console.print(f"\n[cyan]{'='*50}[/cyan]")
            console.print(f"[cyan]Cycle {cycle} started at {now}[/cyan]")
            console.print(f"[cyan]{'='*50}[/cyan]")

            try:
                # Step 1: Process one issue
                console.print(f"\n[yellow]>>> Step 1: Solving one issue...[/yellow]")
                await scheduler.run_once(dry_run=False, limit=1, topic=topic)

                # Step 2: Check CI status of open PRs
                if check_ci:
                    console.print(f"\n[yellow]>>> Step 2: Checking CI status...[/yellow]")
                    async with session_factory() as session:
                        result = await session.execute(
                            select(PullRequest)
                            .where(PullRequest.status == PRStatus.OPEN.value)
                        )
                        open_prs = result.scalars().all()

                        if open_prs:
                            console.print(f"  Found {len(open_prs)} open PRs to check")
                            for pr in open_prs:
                                console.print(f"  Checking: {pr.pr_url}")
                                try:
                                    ci_status = await ci_monitor.check_pr_status(pr.pr_url)
                                    status_str = "✅ passed" if ci_status.all_passed else (
                                        "❌ failed" if ci_status.has_failures else "⏳ pending"
                                    )
                                    console.print(f"    CI Status: {status_str}")

                                    # Update PR status in database
                                    if ci_status.all_passed:
                                        pr.ci_status = "success"
                                    elif ci_status.has_failures:
                                        pr.ci_status = "failure"
                                    else:
                                        pr.ci_status = "pending"

                                except Exception as e:
                                    console.print(f"    [red]Error: {e}[/red]")

                            await session.commit()
                        else:
                            console.print("  No open PRs to check")

                console.print(f"\n[green]Cycle {cycle} complete. Sleeping {interval} minutes...[/green]")

            except Exception as e:
                console.print(f"\n[red]Cycle {cycle} error: {e}[/red]")
                import traceback
                console.print(traceback.format_exc())

            await asyncio.sleep(interval * 60)

    try:
        asyncio.run(run_loop())
    except KeyboardInterrupt:
        console.print("\n[yellow]Loop stopped by user.[/yellow]")


if __name__ == "__main__":
    app()
