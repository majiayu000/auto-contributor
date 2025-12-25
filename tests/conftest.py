"""Pytest configuration and fixtures."""

import os
from pathlib import Path

import pytest

# Set test environment variables before importing anything else
os.environ["GITHUB_TOKEN"] = "test_token"
os.environ["GITHUB_USERNAME"] = "test_user"


@pytest.fixture
def temp_workspace(tmp_path: Path) -> Path:
    """Create a temporary workspace directory."""
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    return workspace


@pytest.fixture
def mock_settings(temp_workspace: Path):
    """Create mock settings for testing."""
    from auto_contributor.config import Settings

    return Settings(
        workspace_dir=temp_workspace,
        database_url=f"sqlite+aiosqlite:///{temp_workspace}/test.db",
    )
