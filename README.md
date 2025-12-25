# AutoContributor

Automated GitHub contribution bot using Claude Code CLI.

## Features

- **Issue Discovery**: Automatically finds `good-first-issue` and `help-wanted` issues across GitHub
- **Intelligent Solving**: Uses Claude Code CLI to analyze and fix issues
- **Multi-Framework Testing**: Supports pytest, npm, cargo, go, and more
- **Automated PRs**: Creates well-formatted pull requests with DCO signing
- **CI Monitoring**: Watches CI status and attempts to fix failures
- **Retry Logic**: Automatically retries failed CI with intelligent fixes

## Requirements

- Python 3.12+
- [uv](https://github.com/astral-sh/uv) - Python package manager
- [Claude Code CLI](https://claude.ai/code) - AI coding assistant
- [GitHub CLI](https://cli.github.com/) - GitHub command-line tool
- Git

## Installation

```bash
# Clone the repository
git clone https://github.com/yourusername/auto-contributor
cd auto-contributor

# Install dependencies
uv sync

# Copy and configure environment
cp .env.example .env
# Edit .env with your GitHub token and username

# Initialize the database
make init
```

## Configuration

Edit `.env` file with your settings:

```bash
# Required
GITHUB_TOKEN=ghp_your_token_here
GITHUB_USERNAME=your_username

# Optional - adjust as needed
SCHEDULER_DISCOVERY_HOUR=8      # Hour (UTC) to discover issues
SCHEDULER_PROCESSING_HOUR=9     # Hour (UTC) to process issues
LIMITS_MAX_PRS_PER_DAY=10       # Maximum PRs to create per day
```

## Usage

### One-time Run

```bash
# Discover and process issues (creates PRs)
make run

# Dry run - discover issues without creating PRs
make dry-run
```

### Daemon Mode

```bash
# Run continuously with scheduled jobs
make daemon
```

### Commands

```bash
# Discover issues
make discover

# Show status of issues and PRs
make status

# Show current configuration
make config

# Initialize database
make init
```

### CLI

```bash
# Run directly via CLI
uv run auto-contributor --help
uv run auto-contributor run --daemon
uv run auto-contributor discover --limit 50
uv run auto-contributor status
```

## Architecture

```
auto-contributor/
├── src/auto_contributor/
│   ├── scheduler/      # APScheduler job management
│   ├── finder/         # GitHub issue discovery
│   ├── solver/         # Claude Code integration
│   ├── runner/         # Multi-framework test runner
│   ├── pr/             # Pull request creation
│   ├── monitor/        # CI status monitoring
│   └── models/         # SQLAlchemy database models
```

## Pipeline Flow

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Discover  │────▶│    Solve    │────▶│    Test     │
│   Issues    │     │   (Claude)  │     │  (pytest,   │
└─────────────┘     └─────────────┘     │   npm, etc) │
                                        └──────┬──────┘
                                               │
┌─────────────┐     ┌─────────────┐            │
│   Monitor   │◀────│  Create PR  │◀───────────┘
│     CI      │     │   (gh CLI)  │
└──────┬──────┘     └─────────────┘
       │
       ▼
┌─────────────┐
│  Fix or     │
│  Abandon    │
└─────────────┘
```

## Development

```bash
# Run tests
make test

# Run tests with coverage
make test-cov

# Format code
make fmt

# Lint code
make lint

# Type check
make typecheck

# Run all checks
make check
```

## License

MIT
