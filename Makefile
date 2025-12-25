.PHONY: dev run discover status init test lint fmt typecheck check clean sync upgrade

# Run the bot (one-time)
run:
	uv run auto-contributor run

# Run in daemon mode
daemon:
	uv run auto-contributor run --daemon

# Run with dry-run (discover only)
dry-run:
	uv run auto-contributor run --dry-run

# Discover issues
discover:
	uv run auto-contributor discover --limit 20

# Show status
status:
	uv run auto-contributor status

# Initialize database
init:
	uv run auto-contributor init

# Show configuration
config:
	uv run auto-contributor config

# Run tests
test:
	uv run pytest

# Run tests with coverage
test-cov:
	uv run pytest --cov=auto_contributor --cov-report=html

# Lint code
lint:
	uv run ruff check src tests

# Format code
fmt:
	uv run ruff format src tests
	uv run ruff check --fix src tests

# Type check
typecheck:
	uv run mypy src

# Run all checks
check: fmt lint typecheck test
	@echo "All checks passed!"

# Clean generated files
clean:
	rm -rf .pytest_cache .mypy_cache .ruff_cache htmlcov .coverage
	rm -rf ~/.auto-contributor/workspace/*
	find . -type d -name __pycache__ -exec rm -rf {} +

# Sync dependencies
sync:
	uv sync

# Upgrade dependencies
upgrade:
	uv lock --upgrade
	uv sync

# Install in development mode
install:
	uv sync
	uv run auto-contributor init
