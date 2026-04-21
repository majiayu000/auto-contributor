# Auto-Contributor

Automated GitHub contribution bot powered by Claude Code CLI. Discovers issues, creates fixes, and submits PRs automatically.

## Features

- **Claude-Powered Discovery**: Uses Claude Code to intelligently find and analyze GitHub issues
- **PR Verification**: Ensures issues don't already have associated PRs before working on them
- **Recursive Search**: Automatically searches lower-star repos if high-star repos have no available issues
- **Automated Solving**: Claude Code analyzes codebase, implements fixes, and runs tests
- **DCO Signing**: All commits are signed off for Developer Certificate of Origin compliance
- **Web Dashboard**: Real-time monitoring of worker status and progress
- **Retry Logic**: Automatic retry for transient failures

## Requirements

- Go 1.21+
- [Claude Code CLI](https://claude.ai/code) - `claude` command available in PATH
- [GitHub CLI](https://cli.github.com/) - `gh` command authenticated

## Installation

```bash
# Clone the repository
git clone https://github.com/majiayu000/auto-contributor
cd auto-contributor

# Build
go build -o auto-contributor ./cmd/auto-contributor

# Verify
./auto-contributor version
```

## Configuration

Create `~/.auto-contributor/config.yaml`:

```yaml
# GitHub settings
github_email: "your-email@example.com"

# Claude settings - no timeout, let Claude work thoroughly
claude_timeout: 999m
claude_max_retries: 2

# Worker settings
worker_count: 1              # Sequential processing recommended
worker_queue_size: 5
issue_check_interval: 60m

# Language filter
languages:
  - go
  # - python
  # - typescript

# Issue filters
include_labels:
  - good first issue
  - help wanted
  - bug

exclude_labels:
  - wontfix
  - duplicate

# Repo filters
min_repo_stars: 50
max_issue_age_days: 60

# Web UI
web_enabled: true
web_port: 8080

# Logging
log_level: info
```

## Usage

### Continuous Loop Mode (Recommended)

```bash
# Run continuously, ~1 issue per hour
./auto-contributor loop --topic golang --interval 60

# With ultrathink analysis
./auto-contributor loop --topic golang --depth ultrathink
```

### Single Issue

```bash
# Solve a specific issue
./auto-contributor solve --repo owner/repo --issue 123
```

### Smart Discovery Only

```bash
# Discover issues without solving
./auto-contributor discover-smart --topic golang --limit 5

# Save results to file
./auto-contributor discover-smart --topic ai --output issues.json
```

### Commands

| Command | Description |
|---------|-------------|
| `loop` | Continuous discovery and solving (~1 issue/hour) |
| `solve` | Solve a single specific issue |
| `discover` | Basic issue discovery |
| `discover-smart` | Claude-powered intelligent discovery |
| `stats` | Show statistics |
| `status` | Show worker status |

## Architecture

```
auto-contributor/
├── cmd/auto-contributor/     # CLI entry point
│   └── main.go
├── internal/
│   ├── claude/               # Claude Code executor
│   │   └── executor.go       # Solve, validate, commit
│   ├── config/               # Configuration loading
│   ├── db/                   # SQLite database
│   ├── discovery/            # Issue discovery
│   │   └── claude_discovery.go  # Claude-powered discovery
│   ├── github/               # GitHub API client
│   ├── web/                  # Web dashboard
│   └── worker/               # Worker pool
│       └── pool.go           # Parallel issue processing
└── pkg/
    ├── logger/               # Structured logging
    └── models/               # Data models
```

## Pipeline Flow

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Discovery Phase                                  │
├─────────────────────────────────────────────────────────────────────────┤
│  1. Search trending/AI repos (stars > 1000)                             │
│  2. Find issues with "good first issue" / "help wanted" labels          │
│  3. Verify each issue has NO existing PR (mandatory)                    │
│  4. If no issues found, recursively search lower-star repos:            │
│     1000+ → 500-1000 → 100-500 → 50-100                                │
│  5. Use ultrathink to analyze and rank issues                           │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                          Solving Phase                                   │
├─────────────────────────────────────────────────────────────────────────┤
│  1. Clone repository (shallow clone)                                    │
│  2. Evaluate complexity with Claude                                     │
│  3. Create feature branch                                               │
│  4. Claude Code solves the issue:                                       │
│     - Read CONTRIBUTING.md and CI config                                │
│     - Implement minimal fix                                             │
│     - Run tests (go test / npm test / pytest)                          │
│     - Format code                                                       │
│  5. Commit with DCO sign-off                                           │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                           PR Phase                                       │
├─────────────────────────────────────────────────────────────────────────┤
│  1. Fork repository                                                      │
│  2. Push branch to fork                                                  │
│  3. Create pull request with:                                            │
│     - Title: "fix: <issue title>"                                       │
│     - Body: Fixes #N + changed files list                               │
│  4. Save PR to database for tracking                                    │
└─────────────────────────────────────────────────────────────────────────┘
```

## Web Dashboard

Access at `http://localhost:8080` when running with `web_enabled: true`.

Features:
- Real-time worker status
- Discovery progress
- Issue queue
- PR history

## Rate Limiting

Default configuration targets ~1 issue per hour:

| Parameter | Value | Purpose |
|-----------|-------|---------|
| `worker_count` | 1 | Sequential processing |
| `interval` | 60min | Discovery cycle interval |
| `Limit` | 2 | Issues per discovery cycle |
| `claude_timeout` | 999m | No timeout, let Claude work |

## Logs

```bash
# View logs
tail -f ~/.auto-contributor/auto-contributor.log

# Or with structured output
./auto-contributor loop 2>&1 | jq -R 'fromjson?'
```

## Database

SQLite database at `~/.auto-contributor/data.db`:

```bash
# View issues
sqlite3 ~/.auto-contributor/data.db "SELECT * FROM issues ORDER BY created_at DESC LIMIT 10"

# View PRs
sqlite3 ~/.auto-contributor/data.db "SELECT * FROM pull_requests ORDER BY created_at DESC"

# Stats
sqlite3 ~/.auto-contributor/data.db "SELECT status, COUNT(*) FROM issues GROUP BY status"
```

## Troubleshooting

### Claude CLI not found

```bash
# Ensure claude is in PATH
which claude

# Or specify full path in config
export PATH="$PATH:/path/to/claude"
```

### GitHub authentication

```bash
# Login with gh CLI
gh auth login

# Verify
gh auth status
```

### Rate limiting

If hitting GitHub API limits:
- Increase `interval` to 90-120 minutes
- Decrease `Limit` to 1
- Add repos to `exclude_repos` after successful PRs

## License

MIT

## Author

majiayu000 <user@example.com>
