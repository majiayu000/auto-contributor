"""Configuration management using Pydantic Settings."""

from pathlib import Path

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """Main application settings."""

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # Application
    app_name: str = "auto-contributor"
    debug: bool = False
    workspace_dir: Path = Field(default=Path.home() / ".auto-contributor" / "workspace")
    database_url: str = Field(default="sqlite+aiosqlite:///~/.auto-contributor/data.db")

    # GitHub
    github_token: str = Field(description="GitHub personal access token")
    github_username: str = Field(description="GitHub username for contributions")
    github_email: str = Field(
        default="1835304752@qq.com",
        description="Email for git commits and DCO sign-off"
    )

    # Claude
    claude_timeout: int = Field(default=900, description="Max timeout in seconds")
    claude_max_retries: int = Field(default=3, description="Max retries per issue")

    # Scheduler
    scheduler_timezone: str = Field(default="UTC")
    scheduler_discovery_hour: int = Field(default=8)
    scheduler_processing_hour: int = Field(default=9)
    scheduler_ci_check_interval: int = Field(default=30)

    # Filters
    filter_languages: list[str] = Field(
        default=["python", "typescript", "javascript", "go", "rust"]
    )
    filter_include_labels: list[str] = Field(
        default=["good first issue", "help wanted", "bug"]
    )
    filter_exclude_labels: list[str] = Field(default=["wontfix", "duplicate", "invalid"])
    filter_exclude_repos: list[str] = Field(
        default=["denoland/deno"],  # deno has permission issues with Claude Code
        description="Repos to exclude from contribution (owner/name format)"
    )
    filter_min_repo_stars: int = Field(default=100)
    filter_max_repo_size_mb: int = Field(default=500)
    filter_max_issue_age_days: int = Field(
        default=30,
        description="Only consider issues created within this many days"
    )

    # Search
    search_queries: list[str] = Field(
        default=[
            "is:issue is:open label:\"good first issue\" no:assignee",
            "is:issue is:open label:\"help wanted\" no:assignee",
        ],
        description="GitHub search queries for issue discovery"
    )

    # Limits
    limits_max_prs_per_day: int = Field(default=10)
    limits_max_retries_per_pr: int = Field(default=3)
    limits_max_concurrent_solves: int = Field(default=2)

    # Notifications (optional)
    slack_webhook: str | None = None
    notification_email: str | None = None

    def __init__(self, **kwargs):
        super().__init__(**kwargs)
        # Ensure directories exist
        self.workspace_dir.mkdir(parents=True, exist_ok=True)
        db_path = Path(self.database_url.replace("sqlite+aiosqlite:///", "")).expanduser()
        db_path.parent.mkdir(parents=True, exist_ok=True)


_settings: Settings | None = None


def get_settings() -> Settings:
    """Get application settings (cached)."""
    global _settings
    if _settings is None:
        _settings = Settings()
    return _settings
