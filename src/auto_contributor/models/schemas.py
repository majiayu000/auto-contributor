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
