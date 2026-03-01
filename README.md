# greenlight

TUI dashboard for GitHub Actions deployment approvals — directly from the terminal or via macOS notifications.

Designed for the **create-release → release.yml** workflow with environments:

```
Development  →  Staging (approval)  →  Production (approval, tech lead only)
```

## Features

| Feature | Detail |
|---------|--------|
| 📋 **TUI Dashboard** | Real-time pending approvals, auto-refresh every 30s |
| ✅❌ **Approve / Reject** | Keyboard shortcut + confirm dialog |
| 🔭 **Env Filter** | Toggle staging/production visibility (`s` / `p`) |
| 📜 **History Panel** | Local approve/reject log + toggle to GitHub history |
| 🔍 **Changelog Panel** | Commits between the previous and current tag |
| 🏷️ **Release Badge** | `REGULAR` / `HOTFIX` / `PREVIEW` per deployment |
| 👤 **Role-aware** | Engineer → staging only; Tech Lead → staging + production |
| 🔔 **macOS Daemon** | Background polling, push notification banner via terminal-notifier / osascript |
| 🌐 **Open Web** | `o` opens the workflow run page in the browser |

## Stack

| Library | Purpose |
|---------|---------|
| [bubbletea](https://github.com/charmbracelet/bubbletea) | TUI framework |
| [lipgloss](https://github.com/charmbracelet/lipgloss) | Terminal styling |
| [bubbles](https://github.com/charmbracelet/bubbles) | Table, spinner, viewport |
| [cobra](https://github.com/spf13/cobra) | CLI commands |
| `gh` CLI + `gh api` | GitHub API |
| `terminal-notifier` / `osascript` | macOS notifications |

## Prerequisites

```bash
brew install gh
gh auth login

# Optional — for push notification banners (recommended)
brew install terminal-notifier
```

## Install

```bash
git clone https://github.com/adhaniscuber/greenlight
cd greenlight
go build -o greenlight .
mv greenlight ~/go/bin/
```

## Setup

```bash
# 1. Set your role
greenlight config set-role engineer    # staging only
greenlight config set-role techlead    # staging + production

# 2. Add repos to watch (accepts owner/repo or full GitHub URL)
greenlight config add-repo your-org/your-repo
greenlight config add-repo https://github.com/your-org/another-repo

# 3. Check config
greenlight config show
```

Config is stored at: `~/.config/greenlight/config.json`

You can also edit this file directly:

```json
{
  "github_user": "your-username",
  "role": "techlead",
  "env_filter": { "staging": true, "production": true },
  "poll_interval": 600,
  "repos": [
    "your-org/backend-api",
    "your-org/frontend"
  ]
}
```

## Usage

### TUI Mode

```bash
greenlight
# or with a one-off repo override:
greenlight --repo your-org/your-repo

# mock data (no GitHub connection needed):
greenlight --mock
```

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `a` | Approve selected deployment |
| `r` | Reject selected deployment |
| `o` | Open workflow run in browser |
| `tab` | Switch panel (Pending → History → Changelog) |
| `s` | Toggle staging filter |
| `p` | Toggle production filter (tech lead only) |
| `v` | Collapse / expand the Repository section |
| `g` | Toggle history source: local ↔ GitHub (in History panel) |
| `R` | Manual refresh |
| `j/k` `↑/↓` | Navigate rows |
| `q` | Quit |

### TUI Layout

```
╭──────────────────────────────────── Information ╮
│  @adhan  techlead  ⟳ 14:30:01  2 repo(s)        │
╰─────────────────────────────────────────────────╯

╭────────────────────────────────── Need Approval ╮
│  [Pending]  History  Changelog    ●stg  ●prod    │
│                                                  │
│  Tag                  Type     Environment  ...  │
│  v1.7.2               REGULAR  ● staging    ...  │
│  v1.6.3               HOTFIX   ▲ production ...  │
│  v2.0.0-preview       PREVIEW  ● staging    ...  │
╰─────────────────────────────────────────────────╯

╭────────────────────────────────────── Repository ╮
│  your-org/backend-api  your-org/frontend         │
╰─────────────────────────────────────────────────╯

  a approve  r reject  o open web  tab switch  R refresh  q quit
```

### Confirm Dialog

```
╭──────────────────────────────────────────╮
│  ✅ approve deployment?                  │
│                                          │
│  Tag:          v1.7.2                    │
│  Type:         REGULAR                   │
│  Environment:  staging                   │
│  Workflow:     Release Deployment        │
│  Branch:       release/v1.7.x            │
│  Triggered by: wirya                     │
│                                          │
│  [y] Confirm   [n/esc] Cancel            │
╰──────────────────────────────────────────╯
```

### History Panel

```
  ● source: local    [g] load from github

  Time              Action        Tag         Environment   Workflow               By
  14:30 01-Mar-26   ✅ approved   v1.7.1      production    Release Deployment     adhan
  12:15 01-Mar-26   ✅ approved   v1.7.1      staging       Release Deployment     adhan
  10:00 28-Feb-26   ❌ rejected   v1.6.4      staging       Release Deployment     adhan
```

Press `g` to toggle to **GitHub history** (whole team, not just local):

```
  ◎ source: github   [g] back to local

  Time              Action        Tag         Environment   Workflow               By
  14:30 01-Mar-26   ✅ approved   v1.7.1      production    Release Deployment     adhan
  14:30 01-Mar-26   ✅ approved   v1.5.9      production    Release Deployment     sari
```

### Daemon Mode (Background)

```bash
# Run in background
greenlight daemon &

# or with nohup
nohup greenlight daemon > /tmp/greenlight.log 2>&1 &
```

When a pending approval is detected, a push notification banner appears. Clicking it activates the terminal.

```
Greenlight — 2 Pending Approval(s)
[REGULAR] backend-api v1.7.2 → staging (by wirya)
[HOTFIX]  backend-api v1.6.3 → production (by wirya)
```

```bash
# Test notification without GitHub connection
greenlight daemon --mock
```

### Auto-start as LaunchAgent

```bash
# Edit path in plist
sed -i '' 's/YOUR_USERNAME/'"$USER"'/g' com.adhaniscuber.greenlight.plist

# Install
cp com.adhaniscuber.greenlight.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.adhaniscuber.greenlight.plist

# Logs
tail -f /tmp/greenlight.log

# Stop
launchctl unload ~/Library/LaunchAgents/com.adhaniscuber.greenlight.plist
```

### Manage Repos

```bash
# Add (accepts owner/repo or GitHub URL)
greenlight config add-repo your-org/service-a
greenlight config add-repo https://github.com/your-org/service-b

# Remove
greenlight config remove-repo your-org/service-a

# List
greenlight config show
```

You can also edit `~/.config/greenlight/config.json` directly.

### History CLI

```bash
# Local history (your approvals only)
greenlight history
greenlight history --env production --limit 10

# GitHub history (whole team)
greenlight history --source github
greenlight history --source github --env staging --limit 20
```

Local history is stored at: `~/.config/greenlight/history.json`

## Release Type Detection

Derived from the workflow `inputs.release_type` field (set when triggering `release.yml`). Tag suffix is used as a fallback for preview detection.

| Condition | Badge |
|-----------|-------|
| `inputs.release_type == "hotfix"` | `HOTFIX` |
| Tag ends with `-preview` | `PREVIEW` |
| Everything else | `REGULAR` |

Both `HOTFIX` and `REGULAR` deploy through a `release/vX.X.x` branch — the only reliable distinction is the workflow input choice.

## Role Matrix

| Environment | Engineer | Tech Lead |
|-------------|----------|-----------|
| development | auto (no approval needed) | auto |
| staging | ✅ can approve | ✅ can approve |
| production | ⛔ hidden | ✅ can approve |

## GitHub Actions Setup

Make sure environments are configured:

**Settings → Environments → staging:**
- Required reviewers: engineers

**Settings → Environments → production:**
- Required reviewers: tech lead / CTO
- Wait timer: 5 minutes (optional)
