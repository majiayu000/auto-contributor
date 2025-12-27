#!/bin/bash
# Hourly automation script for auto-contributor
# Run one issue fix per hour and check PR status

set -e

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
LOG_DIR="$HOME/.auto-contributor/logs"
LOG_FILE="$LOG_DIR/hourly-$(date +%Y%m%d).log"

# Ensure log directory exists
mkdir -p "$LOG_DIR"

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

log "========== Starting hourly run =========="

cd "$PROJECT_DIR"

# Check if GITHUB_TOKEN is set
if [ -z "$GITHUB_TOKEN" ]; then
    log "ERROR: GITHUB_TOKEN not set"
    exit 1
fi

# Run the auto-contributor for one issue
log "Running auto-contributor discover..."
./auto-contributor discover --limit 1 2>&1 | tee -a "$LOG_FILE"

# Check existing PR status
log "Checking PR status..."
gh pr list --author majiayu000 --state open --json number,title,url,statusCheckRollup 2>&1 | tee -a "$LOG_FILE"

log "========== Hourly run complete =========="
