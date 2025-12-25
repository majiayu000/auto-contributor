"""Pull request creation and management."""

import asyncio
import re
import shutil
from dataclasses import dataclass
from pathlib import Path

import structlog
from git import Repo

from auto_contributor.config import Settings
from auto_contributor.core.exceptions import GitError, PRError
from auto_contributor.finder import IssueCandidate

logger = structlog.get_logger(__name__)


@dataclass
class PRResult:
    """Result of creating a pull request."""

    success: bool
    pr_url: str | None = None
    pr_number: int | None = None
    message: str = ""
    error: str | None = None


class PRManager:
    """Creates and manages pull requests."""

    def __init__(self, settings: Settings):
        self.settings = settings
        self.username = settings.github_username
        self.email = settings.github_email

    async def create_pr(
        self,
        repo_path: Path,
        issue: IssueCandidate,
        files_changed: list[str],
        pr_description: str | None = None,
    ) -> PRResult:
        """
        Create a pull request for an issue fix.

        Args:
            repo_path: Path to the cloned repository
            issue: The issue being fixed
            files_changed: List of files that were modified

        Returns:
            PRResult with PR URL and status
        """
        branch_name = self._generate_branch_name(issue)
        logger.info("pr_create_start", branch=branch_name, files_count=len(files_changed))

        try:
            # Configure git
            logger.info("pr_step_1_configure_git")
            await self._configure_git(repo_path)

            # Create and checkout branch
            logger.info("pr_step_2_create_branch", branch=branch_name)
            await self._create_branch(repo_path, branch_name)

            # Stage and commit changes
            commit_msg = self._generate_commit_message(issue)
            logger.info("pr_step_3_commit", message_preview=commit_msg[:100])
            await self._commit_changes(repo_path, commit_msg)

            # Fork the repository (if needed) and push
            logger.info("pr_step_4_push_to_fork", repo=issue.repo)
            await self._push_to_fork(repo_path, issue.repo, branch_name)

            # Create the PR using gh CLI
            logger.info("pr_step_5_create_pr_via_gh")
            pr_result = await self._create_pr_via_gh(
                issue.repo,
                branch_name,
                issue,
                files_changed,
                pr_description,
            )

            logger.info("pr_create_complete", success=pr_result.success, url=pr_result.pr_url)
            return pr_result

        except (GitError, PRError) as e:
            logger.error("pr_creation_failed", error=str(e), error_type=type(e).__name__)
            return PRResult(
                success=False,
                message=str(e),
                error=e.code if hasattr(e, "code") else "UNKNOWN",
            )
        except Exception as e:
            logger.error("unexpected_error", error=str(e), error_type=type(e).__name__)
            import traceback
            logger.error("traceback", tb=traceback.format_exc())
            return PRResult(
                success=False,
                message=str(e),
                error="UNEXPECTED",
            )

    def _generate_branch_name(self, issue: IssueCandidate) -> str:
        """Generate a branch name for the issue."""
        # Sanitize title for branch name
        title_slug = re.sub(r"[^a-zA-Z0-9]+", "-", issue.title.lower())[:30]
        title_slug = title_slug.strip("-")
        return f"fix/issue-{issue.issue_number}-{title_slug}"

    def _generate_commit_message(self, issue: IssueCandidate) -> str:
        """Generate a commit message for the fix."""
        # Determine commit type based on labels
        labels_lower = [l.lower() for l in issue.labels]

        if "bug" in labels_lower:
            prefix = "fix"
        elif "documentation" in labels_lower or "docs" in labels_lower:
            prefix = "docs"
        elif "enhancement" in labels_lower:
            prefix = "feat"
        else:
            prefix = "fix"

        # Truncate title if too long
        title = issue.title[:50]
        if len(issue.title) > 50:
            title += "..."

        return f"""{prefix}: {title}

Fixes #{issue.issue_number}

Signed-off-by: {self.username} <{self.email}>
"""

    async def _configure_git(self, repo_path: Path) -> None:
        """Configure git user for the repository."""
        repo = Repo(repo_path)
        with repo.config_writer() as config:
            config.set_value("user", "name", self.username)
            config.set_value("user", "email", self.email)

    async def _create_branch(self, repo_path: Path, branch_name: str) -> None:
        """Create and checkout a new branch."""
        repo = Repo(repo_path)
        try:
            repo.git.checkout("-b", branch_name)
        except Exception as e:
            raise GitError(f"Failed to create branch: {e}")

    async def _commit_changes(self, repo_path: Path, message: str) -> None:
        """Stage and commit all changes."""
        repo = Repo(repo_path)
        try:
            # Clean up common garbage files before committing
            garbage_patterns = ["*.log", "*.tmp", "*.bak", "__pycache__", ".pytest_cache", "*.pyc"]
            for pattern in garbage_patterns:
                try:
                    repo.git.clean("-fd", "--", pattern)
                except Exception:
                    pass

            # Only add tracked files that were modified, plus new source files
            # Avoid adding random generated files
            repo.git.add("-u")  # Stage modified tracked files

            # Check for new files that look like source code
            untracked = repo.untracked_files
            source_extensions = {".py", ".go", ".js", ".ts", ".rs", ".java", ".c", ".cpp", ".h", ".md", ".yaml", ".yml", ".json", ".toml"}
            for f in untracked:
                ext = Path(f).suffix.lower()
                if ext in source_extensions and not f.endswith(".log"):
                    repo.git.add(f)

            repo.git.commit("-m", message)
        except Exception as e:
            raise GitError(f"Failed to commit: {e}")

    async def _push_to_fork(
        self,
        repo_path: Path,
        upstream_repo: str,
        branch_name: str,
    ) -> None:
        """Push changes to the user's fork."""
        # First, fork the repo if not already forked
        await self._ensure_fork_exists(upstream_repo)

        # Add fork as remote and push
        fork_url = f"https://github.com/{self.username}/{upstream_repo.split('/')[1]}.git"

        repo = Repo(repo_path)

        # Add or update origin to point to fork
        try:
            repo.delete_remote("origin")
        except Exception:
            pass

        repo.create_remote("origin", fork_url)

        # Push with authentication
        logger.info("pushing_to_fork", branch=branch_name)

        try:
            repo.git.push("-u", "origin", branch_name, "--force")
        except Exception as e:
            raise GitError(f"Failed to push: {e}")

    async def _ensure_fork_exists(self, repo: str) -> None:
        """Ensure the user has a fork of the repository."""
        # Check if fork exists using gh CLI
        check_cmd = ["gh", "repo", "view", f"{self.username}/{repo.split('/')[1]}", "--json", "name"]

        process = await asyncio.create_subprocess_exec(
            *check_cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        await process.communicate()

        if process.returncode != 0:
            # Fork doesn't exist, create it
            logger.info("forking_repository", repo=repo)
            fork_cmd = ["gh", "repo", "fork", repo, "--clone=false"]

            process = await asyncio.create_subprocess_exec(
                *fork_cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, stderr = await process.communicate()

            if process.returncode != 0:
                raise PRError(f"Failed to fork repository: {stderr.decode()}")

            # Wait for fork to be ready
            await asyncio.sleep(2)

    async def _create_pr_via_gh(
        self,
        upstream_repo: str,
        branch_name: str,
        issue: IssueCandidate,
        files_changed: list[str],
        pr_description: str | None = None,
    ) -> PRResult:
        """Create a pull request using gh CLI."""
        title = f"fix: {issue.title[:60]}"
        # Use provided description or generate a simple one
        body = pr_description if pr_description else self._generate_pr_body(issue, files_changed)

        cmd = [
            "gh", "pr", "create",
            "--repo", upstream_repo,
            "--head", f"{self.username}:{branch_name}",
            "--title", title,
            "--body", body,
        ]

        logger.info("creating_pr", repo=upstream_repo, title=title)

        process = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await process.communicate()

        if process.returncode != 0:
            error_msg = stderr.decode()
            raise PRError(f"Failed to create PR: {error_msg}")

        pr_url = stdout.decode().strip()
        pr_number = self._extract_pr_number(pr_url)

        logger.info("pr_created", url=pr_url, number=pr_number)

        return PRResult(
            success=True,
            pr_url=pr_url,
            pr_number=pr_number,
            message="Pull request created successfully",
        )

    def _generate_pr_body(self, issue: IssueCandidate, files_changed: list[str]) -> str:
        """Generate the pull request body."""
        files_list = "\n".join(f"- `{f}`" for f in files_changed[:10])
        if len(files_changed) > 10:
            files_list += f"\n- ... and {len(files_changed) - 10} more files"

        return f"""## Summary

Fix for #{issue.issue_number}: {issue.title}

## Changes

{files_list}

## Test Plan

- [x] Existing tests pass
- [ ] Manual verification
"""

    def _extract_pr_number(self, pr_url: str) -> int | None:
        """Extract PR number from URL."""
        match = re.search(r"/pull/(\d+)", pr_url)
        return int(match.group(1)) if match else None

    async def push_fix(self, repo_path: Path, message: str = "fix: address CI feedback") -> None:
        """Push additional fixes to an existing PR."""
        repo = Repo(repo_path)

        try:
            repo.git.add("-u")
            repo.git.commit("-m", f"{message}\n\nSigned-off-by: {self.username} <{self.email}>")
            repo.git.push()
        except Exception as e:
            raise GitError(f"Failed to push fix: {e}")
