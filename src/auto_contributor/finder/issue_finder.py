"""GitHub issue discovery and filtering."""

import asyncio
import json
from dataclasses import dataclass, field
from datetime import datetime, timedelta

import httpx
import structlog

from auto_contributor.config import Settings
from auto_contributor.core.exceptions import GitHubAPIError

logger = structlog.get_logger(__name__)


@dataclass
class IssueCandidate:
    """A candidate issue for contribution."""

    repo: str
    issue_number: int
    title: str
    body: str
    labels: list[str] = field(default_factory=list)
    language: str | None = None
    difficulty_score: float = 0.5
    created_at: datetime | None = None
    html_url: str = ""
    has_linked_pr: bool = False  # Whether issue already has a linked PR
    is_author_maintainer: bool = False  # Whether issue author is a maintainer


class IssueFinder:
    """Discovers suitable GitHub issues for automated contribution."""

    GITHUB_API = "https://api.github.com"

    def __init__(self, settings: Settings):
        self.settings = settings
        self.client = httpx.AsyncClient(
            headers={
                "Authorization": f"Bearer {settings.github_token}",
                "Accept": "application/vnd.github+json",
                "X-GitHub-Api-Version": "2022-11-28",
            },
            timeout=30.0,
        )

    async def close(self) -> None:
        """Close the HTTP client."""
        await self.client.aclose()

    async def find_issues(self, limit: int = 50, search_queries: list[str] | None = None) -> list[IssueCandidate]:
        """
        Find suitable issues for contribution.

        Args:
            limit: Maximum number of issues to return
            search_queries: Optional list of search queries (uses settings if not provided)

        Returns:
            List of issue candidates sorted by difficulty (easiest first)
        """
        candidates: list[IssueCandidate] = []
        seen_issues: set[str] = set()

        # Use provided queries or default from settings
        queries = search_queries or self.settings.search_queries

        for query in queries:
            # Add language filter
            languages = self.settings.filter_languages
            lang_filter = " ".join(f"language:{lang}" for lang in languages[:3])
            full_query = f"{query} {lang_filter}"

            # Add date filter
            days = self.settings.filter_max_issue_age_days
            date_threshold = (datetime.utcnow() - timedelta(days=days)).strftime("%Y-%m-%d")
            full_query += f" created:>{date_threshold}"

            try:
                issues = await self._search_issues(full_query)

                for issue in issues:
                    issue_key = f"{issue.repo}#{issue.issue_number}"
                    if issue_key in seen_issues:
                        continue

                    if self._is_suitable(issue):
                        issue.difficulty_score = self._calculate_difficulty(issue)
                        candidates.append(issue)
                        seen_issues.add(issue_key)

                        if len(candidates) >= limit:
                            break

            except GitHubAPIError as e:
                logger.warning("search_failed", query=query, error=str(e))
                continue

            if len(candidates) >= limit:
                break

        # Sort by difficulty (easier first)
        candidates.sort(key=lambda x: x.difficulty_score)
        return candidates[:limit]

    async def _search_issues(self, query: str) -> list[IssueCandidate]:
        """Search GitHub issues using the search API."""
        url = f"{self.GITHUB_API}/search/issues"
        params = {
            "q": query,
            "sort": "created",
            "order": "desc",
            "per_page": 30,
        }

        response = await self.client.get(url, params=params)

        if response.status_code == 403:
            raise GitHubAPIError("Rate limit exceeded", status_code=403)

        if response.status_code != 200:
            raise GitHubAPIError(
                f"GitHub API error: {response.text}",
                status_code=response.status_code,
            )

        data = response.json()
        issues = []

        for item in data.get("items", []):
            # Extract repo from URL
            repo_url = item.get("repository_url", "")
            repo = repo_url.replace(f"{self.GITHUB_API}/repos/", "")

            issues.append(
                IssueCandidate(
                    repo=repo,
                    issue_number=item["number"],
                    title=item["title"],
                    body=item.get("body") or "",
                    labels=[label["name"] for label in item.get("labels", [])],
                    created_at=datetime.fromisoformat(
                        item["created_at"].replace("Z", "+00:00")
                    ),
                    html_url=item["html_url"],
                )
            )

        return issues

    async def get_repo_info(self, repo: str) -> dict:
        """Get repository information."""
        url = f"{self.GITHUB_API}/repos/{repo}"
        response = await self.client.get(url)

        if response.status_code != 200:
            raise GitHubAPIError(
                f"Failed to get repo info: {response.text}",
                status_code=response.status_code,
            )

        return response.json()

    def _is_suitable(self, issue: IssueCandidate) -> bool:
        """Check if an issue is suitable for automated contribution."""
        # Check excluded repos from settings
        excluded_repos = self.settings.filter_exclude_repos
        if issue.repo in excluded_repos:
            logger.debug("repo_excluded", repo=issue.repo)
            return False

        # Check labels
        labels_lower = [label.lower() for label in issue.labels]
        exclude_labels = [l.lower() for l in self.settings.filter_exclude_labels]

        for exclude in exclude_labels:
            if exclude in labels_lower:
                return False

        # Must have at least one include label
        include_labels = [l.lower() for l in self.settings.filter_include_labels]
        has_include = any(label in labels_lower for label in include_labels)

        if not has_include:
            return False

        return True

    def _calculate_difficulty(self, issue: IssueCandidate) -> float:
        """
        Estimate issue difficulty (0-1, lower is easier).

        Factors:
        - Labels (good first issue = easier)
        - Body length (longer = more complex)
        - Number of labels (more = more complex)
        """
        score = 0.5
        labels_lower = [label.lower() for label in issue.labels]

        # Label-based scoring
        if "good first issue" in labels_lower:
            score -= 0.2
        if "beginner" in labels_lower or "easy" in labels_lower:
            score -= 0.1
        if "complex" in labels_lower or "hard" in labels_lower:
            score += 0.3
        if "help wanted" in labels_lower:
            score -= 0.05

        # Body length factor
        body_len = len(issue.body)
        if body_len < 200:
            score -= 0.1  # Short issues are usually simpler
        elif body_len > 2000:
            score += 0.15  # Long issues might be complex

        # Number of labels
        if len(issue.labels) > 5:
            score += 0.1

        return max(0.0, min(1.0, score))

    async def enrich_with_repo_info(self, issue: IssueCandidate) -> IssueCandidate:
        """Add repository information to an issue."""
        try:
            repo_info = await self.get_repo_info(issue.repo)
            issue.language = repo_info.get("language")

            # Adjust difficulty based on repo size
            size_mb = repo_info.get("size", 0) / 1024
            if size_mb > self.settings.filter_max_repo_size_mb:
                issue.difficulty_score = min(1.0, issue.difficulty_score + 0.2)

            # Check star count
            stars = repo_info.get("stargazers_count", 0)
            if stars < self.settings.filter_min_repo_stars:
                issue.difficulty_score = min(1.0, issue.difficulty_score + 0.1)

        except GitHubAPIError:
            logger.warning("failed_to_get_repo_info", repo=issue.repo)

        return issue

    async def check_has_linked_pr(self, issue: IssueCandidate) -> bool:
        """
        Check if an issue already has a linked PR.

        Returns True if there's an open or recently closed PR for this issue.
        """
        try:
            # Search for PRs that reference this issue
            url = f"{self.GITHUB_API}/repos/{issue.repo}/issues/{issue.issue_number}/timeline"
            response = await self.client.get(url)

            if response.status_code != 200:
                logger.warning("failed_to_get_timeline", issue=issue.issue_number)
                return False

            timeline = response.json()

            for event in timeline:
                # Check for cross-referenced PRs
                if event.get("event") == "cross-referenced":
                    source = event.get("source", {})
                    source_issue = source.get("issue", {})
                    if source_issue.get("pull_request"):
                        # Check if PR is still open or recently merged
                        pr_state = source_issue.get("state")
                        if pr_state == "open":
                            logger.info(
                                "issue_has_open_pr",
                                repo=issue.repo,
                                issue=issue.issue_number,
                                pr_url=source_issue.get("html_url"),
                            )
                            return True

            # Also search for PRs with issue number in title/body
            pr_search_url = f"{self.GITHUB_API}/search/issues"
            params = {
                "q": f"repo:{issue.repo} is:pr {issue.issue_number} in:title,body",
                "per_page": 5,
            }
            pr_response = await self.client.get(pr_search_url, params=params)

            if pr_response.status_code == 200:
                pr_data = pr_response.json()
                for pr in pr_data.get("items", []):
                    if pr.get("state") == "open":
                        logger.info(
                            "issue_has_related_pr",
                            repo=issue.repo,
                            issue=issue.issue_number,
                            pr_url=pr.get("html_url"),
                        )
                        return True

            return False

        except Exception as e:
            logger.warning("pr_check_failed", issue=issue.issue_number, error=str(e))
            return False

    async def check_author_is_maintainer(self, issue: IssueCandidate) -> bool:
        """
        Check if issue author is a repo maintainer/collaborator.

        Issues created by maintainers might be reserved for specific purposes.
        """
        try:
            url = f"{self.GITHUB_API}/repos/{issue.repo}/issues/{issue.issue_number}"
            response = await self.client.get(url)

            if response.status_code != 200:
                return False

            issue_data = response.json()
            author = issue_data.get("user", {}).get("login")
            author_association = issue_data.get("author_association", "")

            # Check if author is maintainer/owner/member
            maintainer_roles = {"OWNER", "MEMBER", "COLLABORATOR", "MAINTAINER"}
            is_maintainer = author_association in maintainer_roles

            if is_maintainer:
                logger.info(
                    "issue_author_is_maintainer",
                    repo=issue.repo,
                    issue=issue.issue_number,
                    author=author,
                    role=author_association,
                )

            return is_maintainer

        except Exception as e:
            logger.warning("author_check_failed", issue=issue.issue_number, error=str(e))
            return False

    async def find_issues_in_repos(self, repos: list[str], limit: int = 20) -> list[IssueCandidate]:
        """
        Search for good first issues in specified repos.

        Args:
            repos: List of repos in "owner/name" format
            limit: Maximum number of issues to return
        """
        candidates: list[IssueCandidate] = []
        seen_issues: set[str] = set()

        logger.info("searching_specified_repos", repos_count=len(repos))

        for repo in repos:
            if len(candidates) >= limit:
                break

            try:
                # Search for good first issues in this repo
                url = f"{self.GITHUB_API}/repos/{repo}/issues"
                params = {
                    "state": "open",
                    "labels": ",".join(self.settings.filter_include_labels),
                    "per_page": 10,
                }

                response = await self.client.get(url, params=params)

                if response.status_code != 200:
                    logger.warning("failed_to_fetch_repo", repo=repo, status=response.status_code)
                    continue

                issues = response.json()

                for item in issues:
                    # Skip PRs (GitHub API returns them too)
                    if item.get("pull_request"):
                        continue

                    # Skip assigned issues
                    if item.get("assignees"):
                        continue

                    issue_key = f"{repo}#{item['number']}"
                    if issue_key in seen_issues:
                        continue

                    issue = IssueCandidate(
                        repo=repo,
                        issue_number=item["number"],
                        title=item["title"],
                        body=item.get("body") or "",
                        labels=[label["name"] for label in item.get("labels", [])],
                        created_at=datetime.fromisoformat(
                            item["created_at"].replace("Z", "+00:00")
                        ),
                        html_url=item["html_url"],
                    )

                    # Check if issue has linked PR
                    issue.has_linked_pr = await self.check_has_linked_pr(issue)
                    if issue.has_linked_pr:
                        logger.info("skipping_issue_with_pr", repo=repo, issue=item["number"])
                        continue

                    # Check if author is maintainer
                    issue.is_author_maintainer = await self.check_author_is_maintainer(issue)

                    issue.difficulty_score = self._calculate_difficulty(issue)
                    candidates.append(issue)
                    seen_issues.add(issue_key)

                    logger.info(
                        "found_candidate",
                        repo=repo,
                        issue=item["number"],
                        title=item["title"][:50],
                        has_pr=issue.has_linked_pr,
                        maintainer_issue=issue.is_author_maintainer,
                    )

                    if len(candidates) >= limit:
                        break

                # Small delay to avoid rate limiting
                await asyncio.sleep(0.5)

            except Exception as e:
                logger.warning("failed_to_search_repo", repo=repo, error=str(e))
                continue

        # Sort by difficulty (easier first), deprioritize maintainer issues
        candidates.sort(key=lambda x: (x.is_author_maintainer, x.difficulty_score))
        return candidates[:limit]

    async def search_trending_repos(self, topic: str, min_stars: int = 1000, limit: int = 20) -> list[str]:
        """
        Search for trending repos by topic.

        Args:
            topic: Topic to search (e.g., "ai", "llm", "machine-learning")
            min_stars: Minimum star count
            limit: Maximum repos to return

        Returns:
            List of repo names in "owner/name" format
        """
        url = f"{self.GITHUB_API}/search/repositories"

        # Calculate date threshold (pushed in last 7 days = active)
        date_threshold = (datetime.utcnow() - timedelta(days=7)).strftime("%Y-%m-%d")

        params = {
            "q": f"topic:{topic} stars:>{min_stars} pushed:>{date_threshold}",
            "sort": "stars",
            "order": "desc",
            "per_page": limit,
        }

        try:
            response = await self.client.get(url, params=params)

            if response.status_code != 200:
                logger.warning("trending_search_failed", status=response.status_code)
                return []

            data = response.json()
            repos = [item["full_name"] for item in data.get("items", [])]

            logger.info("found_trending_repos", topic=topic, count=len(repos))
            return repos

        except Exception as e:
            logger.warning("trending_search_error", error=str(e))
            return []

    async def get_contributing_guide(self, repo: str) -> str | None:
        """
        Fetch CONTRIBUTING.md from a repository if it exists.
        """
        # Try common locations for contribution guide
        paths = [
            "CONTRIBUTING.md",
            ".github/CONTRIBUTING.md",
            "docs/CONTRIBUTING.md",
            "CONTRIBUTING.rst",
        ]

        for path in paths:
            try:
                url = f"{self.GITHUB_API}/repos/{repo}/contents/{path}"
                response = await self.client.get(url)

                if response.status_code == 200:
                    data = response.json()
                    content = data.get("content", "")
                    if content:
                        import base64
                        decoded = base64.b64decode(content).decode("utf-8")
                        logger.info("found_contributing_guide", repo=repo, path=path)
                        return decoded

            except Exception:
                continue

        return None
