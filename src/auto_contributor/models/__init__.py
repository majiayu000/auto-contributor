"""Database models and schemas."""

from auto_contributor.models.database import Base, get_engine, get_session, get_session_factory, init_db
from auto_contributor.models.schemas import (
    CIRun,
    CIStatus,
    DailyStats,
    FailureReason,
    Issue,
    IssueMetrics,
    IssueStatus,
    PRStatus,
    PullRequest,
    SolveAttempt,
)

__all__ = [
    "Base",
    "get_engine",
    "get_session",
    "get_session_factory",
    "init_db",
    # Core models
    "Issue",
    "IssueStatus",
    "PullRequest",
    "PRStatus",
    "CIRun",
    "CIStatus",
    # Data flywheel models
    "SolveAttempt",
    "IssueMetrics",
    "DailyStats",
    "FailureReason",
]
