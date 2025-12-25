"""Custom exceptions for AutoContributor."""


class AutoContributorError(Exception):
    """Base exception for all AutoContributor errors."""

    def __init__(self, message: str, code: str = "UNKNOWN"):
        self.message = message
        self.code = code
        super().__init__(message)


class GitHubAPIError(AutoContributorError):
    """GitHub API related errors."""

    def __init__(self, message: str, status_code: int | None = None):
        self.status_code = status_code
        super().__init__(message, "GITHUB_API_ERROR")


class GitError(AutoContributorError):
    """Git operation errors."""

    def __init__(self, message: str, command: str | None = None):
        self.command = command
        super().__init__(message, "GIT_ERROR")


class ClaudeError(AutoContributorError):
    """Claude Code CLI errors."""

    def __init__(self, message: str, exit_code: int | None = None):
        self.exit_code = exit_code
        super().__init__(message, "CLAUDE_ERROR")


class TestError(AutoContributorError):
    """Test execution errors."""

    def __init__(self, message: str, failed_tests: list[str] | None = None):
        self.failed_tests = failed_tests or []
        super().__init__(message, "TEST_ERROR")


class PRError(AutoContributorError):
    """Pull request related errors."""

    def __init__(self, message: str, pr_url: str | None = None):
        self.pr_url = pr_url
        super().__init__(message, "PR_ERROR")


class CIError(AutoContributorError):
    """CI/CD related errors."""

    def __init__(self, message: str, check_name: str | None = None, logs: str | None = None):
        self.check_name = check_name
        self.logs = logs
        super().__init__(message, "CI_ERROR")
