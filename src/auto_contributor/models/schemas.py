"""SQLAlchemy models for database tables."""

from datetime import datetime
from enum import Enum

from sqlalchemy import DateTime, Float, ForeignKey, Integer, String, Text
from sqlalchemy.orm import Mapped, mapped_column, relationship

from auto_contributor.models.database import Base


class IssueStatus(str, Enum):
    """Status of an issue in the pipeline."""

    DISCOVERED = "discovered"
    PROCESSING = "processing"
    PR_CREATED = "pr_created"
    MERGED = "merged"
    ABANDONED = "abandoned"


class PRStatus(str, Enum):
    """Status of a pull request."""

    OPEN = "open"
    READY = "ready"  # CI passed, waiting for maintainer review/merge
    MERGED = "merged"
    CLOSED = "closed"


class CIStatus(str, Enum):
    """Status of a CI run."""

    PENDING = "pending"
    RUNNING = "running"
    SUCCESS = "success"
    FAILURE = "failure"
    CANCELLED = "cancelled"


class Issue(Base):
    """Discovered GitHub issues."""

    __tablename__ = "issues"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    repo: Mapped[str] = mapped_column(String(255), nullable=False, index=True)
    issue_number: Mapped[int] = mapped_column(Integer, nullable=False)
    title: Mapped[str] = mapped_column(String(500), nullable=False)
    body: Mapped[str | None] = mapped_column(Text, nullable=True)
    labels: Mapped[str | None] = mapped_column(String(500), nullable=True)  # JSON list
    language: Mapped[str | None] = mapped_column(String(50), nullable=True)
    difficulty_score: Mapped[float] = mapped_column(Float, default=0.5)
    status: Mapped[str] = mapped_column(
        String(20), default=IssueStatus.DISCOVERED.value, index=True
    )
    error_message: Mapped[str | None] = mapped_column(Text, nullable=True)
    discovered_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime, default=datetime.utcnow, onupdate=datetime.utcnow
    )

    # Relationships
    pull_requests: Mapped[list["PullRequest"]] = relationship(
        "PullRequest", back_populates="issue", cascade="all, delete-orphan"
    )
    solve_attempts: Mapped[list["SolveAttempt"]] = relationship(
        "SolveAttempt", back_populates="issue", cascade="all, delete-orphan"
    )
    metrics: Mapped["IssueMetrics | None"] = relationship(
        "IssueMetrics", back_populates="issue", uselist=False, cascade="all, delete-orphan"
    )

    def __repr__(self) -> str:
        return f"<Issue {self.repo}#{self.issue_number}>"


class PullRequest(Base):
    """Created pull requests."""

    __tablename__ = "pull_requests"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    issue_id: Mapped[int] = mapped_column(Integer, ForeignKey("issues.id"), nullable=False)
    pr_url: Mapped[str] = mapped_column(String(500), nullable=False)
    pr_number: Mapped[int | None] = mapped_column(Integer, nullable=True)
    branch_name: Mapped[str] = mapped_column(String(255), nullable=False)
    status: Mapped[str] = mapped_column(String(20), default=PRStatus.OPEN.value, index=True)
    ci_status: Mapped[str] = mapped_column(String(20), default=CIStatus.PENDING.value)
    retry_count: Mapped[int] = mapped_column(Integer, default=0)
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime, default=datetime.utcnow, onupdate=datetime.utcnow
    )

    # Relationships
    issue: Mapped["Issue"] = relationship("Issue", back_populates="pull_requests")
    ci_runs: Mapped[list["CIRun"]] = relationship(
        "CIRun", back_populates="pull_request", cascade="all, delete-orphan"
    )

    def __repr__(self) -> str:
        return f"<PullRequest {self.pr_url}>"


class CIRun(Base):
    """CI run history."""

    __tablename__ = "ci_runs"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    pr_id: Mapped[int] = mapped_column(Integer, ForeignKey("pull_requests.id"), nullable=False)
    check_name: Mapped[str] = mapped_column(String(255), nullable=False)
    status: Mapped[str] = mapped_column(String(20), nullable=False)
    conclusion: Mapped[str | None] = mapped_column(String(50), nullable=True)
    details_url: Mapped[str | None] = mapped_column(String(500), nullable=True)
    logs: Mapped[str | None] = mapped_column(Text, nullable=True)
    run_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)

    # Relationships
    pull_request: Mapped["PullRequest"] = relationship("PullRequest", back_populates="ci_runs")

    def __repr__(self) -> str:
        return f"<CIRun {self.check_name}: {self.status}>"


class FailureReason(str, Enum):
    """Categorized failure reasons for analysis."""

    TIMEOUT = "timeout"
    NO_CHANGES = "no_changes"
    TESTS_FAILED = "tests_failed"
    CI_FAILED = "ci_failed"
    CLONE_FAILED = "clone_failed"
    PR_FAILED = "pr_failed"
    COMPLEXITY_TOO_HIGH = "complexity_too_high"
    ALREADY_HAS_PR = "already_has_pr"
    UNKNOWN = "unknown"


class SolveAttempt(Base):
    """Records each Claude solve attempt for data flywheel."""

    __tablename__ = "solve_attempts"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    issue_id: Mapped[int] = mapped_column(Integer, ForeignKey("issues.id"), nullable=False)
    attempt_number: Mapped[int] = mapped_column(Integer, default=1)

    # Time tracking
    started_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    completed_at: Mapped[datetime | None] = mapped_column(DateTime, nullable=True)
    duration_seconds: Mapped[float | None] = mapped_column(Float, nullable=True)

    # Claude input
    prompt_version: Mapped[str] = mapped_column(String(50), default="v1")
    model_used: Mapped[str] = mapped_column(String(100), default="claude-sonnet-4")

    # Claude output
    files_changed: Mapped[str | None] = mapped_column(Text, nullable=True)  # JSON list
    claude_output_preview: Mapped[str | None] = mapped_column(Text, nullable=True)
    fix_complete_marker: Mapped[bool] = mapped_column(Integer, default=False)
    claude_tests_passed: Mapped[bool | None] = mapped_column(Integer, nullable=True)

    # Complexity evaluation
    is_complex: Mapped[bool | None] = mapped_column(Integer, nullable=True)
    can_test_locally: Mapped[bool | None] = mapped_column(Integer, nullable=True)
    complexity_reasons: Mapped[str | None] = mapped_column(Text, nullable=True)  # JSON list

    # Test results
    external_test_passed: Mapped[bool | None] = mapped_column(Integer, nullable=True)
    test_framework: Mapped[str | None] = mapped_column(String(50), nullable=True)
    test_duration_seconds: Mapped[float | None] = mapped_column(Float, nullable=True)
    test_output_preview: Mapped[str | None] = mapped_column(Text, nullable=True)

    # Result
    success: Mapped[bool] = mapped_column(Integer, default=False)
    failure_reason: Mapped[str | None] = mapped_column(String(50), nullable=True)
    error_details: Mapped[str | None] = mapped_column(Text, nullable=True)

    # Relationships
    issue: Mapped["Issue"] = relationship("Issue", back_populates="solve_attempts")

    def __repr__(self) -> str:
        return f"<SolveAttempt issue={self.issue_id} #{self.attempt_number} success={self.success}>"


class IssueMetrics(Base):
    """Aggregated metrics for an issue (for ML/analysis)."""

    __tablename__ = "issue_metrics"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    issue_id: Mapped[int] = mapped_column(Integer, ForeignKey("issues.id"), nullable=False, unique=True)

    # Difficulty metrics
    estimated_difficulty: Mapped[float] = mapped_column(Float, default=0.5)
    actual_difficulty: Mapped[float | None] = mapped_column(Float, nullable=True)

    # Repository characteristics
    repo_stars: Mapped[int | None] = mapped_column(Integer, nullable=True)
    repo_language: Mapped[str | None] = mapped_column(String(50), nullable=True)
    repo_has_contributing: Mapped[bool] = mapped_column(Integer, default=False)
    repo_has_claude_md: Mapped[bool] = mapped_column(Integer, default=False)
    repo_test_framework: Mapped[str | None] = mapped_column(String(50), nullable=True)

    # Issue characteristics
    issue_body_length: Mapped[int] = mapped_column(Integer, default=0)
    issue_has_code_blocks: Mapped[bool] = mapped_column(Integer, default=False)
    issue_has_stack_trace: Mapped[bool] = mapped_column(Integer, default=False)
    issue_labels_count: Mapped[int] = mapped_column(Integer, default=0)
    issue_comments_count: Mapped[int | None] = mapped_column(Integer, nullable=True)

    # Solve statistics
    total_attempts: Mapped[int] = mapped_column(Integer, default=0)
    successful_attempts: Mapped[int] = mapped_column(Integer, default=0)
    total_time_spent_seconds: Mapped[float] = mapped_column(Float, default=0.0)
    first_attempt_success: Mapped[bool | None] = mapped_column(Integer, nullable=True)

    # Timestamps
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime, default=datetime.utcnow, onupdate=datetime.utcnow
    )

    # Relationships
    issue: Mapped["Issue"] = relationship("Issue", back_populates="metrics")

    def __repr__(self) -> str:
        return f"<IssueMetrics issue={self.issue_id} attempts={self.total_attempts}>"


class DailyStats(Base):
    """Daily aggregated statistics snapshot."""

    __tablename__ = "daily_stats"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    date: Mapped[str] = mapped_column(String(10), nullable=False, unique=True, index=True)  # YYYY-MM-DD

    # Counts
    issues_discovered: Mapped[int] = mapped_column(Integer, default=0)
    issues_attempted: Mapped[int] = mapped_column(Integer, default=0)
    issues_solved: Mapped[int] = mapped_column(Integer, default=0)
    prs_created: Mapped[int] = mapped_column(Integer, default=0)
    prs_merged: Mapped[int] = mapped_column(Integer, default=0)
    prs_closed: Mapped[int] = mapped_column(Integer, default=0)

    # Efficiency metrics
    avg_solve_time_seconds: Mapped[float | None] = mapped_column(Float, nullable=True)
    avg_attempts_per_issue: Mapped[float | None] = mapped_column(Float, nullable=True)
    first_attempt_success_rate: Mapped[float | None] = mapped_column(Float, nullable=True)
    overall_success_rate: Mapped[float | None] = mapped_column(Float, nullable=True)

    # Breakdown by category (JSON)
    stats_by_language: Mapped[str | None] = mapped_column(Text, nullable=True)
    stats_by_repo: Mapped[str | None] = mapped_column(Text, nullable=True)
    failure_reasons_count: Mapped[str | None] = mapped_column(Text, nullable=True)

    # Timestamps
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)

    def __repr__(self) -> str:
        return f"<DailyStats {self.date} solved={self.issues_solved}>"
