"""Tests for issue finder."""

import pytest

from auto_contributor.finder import IssueFinder, IssueCandidate


class TestIssueCandidate:
    """Tests for IssueCandidate dataclass."""

    def test_create_candidate(self):
        """Test creating an issue candidate."""
        candidate = IssueCandidate(
            repo="owner/repo",
            issue_number=123,
            title="Test issue",
            body="Test body",
            labels=["bug", "good first issue"],
        )

        assert candidate.repo == "owner/repo"
        assert candidate.issue_number == 123
        assert candidate.title == "Test issue"
        assert len(candidate.labels) == 2


class TestIssueFinder:
    """Tests for IssueFinder."""

    def test_is_suitable_excluded_repo(self, mock_settings):
        """Test that excluded repos are filtered out."""
        finder = IssueFinder(mock_settings)

        candidate = IssueCandidate(
            repo="kubernetes/kubernetes",
            issue_number=1,
            title="Test",
            body="",
            labels=["good first issue"],
        )

        assert not finder._is_suitable(candidate)

    def test_is_suitable_excluded_label(self, mock_settings):
        """Test that issues with excluded labels are filtered out."""
        finder = IssueFinder(mock_settings)

        candidate = IssueCandidate(
            repo="owner/repo",
            issue_number=1,
            title="Test",
            body="",
            labels=["wontfix"],
        )

        assert not finder._is_suitable(candidate)

    def test_is_suitable_no_include_label(self, mock_settings):
        """Test that issues without include labels are filtered out."""
        finder = IssueFinder(mock_settings)

        candidate = IssueCandidate(
            repo="owner/repo",
            issue_number=1,
            title="Test",
            body="",
            labels=["documentation"],
        )

        assert not finder._is_suitable(candidate)

    def test_is_suitable_valid(self, mock_settings):
        """Test that valid issues pass the filter."""
        finder = IssueFinder(mock_settings)

        candidate = IssueCandidate(
            repo="owner/repo",
            issue_number=1,
            title="Test",
            body="",
            labels=["good first issue"],
        )

        assert finder._is_suitable(candidate)

    def test_calculate_difficulty_easy(self, mock_settings):
        """Test difficulty calculation for easy issues."""
        finder = IssueFinder(mock_settings)

        candidate = IssueCandidate(
            repo="owner/repo",
            issue_number=1,
            title="Test",
            body="Short body",
            labels=["good first issue", "beginner"],
        )

        score = finder._calculate_difficulty(candidate)
        assert score < 0.5  # Should be easier than average

    def test_calculate_difficulty_hard(self, mock_settings):
        """Test difficulty calculation for hard issues."""
        finder = IssueFinder(mock_settings)

        candidate = IssueCandidate(
            repo="owner/repo",
            issue_number=1,
            title="Test",
            body="x" * 3000,  # Long body
            labels=["complex", "hard"] + [f"label{i}" for i in range(10)],
        )

        score = finder._calculate_difficulty(candidate)
        assert score > 0.5  # Should be harder than average
