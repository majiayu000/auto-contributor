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
    tests_passed: bool | None = None  # None = unknown, True = passed, False = failed
    claude_output: str | None = None  # Raw output for debugging


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
        output = stdout.decode() if stdout else ""
        if output:
            logger.info("claude_output", output_preview=output[:1000])

        # Check what files were changed
        files_changed = await self._get_changed_files(repo_path)
        logger.info("files_changed_check", files=files_changed)

        # Parse completion markers
        fix_status = self._parse_fix_status(output)
        tests_passed = fix_status.get("tests_passed")
        is_complete = fix_status.get("complete", False)

        logger.info(
            "fix_status_parsed",
            complete=is_complete,
            tests_passed=tests_passed,
            reason=fix_status.get("reason"),
        )

        if not files_changed:
            logger.warning("no_changes_made")
            return SolveResult(
                success=False,
                message="Claude did not make any changes",
                files_changed=[],
                tests_passed=tests_passed,
                claude_output=output[-2000:] if output else None,
            )

        # Determine success based on completion marker and tests
        if is_complete and tests_passed:
            message = f"Successfully fixed with {len(files_changed)} files changed, tests passed"
            success = True
        elif is_complete and tests_passed is None:
            message = f"Fix complete but test status unknown, {len(files_changed)} files changed"
            success = True  # Trust Claude if it says complete
        elif not is_complete and fix_status.get("reason"):
            message = f"Fix incomplete: {fix_status.get('reason')}"
            success = False
        else:
            # No marker found, fall back to file changes
            message = f"Modified {len(files_changed)} files (no completion marker)"
            success = True

        logger.info("solve_result", success=success, message=message, files_count=len(files_changed))
        return SolveResult(
            success=success,
            message=message,
            files_changed=files_changed,
            tests_passed=tests_passed,
            claude_output=output[-2000:] if output else None,
        )

    def _parse_fix_status(self, output: str) -> dict:
        """Parse the FIX_COMPLETE or FIX_INCOMPLETE marker from Claude's output."""
        import re

        result = {
            "complete": False,
            "tests_passed": None,
            "reason": None,
            "summary": None,
        }

        # Check for FIX_COMPLETE marker
        if "===FIX_COMPLETE===" in output:
            result["complete"] = True

            # Parse tests status
            tests_match = re.search(r"Tests status:\s*(PASSED|FAILED)", output, re.IGNORECASE)
            if tests_match:
                result["tests_passed"] = tests_match.group(1).upper() == "PASSED"

            # Parse summary
            summary_match = re.search(r"Summary:\s*(.+?)(?:\n|$)", output)
            if summary_match:
                result["summary"] = summary_match.group(1).strip()

        # Check for FIX_INCOMPLETE marker
        elif "===FIX_INCOMPLETE===" in output:
            result["complete"] = False

            # Parse reason
            reason_match = re.search(r"Reason:\s*(.+?)(?:\n|$)", output)
            if reason_match:
                result["reason"] = reason_match.group(1).strip()

        return result

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

        # Truncate very long issue bodies to keep prompt manageable
        issue_body = issue.body
        if len(issue_body) > 5000:
            issue_body = issue_body[:5000] + "\n\n... (truncated, see original issue for full details)"

        return f"""You are an expert software engineer fixing GitHub issue #{issue.issue_number} in repository {issue.repo}.

## Issue Information

**Title:** {issue.title}

**Description:**
{issue_body}

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

### Step 5: TEST-FIX LOOP (CRITICAL - MUST COMPLETE)
This is the most important step. You MUST iterate until all tests pass:

```
REPEAT:
  1. Run the relevant tests:
     - Go: `go test -v ./path/to/affected/package/...`
     - Python: `pytest path/to/affected/tests/ -v`
     - JavaScript/TypeScript: `npm test -- --testPathPattern=affected`
     - Rust: `cargo test`

  2. If tests FAIL:
     - Analyze the failure output carefully
     - Fix the issue causing the failure
     - Go back to step 1

  3. If tests PASS:
     - Run a broader test to catch regressions
     - If broader tests fail, fix and repeat
     - If all pass, exit loop
```

**DO NOT STOP until tests pass.** If you encounter dependency issues you cannot resolve, note them but still attempt to run whatever tests are available.

### Step 6: Final Verification
Before finishing:
1. Review all your changes
2. Ensure code style matches the project
3. Verify no temporary or debug code remains
4. Confirm the fix actually addresses the issue

## Critical Rules

1. **TEST-FIX LOOP IS MANDATORY** - Keep fixing until tests pass
2. **No early exit** - Do not stop at the first failure
3. **Complete fixes only** - Find and fix ALL occurrences
4. **No garbage files** - Do NOT create .log, .tmp, or temporary files
5. **Follow conventions** - Match the project's exact coding style
6. **Minimal changes** - Only modify what's necessary for the fix
7. **No new dependencies** - Unless absolutely required by the fix

## Output Format (MANDATORY)

When you have completed the fix and all tests pass, you MUST output this exact marker:

```
===FIX_COMPLETE===
Files changed: [list of files]
Tests status: PASSED
Summary: [brief description of fix]
```

If you cannot complete the fix (e.g., blocked by external issues), output:

```
===FIX_INCOMPLETE===
Reason: [why the fix could not be completed]
Progress: [what was accomplished]
```

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

    async def evaluate_project_complexity(
        self,
        repo_path: Path,
        timeout: int = 60,
    ) -> dict:
        """
        Use Claude to evaluate project complexity for testing strategy.

        Returns:
            dict with keys:
                - is_complex: bool
                - can_test_locally: bool
                - reasons: list[str]
                - recommended_test_command: str | None
        """
        prompt = """Analyze this project and evaluate its complexity for local testing.

## Task

Quickly scan the project structure and determine:

1. **Can tests run locally without special setup?**
   - Check if it needs GPU (torch, tensorflow, jax, cuda)
   - Check if it needs external services (databases, APIs, cloud)
   - Check if it has complex native dependencies (C extensions, Rust, etc.)
   - Check if it requires specific OS or hardware

2. **What's the recommended test command?**
   - Find the test configuration (pytest.ini, setup.cfg, pyproject.toml, package.json, etc.)
   - Determine the correct test command for this project

## Output Format (JSON only)

```json
{
  "is_complex": true/false,
  "can_test_locally": true/false,
  "reasons": ["reason1", "reason2"],
  "recommended_test_command": "pytest tests/ -v" or null,
  "dependencies_concern": "GPU required" or "None"
}
```

Output ONLY the JSON, no other text.
"""

        try:
            result = await self._run_claude_raw(prompt, repo_path, timeout)

            # Parse JSON from output
            import re
            json_match = re.search(r'\{[^{}]*\}', result, re.DOTALL)
            if json_match:
                data = json.loads(json_match.group())
                logger.info(
                    "project_complexity_evaluated",
                    is_complex=data.get("is_complex"),
                    can_test_locally=data.get("can_test_locally"),
                    reasons=data.get("reasons"),
                )
                return data
        except Exception as e:
            logger.warning("complexity_evaluation_failed", error=str(e))

        # Default: assume can test locally
        return {
            "is_complex": False,
            "can_test_locally": True,
            "reasons": ["evaluation failed, assuming simple"],
            "recommended_test_command": None,
        }

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

        # Truncate logs to prevent "Prompt is too long" error
        # Keep first 3000 chars (context) + last 2000 chars (actual errors)
        max_logs = 5000
        if len(logs) > max_logs:
            truncated_logs = logs[:3000] + "\n\n... (truncated) ...\n\n" + logs[-2000:]
        else:
            truncated_logs = logs

        prompt = f"""The CI check '{check_name}' has failed. Please fix the issue.

## CI Failure Logs
```
{truncated_logs}
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

    async def smart_discover_issues(
        self,
        topic: str = "golang",
        languages: list[str] | None = None,
        min_stars: int = 50,
        limit: int = 5,
        depth: str = "deep",
        timeout: int | None = None,
    ) -> list[dict]:
        """
        Use Claude to intelligently discover and analyze GitHub issues.

        This uses Claude Code to:
        1. Search GitHub for issues matching criteria
        2. Check if issues already have linked PRs
        3. Analyze each issue for suitability
        4. Score and rank issues

        Args:
            topic: Topic to search (e.g., "golang", "ai", "web")
            languages: Languages to filter (e.g., ["go", "python"])
            min_stars: Minimum repo stars
            limit: Max issues to return
            depth: Analysis depth ("quick", "deep", "ultrathink")
            timeout: Optional timeout (default 10 min, 20 min for ultrathink)

        Returns:
            List of discovered issues with analysis
        """
        if timeout is None:
            timeout = 20 * 60 if depth == "ultrathink" else 10 * 60

        languages = languages or self.settings.filter_languages
        languages_str = ", ".join(languages)

        depth_instructions = {
            "quick": "Do a quick assessment based on title and labels only.",
            "deep": "Read the full issue body and analyze thoroughly.",
            "ultrathink": """Use extended thinking to deeply analyze each issue:
- Read the full issue body and all comments
- Check if there's already a PR addressing this issue
- Look at the repo's CONTRIBUTING.md and code structure
- Evaluate if this can realistically be solved automatically
- Consider edge cases and potential complications""",
        }

        prompt = f"""You are an expert at finding GitHub issues suitable for automated solving.

## Task
Find and analyze GitHub issues that can be automatically fixed using Claude Code.

## Search Criteria
- Topic/Focus: {topic}
- Languages: {languages_str}
- Minimum Stars: {min_stars}
- Labels to look for: good first issue, help wanted, bug
- Maximum issue age: 30 days
- Number of issues to return: {limit}

## Analysis Depth
{depth_instructions.get(depth, depth_instructions["deep"])}

## Instructions

1. **Search Phase**: Use GitHub to search for issues matching the criteria:
   - Search with: gh search issues "label:\\"good first issue\\" language:go" --limit 50
   - Or browse trending repos in the topic area

2. **Filter Phase**: For each candidate issue, check:
   - Does it already have a linked PR? (skip if yes)
   - Is the repo actively maintained? (recent commits)
   - Is the issue clear and well-defined?

3. **Analysis Phase**: For promising issues, analyze deeply:
   - Read the full issue description
   - Check for reproduction steps or code examples
   - Evaluate complexity (lines of code, files affected)
   - Identify potential blockers (needs domain knowledge, external services, etc.)
   - Assess likelihood of successful automated fix

4. **Scoring**: Rate each issue 0.0-1.0 based on:
   - 0.9-1.0: Perfect for automation (clear bug, single file, has test)
   - 0.7-0.9: Good candidate (well-defined, low complexity)
   - 0.5-0.7: Possible but challenging (medium complexity)
   - 0.3-0.5: Difficult (high complexity or unclear)
   - 0.0-0.3: Not suitable (requires human judgment)

## Output Format

Return ONLY valid JSON in this exact format (no markdown, no explanation):

{{
  "issues": [
    {{
      "repo": "owner/repo",
      "issue_number": 123,
      "title": "Issue title here",
      "url": "https://github.com/owner/repo/issues/123",
      "suitability_score": 0.85,
      "analysis": {{
        "is_well_defined": true,
        "has_reproduction_steps": true,
        "is_self_contained": true,
        "fix_type": "bug",
        "complexity": "low",
        "estimated_files": 1,
        "blockers": [],
        "recommendation": "Clear bug with stack trace, single file fix likely"
      }},
      "repo_context": {{
        "stars": 1234,
        "has_contributing": true,
        "has_claude_md": false,
        "test_framework": "go test",
        "ci_system": "GitHub Actions"
      }}
    }}
  ],
  "metadata": {{
    "total_candidates": 50,
    "analyzed": 15,
    "selected": {limit}
  }}
}}

Begin discovery now. Search GitHub, analyze issues, and return the JSON result.
"""

        logger.info(
            "smart_discovery_starting",
            topic=topic,
            languages=languages,
            min_stars=min_stars,
            limit=limit,
            depth=depth,
        )

        import tempfile
        with tempfile.TemporaryDirectory() as tmpdir:
            try:
                result = await self._run_claude_raw(prompt, Path(tmpdir), timeout)

                # Parse JSON from output
                import re
                # Find the JSON object
                start = result.find("{")
                end = result.rfind("}") + 1
                if start >= 0 and end > start:
                    json_str = result[start:end]
                    data = json.loads(json_str)
                    issues = data.get("issues", [])
                    metadata = data.get("metadata", {})

                    logger.info(
                        "smart_discovery_complete",
                        issues_found=len(issues),
                        total_candidates=metadata.get("total_candidates"),
                        analyzed=metadata.get("analyzed"),
                    )

                    return issues
                else:
                    logger.warning("no_json_in_discovery_output", output=result[:500])
                    return []

            except Exception as e:
                logger.error("smart_discovery_failed", error=str(e))
                return []
