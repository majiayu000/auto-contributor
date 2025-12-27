# Auto-Contributor Scripts

## Hourly Automation Setup

### Option 1: Using launchd (macOS)

1. Copy the plist file to LaunchAgents:
```bash
cp scripts/com.majiayu000.auto-contributor.plist ~/Library/LaunchAgents/
```

2. Set GITHUB_TOKEN in your shell profile (~/.zshrc or ~/.bash_profile):
```bash
export GITHUB_TOKEN="your_github_token_here"
```

3. Load the launch agent:
```bash
launchctl load ~/Library/LaunchAgents/com.majiayu000.auto-contributor.plist
```

4. To stop:
```bash
launchctl unload ~/Library/LaunchAgents/com.majiayu000.auto-contributor.plist
```

### Option 2: Using cron

Add to crontab (`crontab -e`):
```
0 * * * * GITHUB_TOKEN="your_token" /Users/lifcc/Desktop/code/auto-contributor/scripts/hourly-run.sh
```

### Option 3: Manual Loop

Run the built-in loop command with 60-minute intervals:
```bash
./auto-contributor loop --interval 60
```

## Logs

Logs are stored in `~/.auto-contributor/logs/`:
- `hourly-YYYYMMDD.log` - Daily activity log
- `launchd-stdout.log` - Standard output from launchd
- `launchd-stderr.log` - Error output from launchd
