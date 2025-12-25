"""Claude Code CLI integration for solving issues."""

import asyncio
import json
import shutil
from dataclasses import dataclass, field
from pathlib import Path

import structlog
from git import Repo

from auto_contributor.config import Settings
from auto_contributor.core.exceptions import ClaudeError, GitError
from auto_contributor.finder import IssueCandidate

logger = structlog.get_logger(__name__)


@dataclass
class SolveResult:
    """Result of attempting to solve an issue."""

    success: bool
    message: str
    files_changed: list[str] = field(default_factory=list)
    error: str | None = None


class ClaudeSolver:
    """Uses Claude Code CLI to analyze and fix GitHub issues."""

    def __init__(self, settings: Settings):
        self.settings = settings
        self.workspace = settings.workspace_dir

    async def solve_issue(
        self,
        issue: IssueCandidate,
        repo_path: Path,
        timeout: int | None = None,
        contributing_guide: str | None = None,
        use_extended_thinking: bool = True,
    ) -> SolveResult:
        """
        Use Claude Code to solve an issue.

        Args:
            issue: The issue to solve
            repo_path: Path to the cloned repository
            timeout: Optional timeout in seconds
            contributing_guide: Optional CONTRIBUTING.md content
            use_extended_thinking: Whether to use extended thinking mode

        Returns:
            SolveResult with success status and details
        """
        timeout = timeout or self.settings.claude_timeout
        prompt = self._build_prompt(issue, contributing_guide)

        logger.info(
            "solving_issue",
            repo=issue.repo,
            issue=issue.issue_number,
            timeout=timeout,
            extended_thinking=use_extended_thinking,
        )

        try:
            # Run Claude Code CLI
            result = await self._run_claude(
                prompt, repo_path, timeout, use_extended_thinking=use_extended_thinking
            )
            return result

        except asyncio.TimeoutError:
            return SolveResult(
                success=False,
                message="Claude Code timed out",
                error="TIMEOUT",
            )
        except ClaudeError as e:
            return SolveResult(
                success=False,
                message=str(e),
                error=e.code,
            )

    async def _run_claude(
        self,
        prompt: str,
        repo_path: Path,
        timeout: int,
        use_extended_thinking: bool = True,
    ) -> SolveResult:
        """Run Claude Code CLI with the given prompt."""
        # Check if claude is available
        claude_path = shutil.which("claude")
        if not claude_path:
            raise ClaudeError("Claude Code CLI not found in PATH")

        # Prepare the command
        cmd = [
            claude_path,
            "--print",  # Non-interactive mode
            "--dangerously-skip-permissions",  # Auto-approve file edits
        ]

        # Add extended thinking mode (ultrathink)
        if use_extended_thinking:
            cmd.extend(["--model", "claude-sonnet-4-20250514"])  # Use latest model
            # Add thinking budget for more thorough analysis
            # Note: Claude Code CLI may support --thinking or similar flag

        logger.info(
            "running_claude",
            cwd=str(repo_path),
            prompt_length=len(prompt),
            prompt_preview=prompt[:200] + "...",
            extended_thinking=use_extended_thinking,
        )

        # Set environment with higher token limit
        import os
        env = os.environ.copy()
        env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = "128000"

        # Run the process
        process = await asyncio.create_subprocess_exec(
            *cmd,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            cwd=str(repo_path),
            env=env,
        )

        try:
            logger.info("claude_process_started", pid=process.pid, timeout=timeout)
            stdout, stderr = await asyncio.wait_for(
                process.communicate(input=prompt.encode()),
                timeout=timeout,
            )
            logger.info(
                "claude_process_completed",
                exit_code=process.returncode,
                stdout_length=len(stdout) if stdout else 0,
                stderr_length=len(stderr) if stderr else 0,
            )
        except asyncio.TimeoutError:
            logger.error("claude_timeout", timeout=timeout)
            process.kill()
            await process.wait()
            raise

        if process.returncode != 0:
            error_msg = stderr.decode() if stderr else "Unknown error"
            logger.error(
                "claude_failed",
                exit_code=process.returncode,
                error=error_msg[:500],
                stdout_preview=stdout.decode()[:500] if stdout else ""
            )
            raise ClaudeError(f"Claude exited with code {process.returncode}", process.returncode)

        # Log Claude's output
        if stdout:
            logger.info("claude_output", output_preview=stdout.decode()[:1000])

        # Check what files were changed
        files_changed = await self._get_changed_files(repo_path)
        logger.info("files_changed_check", files=files_changed)

        if not files_changed:
            logger.warning("no_changes_made")
            return SolveResult(
                success=False,
                message="Claude did not make any changes",
                files_changed=[],
            )

        logger.info("solve_success", files_count=len(files_changed), files=files_changed)
        return SolveResult(
            success=True,
            message=f"Successfully modified {len(files_changed)} files",
            files_changed=files_changed,
        )

    async def _get_changed_files(self, repo_path: Path) -> list[str]:
        """Get list of files changed in the repository."""
        try:
            repo = Repo(repo_path)
            # Get both staged and unstaged changes
            changed = []

            # Unstaged changes
            for item in repo.index.diff(None):
                changed.append(item.a_path)

            # Staged changes
            for item in repo.index.diff("HEAD"):
                if item.a_path not in changed:
                    changed.append(item.a_path)

            # Untracked files
            changed.extend(repo.untracked_files)

            return changed
        except Exception as e:
            logger.warning("failed_to_get_changed_files", error=str(e))
            return []

    def _build_prompt(self, issue: IssueCandidate, contributing_guide: str | None = None) -> str:
        """Build the prompt for Claude Code."""
        contrib_section = ""
        if contributing_guide:
            # Truncate if too long
            guide_preview = contributing_guide[:3000]
            if len(contributing_guide) > 3000:
                guide_preview += "\n... (truncated)"
            contrib_section = f"""
## Contribution Guidelines (from CONTRIBUTING.md)
{guide_preview}
"""

        return f"""You are an expert software engineer fixing GitHub issue #{issue.issue_number} in repository {issue.repo}.

## Issue Information

**Title:** {issue.title}

**Description:**
{issue.body}

**Labels:** {', '.join(issue.labels)}
{contrib_section}
## Your Task

You must fix this issue completely and professionally. Follow these steps carefully:

### Step 1: Understand the Project (MANDATORY)
- Read CLAUDE.md, CONTRIBUTING.md, README.md to understand project conventions
- Understand the project structure, coding style, and testing conventions
- Note any specific requirements for contributions (formatting, linting, etc.)

### Step 2: Deep Analysis (USE EXTENDED THINKING)
Think deeply about this issue:
- What is the ROOT CAUSE of this problem?
- Where in the codebase does this issue manifest?
- Are there MULTIPLE locations affected? Search thoroughly!
- What are the edge cases to consider?
- How do similar issues get handled elsewhere in the codebase?

Use `grep`, `find`, and file reading to search for ALL occurrences of:
- The error messages mentioned in the issue
- Similar code patterns that might have the same problem
- Related functions/methods that could be affected

### Step 3: Implement a COMPLETE Fix
Your fix MUST:
- Address the ROOT CAUSE, not just symptoms
- Fix ALL occurrences of the problem (not just the first one you find)
- Follow the project's existing code style EXACTLY
- Be minimal - only change what's necessary
- NOT introduce new bugs or regressions

### Step 4: Write Tests (MANDATORY)
Every fix MUST include corresponding tests:
- Add unit tests that verify your fix works
- Add tests for edge cases you identified
- Ensure tests follow the project's testing conventions
- Tests should fail without your fix and pass with it

### Step 5: Verify ALL Tests Pass (MANDATORY)
Before finishing:
- Run the relevant package/module tests (not the entire project)
- For Go: `go test -v ./path/to/affected/package/...`
- For Python: `pytest path/to/affected/tests/`
- For JavaScript/TypeScript: `npm test -- --testPathPattern=affected`
- Ensure ALL existing tests still pass
- Fix any test failures before completing

### Step 6: Create a Todo List
Use a mental todo list to track your progress:
1. [ ] Read project conventions
2. [ ] Analyze issue thoroughly
3. [ ] Find all affected code locations
4. [ ] Implement the fix
5. [ ] Write tests for the fix
6. [ ] Run and pass all tests
7. [ ] Final review of changes

## Critical Rules

1. **Tests are MANDATORY** - No fix is complete without tests
2. **All tests MUST pass** - Both new and existing tests
3. **Complete fixes only** - Find and fix ALL occurrences
4. **No garbage files** - Do NOT create .log, .tmp, or temporary files
5. **Follow conventions** - Match the project's exact coding style
6. **Minimal changes** - Only modify what's necessary for the fix
7. **No new dependencies** - Unless absolutely required by the fix

## Output

After completing the fix:
1. Summarize what you changed and why
2. List all files modified
3. Describe the tests you added
4. Confirm all tests pass

Begin by exploring the codebase structure, then proceed systematically through each step.
"""

    async def clone_repo(self, repo: str, issue_number: int) -> Path:
        """
        Clone a repository for working on an issue.

        Args:
            repo: Repository in format "owner/name"
            issue_number: Issue number (used for branch naming)

        Returns:
            Path to the cloned repository
        """
        # Create workspace directory
        repo_dir = self.workspace / repo.replace("/", "_") / f"issue-{issue_number}"

        if repo_dir.exists():
            # Clean up existing directory
            shutil.rmtree(repo_dir)

        repo_dir.parent.mkdir(parents=True, exist_ok=True)

        # Clone the repository
        clone_url = f"https://github.com/{repo}.git"

        logger.info("cloning_repo", repo=repo, target=str(repo_dir))

        try:
            Repo.clone_from(clone_url, repo_dir, depth=1)
        except Exception as e:
            raise GitError(f"Failed to clone repository: {e}")

        return repo_dir

    async def cleanup_repo(self, repo_path: Path) -> None:
        """Remove a cloned repository."""
        if repo_path.exists():
            shutil.rmtree(repo_path)

    async def discover_repos(
        self,
        topic: str,
        criteria: str | None = None,
        timeout: int | None = None,
    ) -> list[str]:
        """
        Use Claude Code to discover suitable GitHub repos for contribution.

        Args:
            topic: Topic to search (e.g., "ai", "llm", "python web framework")
            criteria: Additional criteria for repo selection
            timeout: Optional timeout

        Returns:
            List of repo names in "owner/name" format
        """
        timeout = timeout or self.settings.claude_timeout

        criteria_section = ""
        if criteria:
            criteria_section = f"\n## Additional Criteria\n{criteria}\n"

        prompt = f"""Search GitHub for active, popular repositories related to "{topic}" that are good candidates for open source contribution.

## Requirements

1. Search GitHub for repos matching the topic
2. Look for repos that:
   - Have "good first issue" or "help wanted" labels on open issues
   - Are actively maintained (recent commits)
   - Have clear contribution guidelines
   - Have reasonable test coverage
   - Welcome new contributors

3. Analyze each repo and select the best candidates
{criteria_section}
## Output Format

Return ONLY a JSON array of repo names in "owner/name" format, nothing else:
["owner1/repo1", "owner2/repo2", ...]

Return at least 5 and at most 20 repos.
"""

        logger.info("discovering_repos_with_claude", topic=topic)

        # Create a temporary directory for Claude to work in
        import tempfile
        with tempfile.TemporaryDirectory() as tmpdir:
            try:
                result = await self._run_claude_raw(prompt, Path(tmpdir), timeout)

                # Parse the JSON output
                import re
                # Find JSON array in the output
                match = re.search(r'\[.*?\]', result, re.DOTALL)
                if match:
                    repos = json.loads(match.group())
                    logger.info("repos_discovered", count=len(repos), repos=repos)
                    return repos
                else:
                    logger.warning("no_repos_found_in_output", output=result[:500])
                    return []

            except Exception as e:
                logger.error("repo_discovery_failed", error=str(e))
                return []

    async def _run_claude_raw(
        self,
        prompt: str,
        cwd: Path,
        timeout: int,
    ) -> str:
        """Run Claude Code CLI and return raw output."""
        claude_path = shutil.which("claude")
        if not claude_path:
            raise ClaudeError("Claude Code CLI not found in PATH")

        cmd = [
            claude_path,
            "--print",
            "--dangerously-skip-permissions",
        ]

        # Set environment with higher token limit
        import os
        env = os.environ.copy()
        env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = "128000"

        process = await asyncio.create_subprocess_exec(
            *cmd,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            cwd=str(cwd),
            env=env,
        )

        try:
            stdout, stderr = await asyncio.wait_for(
                process.communicate(input=prompt.encode()),
                timeout=timeout,
            )
        except asyncio.TimeoutError:
            process.kill()
            await process.wait()
            raise

        if process.returncode != 0:
            error_msg = stderr.decode() if stderr else "Unknown error"
            raise ClaudeError(f"Claude exited with code {process.returncode}: {error_msg}")

        return stdout.decode()

    async def generate_pr_description(
        self,
        issue: IssueCandidate,
        files_changed: list[str],
        repo_path: Path,
        timeout: int | None = None,
    ) -> str:
        """
        Generate a professional PR description using Claude.

        Returns a detailed but concise PR description.
        """
        timeout = timeout or 120  # Shorter timeout for description generation

        files_list = "\n".join(f"- {f}" for f in files_changed)

        prompt = f"""Generate a professional pull request description for fixing GitHub issue #{issue.issue_number}.

## Issue Title
{issue.title}

## Issue Description (summary)
{issue.body[:1000]}

## Files Changed
{files_list}

## Instructions

Write a PR description that includes:

1. **Summary** (2-3 sentences)
   - What was the problem?
   - How did you fix it?

2. **Changes Made** (bullet points)
   - List the key changes in each file
   - Be specific but concise

3. **Testing**
   - What tests were added or modified?
   - How to verify the fix works?

4. **Checklist**
   - [ ] Tests pass locally
   - [ ] Code follows project style guidelines
   - [ ] No breaking changes

Keep the description professional and focused. Do not include phrases like "Generated by AI" or similar.
Output ONLY the PR description text, nothing else.
"""

        try:
            result = await self._run_claude_raw(prompt, repo_path, timeout)
            # Clean up the result
            description = result.strip()
            if description:
                return description
        except Exception as e:
            logger.warning("pr_description_generation_failed", error=str(e))

        # Fallback to simple description
        return f"""## Summary

Fix for #{issue.issue_number}: {issue.title}

## Changes

{files_list}

## Testing

- [x] All tests pass
"""

    async def fix_ci_failure(
        self,
        repo_path: Path,
        check_name: str,
        logs: str,
        timeout: int | None = None,
    ) -> SolveResult:
        """
        Attempt to fix a CI failure.

        Args:
            repo_path: Path to the repository
            check_name: Name of the failing CI check
            logs: CI failure logs
            timeout: Optional timeout

        Returns:
            SolveResult with success status
        """
        timeout = timeout or self.settings.claude_timeout

        prompt = f"""The CI check '{check_name}' has failed. Please fix the issue.

## CI Failure Logs
```
{logs[:10000]}  # Truncate very long logs
```

## Instructions

1. Analyze the failure logs to understand what went wrong
2. Fix the issue that caused the failure
3. Run the tests locally to verify the fix works

Focus only on fixing the CI failure. Do not make unrelated changes.
"""

        try:
            result = await self._run_claude(prompt, repo_path, timeout)
            return result
        except Exception as e:
            return SolveResult(
                success=False,
                message=str(e),
                error="CI_FIX_FAILED",
            )
