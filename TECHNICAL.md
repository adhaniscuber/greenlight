# Technical Documentation — greenlight

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Data Flow: Pending Deployments](#data-flow-pending-deployments)
3. [Data Flow: Approve / Reject](#data-flow-approve--reject)
4. [Value Sources](#value-sources)
5. [Release Type Detection](#release-type-detection)
6. [History Mechanism](#history-mechanism)
7. [Config System](#config-system)
8. [Role & Permission System](#role--permission-system)
9. [Daemon & Notification Flow](#daemon--notification-flow)
10. [TUI Architecture (bubbletea)](#tui-architecture-bubbletea)

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    greenlight binary                     │
│                                                         │
│  cmd/root.go          ← cobra CLI entry point           │
│    ├── greenlight          → ui.Start() / ui.StartMock()│
│    ├── greenlight daemon   → daemon.Start()             │
│    ├── greenlight approve  → gh.ApproveDeployment()     │
│    ├── greenlight reject   → gh.RejectDeployment()      │
│    ├── greenlight history  → history.Recent() / gh.GetDeploymentReviews()
│    └── greenlight config   → config.Load() / config.Save()
│                                                         │
│  internal/ui/model.go      ← bubbletea TUI Model        │
│  internal/github/client.go ← all gh CLI calls          │
│  internal/daemon/daemon.go ← background polling loop   │
│  internal/notify/macos.go  ← macOS notifications       │
│  pkg/config/config.go      ← config JSON read/write    │
│  pkg/history/history.go    ← local history JSON        │
└─────────────────────────────────────────────────────────┘
           │
           ▼ shell exec
    ┌─────────────┐
    │   gh CLI    │  ← GitHub API via Personal Access Token
    └─────────────┘
           │
           ▼
    ┌─────────────┐
    │  GitHub API │
    └─────────────┘
```

greenlight **does not call the GitHub REST API directly**. All network calls go through the `gh` CLI (authenticated via `gh auth login`). Results are piped and parsed from JSON/NDJSON using the `-q` flag (jq query).

---

## Data Flow: Pending Deployments

### 1. Trigger

The TUI calls `fetchDeployments()` when:
- `Init()` runs for the first time
- `msgTick` is received (every 30 seconds)
- After an approve/reject action completes
- The user presses `R` (manual refresh)

### 2. GitHub API Call — Workflow Runs

```
gh api "repos/{owner}/{repo}/actions/runs?status=waiting&per_page=30" \
  -q '.workflow_runs[] | {
        id, name, display_title, head_branch, head_sha,
        actor: .actor.login,
        commit: .head_commit.message,
        url: .html_url,
        updated_at,
        input_release_type: (.inputs.release_type // ""),
        input_tag:          (.inputs.tag          // ""),
        input_branch:       (.inputs.branch       // "")
      }'
```

**Parameters:**
- `status=waiting` — only runs awaiting environment approval
- `per_page=30` — maximum 30 runs per request

**Why `inputs.*` fields?**
`release.yml` is always triggered with `--ref main` by `create-release.yml`, so `head_branch` in the runs list API always reports `"main"` regardless of which release branch was actually used. The true values are carried in `workflow_dispatch` inputs:
- `inputs.release_type` — `"regular"` or `"hotfix"` (explicit operator choice)
- `inputs.tag` — the version tag being deployed (e.g. `"v1.7.2"`)
- `inputs.branch` — the true source branch (e.g. `"release/v1.7.x"`)

**Output:** NDJSON — one JSON object per line, one line = one workflow run.

### 3. Per-Run: Fetch Pending Environments

For each workflow run found:

```
gh api "repos/{owner}/{repo}/actions/runs/{run_id}/pending_deployments" \
  -q '.[] | {env: .environment.name, can_approve: .current_user_can_approve}'
```

Each output line = one environment waiting for approval on that run.

### 4. Tag and Branch Resolution

```go
// inputs.tag is the authoritative source; fall back to parsing display_title
tag := raw.InputTag
if tag == "" {
    tag = extractTag(raw.DisplayTitle, raw.HeadBranch)
}

// inputs.branch is the true source branch
branch := raw.InputBranch
if branch == "" {
    branch = raw.HeadBranch
}
```

**extractTag fallback logic:**
```go
func extractTag(displayTitle, branch string) string {
    // Check each word in display_title, take the first starting with "v"
    for _, f := range strings.Fields(displayTitle) {
        if strings.HasPrefix(f, "v") {
            return f  // e.g. "v1.7.2", "v2.0.0-preview"
        }
    }
    // Fallback: extract from branch name
    if strings.HasPrefix(branch, "release/v") {
        return strings.TrimSuffix(branch, ".x")
        // "release/v1.7.x" → "release/v1.7"
    }
    return ""
}
```

### 5. Filter & Assembly

Each environment is filtered against `allowedEnvs` (derived from the user's role). If it passes, one `PendingDeployment` struct is created:

```go
type PendingDeployment struct {
    Repo          string      // "owner/repo" — which repo this belongs to
    RunID         int64       // from workflow run id
    RunURL        string      // from html_url
    Environment   string      // from environment.name
    WaitingSince  time.Time   // from updated_at of the workflow run
    WorkflowName  string      // from name of the workflow run
    Actor         string      // from actor.login (who triggered the workflow)
    Branch        string      // from inputs.branch (fallback: head_branch)
    Tag           string      // from inputs.tag (fallback: extracted from display_title/branch)
    CommitMessage string      // from head_commit.message (first line only)
    ReleaseType   ReleaseType // from workflow input; falls back to tag/branch heuristic
    IsProduction  bool        // true if environment == "production"
}
```

### 6. Multi-Repo Parallel Polling

`GetPendingApprovalsAll` polls multiple repos concurrently:

```go
func GetPendingApprovalsAll(repos []string, allowedEnvs []string) ([]PendingDeployment, error) {
    ch := make(chan result, len(repos))
    for _, r := range repos {
        go func(repo string) {
            items, err := GetPendingApprovals(repo, allowedEnvs)
            ch <- result{items, err}
        }(r)
    }
    // collect and merge...
}
```

Errors from individual repos are surfaced only when all repos fail. Partial results are returned when at least one repo succeeds.

### 7. Raw vs Filtered Deployments

The TUI keeps two separate lists:
- `m.rawDeployments []PendingDeployment` — unfiltered, updated on each fetch
- `m.deployments []PendingDeployment` — filtered subset shown in the table

`applyFilter()` always filters from `rawDeployments`, so toggling the staging/production filter (`s`/`p` keys) works correctly without requiring a re-fetch from GitHub.

### 8. Displayed in Table

```
TUI Pending Table columns:
  Tag          ← PendingDeployment.Tag           (max 20 chars)
  Type         ← renderTypeLabel(ReleaseType)    → REGULAR / HOTFIX / PREVIEW
  Environment  ← renderEnvCell(Environment)      → "● staging" / "▲ production"
  Workflow     ← PendingDeployment.WorkflowName  (truncated 20 chars)
  Branch       ← PendingDeployment.Branch        (truncated 16 chars)
  Actor        ← PendingDeployment.Actor         (truncated 9 chars)
  Wait         ← time.Since(WaitingSince)        → "3m", "1h"
```

**Note:** `renderEnvCell` returns plain text only (no ANSI escape codes). The `bubbles/table` package measures column width in bytes — ANSI codes would inflate the byte count and cause display corruption.

---

## Data Flow: Approve / Reject

### 1. User Flow

```
User presses [a] or [r]
  → check cfg.CanApprove(environment)
  → show confirm dialog (viewConfirm)
  → user presses [y]
  → doAction("approve" / "reject", deployment) dispatched as tea.Cmd
```

### 2. Resolve Environment ID

Before POST, the `environment.id` (integer) must be resolved from the environment name:

```
gh api "repos/{owner}/{repo}/actions/runs/{run_id}/pending_deployments" \
  -q '.[] | select(.environment.name == "{env_name}") | .environment.id'
```

The GitHub API endpoint accepts `environment_ids[]` (integer), not a name string.

### 3. API Call

```
gh api --method POST "repos/{owner}/{repo}/actions/runs/{run_id}/pending_deployments" \
  -f "environment_ids[]={env_id}" \
  -f "state=approved"              ← or "rejected"
  -f "comment=approved via greenlight by {user}"
```

### 4. History Write

After a successful action, the result is immediately written to local history:

```go
history.Append(history.Entry{
    Timestamp:   time.Now(),
    Action:      history.ActionApproved, // or ActionRejected
    RunID:       deployment.RunID,
    Environment: deployment.Environment,
    Workflow:    deployment.WorkflowName,
    Tag:         deployment.Tag,
    Branch:      deployment.Branch,
    Actor:       deployment.Actor,
    ApprovedBy:  cfg.GitHubUser,         // from config
})
```

---

## Value Sources

Complete table of where each value originates:

| TUI Field | API Source | GitHub Field |
|---|---|---|
| Tag | `inputs.tag` (primary); falls back to `display_title` word starting with `v`, then `head_branch` | `workflow_run.inputs.tag`, `workflow_run.display_title`, `workflow_run.head_branch` |
| Type (REGULAR/HOTFIX/PREVIEW) | Derived from `inputs.release_type` + tag suffix (see below) | `workflow_run.inputs.release_type` |
| Environment | `pending_deployments[].environment.name` | `environment.name` |
| Workflow | `workflow_run.name` | `name` |
| Branch | `inputs.branch` (primary); falls back to `head_branch` | `workflow_run.inputs.branch`, `workflow_run.head_branch` |
| Actor | `workflow_run.actor.login` | `actor.login` |
| Wait | `time.Since(workflow_run.updated_at)` | `updated_at` |
| Commit message | `workflow_run.head_commit.message` (first line only) | `head_commit.message` |
| Run URL | `workflow_run.html_url` | `html_url` |
| Reviewer (History) | `approvals[].user.login` | `user.login` |
| Reviewed at (History) | `approvals[].submitted_at` | `submitted_at` |

---

## Release Type Detection

Logic follows the actual `create-release.yml` / `release.yml` workflow design:

```go
func detectReleaseType(tag, inputType string) ReleaseType {
    // 1. Explicit hotfix input — cherry-picked to release/vX.X.x branch
    if strings.ToLower(inputType) == "hotfix" {
        return ReleaseHotfix
    }
    // 2. Preview tag suffix — regular deploy from main with pre-release tag
    if strings.HasSuffix(tag, "-preview") {
        return ReleasePreview
    }
    // 3. Everything else — official release from release/vX.X.x
    return ReleaseRegular
}
```

**Key design decisions:**
- `inputs.release_type` has only two choices in the workflow: `"regular"` and `"hotfix"`. Preview is **not** a separate release type — it is a `regular` dispatch where the tag carries a `-preview` suffix.
- Both HOTFIX and REGULAR are deployed through a `release/vX.X.x` branch (hotfixes are cherry-picked there). The branch name alone cannot distinguish them — only the workflow input choice is reliable.
- `release.yml` is always triggered with `--ref main`, so `head_branch` in the API is always `"main"` and cannot be used for classification.

| Example Tag | inputs.release_type | Result |
|---|---|---|
| `v1.7.2` | `regular` | `REGULAR` |
| `v1.6.3` | `hotfix` | `HOTFIX` |
| `v2.0.0-preview` | `regular` | `PREVIEW` |
| `v1.7.2` | _(empty)_ | `REGULAR` (fallback) |

---

## History Mechanism

There are **two history sources** that can be toggled with `g`:

### 1. Local History (Default)

**Storage:** `~/.config/greenlight/history.json`

**Format:**
```json
[
  {
    "timestamp": "2026-03-01T14:30:00Z",
    "action": "approved",
    "run_id": 12345678,
    "environment": "production",
    "workflow": "Release Deployment",
    "tag": "v1.7.2",
    "branch": "release/v1.7.x",
    "actor": "wirya",
    "approved_by": "adhaniscuber"
  }
]
```

**When written:** Every time an approve/reject succeeds, `history.Append()` is called. New entries are prepended (newest first). The file is capped at 200 entries.

**Error propagation:** `history.Append()` returns an error if loading the existing file fails. `history.Recent()` returns `([]Entry, error)` — callers must check the error.

**Limitations:**
- Only records actions performed on the current machine
- Not shared across the team
- Actions taken by teammates on their own machines do not appear

### 2. GitHub History (Toggle `g`)

**Source:** GitHub Deployment Reviews API

**API calls:**

```
# Step 1: fetch recent workflow runs
gh api "repos/{owner}/{repo}/actions/runs?per_page={limit*3}" \
  -q '.workflow_runs[] | {id, name, display_title, head_branch, actor: .actor.login, updated_at}'

# Step 2: for each run, fetch review actions
gh api "repos/{owner}/{repo}/actions/runs/{run_id}/approvals" \
  -q '.[] | {state, user: .user.login, submitted_at, env: (.environments[0].name // "")}'
```

**Advantages:**
- Covers the entire team's actions (whoever approved/rejected)
- Data lives on GitHub, not tied to a local machine
- Real-time, matches current GitHub state

**Limitations:**
- Only available for runs that already have a review action (unreviewed runs do not appear)
- Requires a network call to GitHub every time the toggle is pressed

### Comparison

| Aspect | Local | GitHub |
|--------|-------|--------|
| Source | `~/.config/greenlight/history.json` | GitHub REST API via `gh` |
| Scope | Current user's actions only | Whole team |
| Offline | ✅ Available | ❌ Requires internet |
| Real-time | Immediate after action | Each toggle (no auto-refresh) |
| Data retention | Forever (JSON file) | While GitHub retains workflow runs |

---

## Config System

**File:** `~/.config/greenlight/config.json`

**Struct:**
```go
type Config struct {
    GitHubUser   string    `json:"github_user"`
    Role         Role      `json:"role"`
    EnvFilter    EnvFilter `json:"env_filter"`
    PollInterval int       `json:"poll_interval"`
    DefaultRepo  string    `json:"default_repo,omitempty"` // legacy single-repo, kept for compat
    Repos        []string  `json:"repos,omitempty"`        // multi-repo list (preferred)
}
```

**Default values on first run:**
- `Role`: `"engineer"`
- `PollInterval`: `600` (10 minutes for daemon)
- `EnvFilter`: `{staging: true, production: false}`
- `GitHubUser`: auto-detected from `gh api user -q .login` when TUI first starts
- `Repos`: empty — configure with `greenlight config add-repo`

**`EnvFilter` after `set-role`:**
- `set-role engineer` → `{staging: true, production: false}`
- `set-role techlead` → `{staging: true, production: true}`

**Multi-repo support:**
- `cfg.Repos []string` — preferred, supports multiple repositories
- `cfg.DefaultRepo string` — legacy single-repo field, still read for backwards compatibility
- `cfg.AllRepos()` returns `Repos` if non-empty, otherwise falls back to `[]string{DefaultRepo}`

**Repo input formats (CLI):**
`add-repo` and `remove-repo` accept both `owner/repo` and full GitHub URLs:
```
greenlight config add-repo your-org/service-a
greenlight config add-repo https://github.com/your-org/service-a
greenlight config add-repo https://github.com/your-org/service-a/tree/main
```
All forms are normalized to `owner/repo` before storing.

**Manual editing:**
The config file can be edited directly at `~/.config/greenlight/config.json`. Changes take effect on the next TUI start or daemon restart.

**First-run error propagation:**
If the default config cannot be saved to disk on first run, `Load()` returns both the default config and the error — so the caller can decide whether to surface it.

---

## Role & Permission System

```go
type Role string
const (
    RoleEngineer Role = "engineer"
    RoleTechLead Role = "techlead"
)

func (c Config) AllowedEnvironments() []string {
    var envs []string
    if c.EnvFilter.Staging {
        envs = append(envs, "staging")
    }
    if c.EnvFilter.Production || c.Role == RoleTechLead {
        envs = append(envs, "production")
    }
    return envs
}

func (c Config) CanApprove(env string) bool {
    switch strings.ToLower(env) {
    case "staging":
        return true
    case "production":
        return c.Role == RoleTechLead
    default:
        // Unknown environments are not approvable — deny by default.
        return false
    }
}
```

**In the TUI:**
- `AllowedEnvironments()` is used as a filter when fetching from GitHub — engineers never see production deployments at all
- `CanApprove()` is checked when the user presses `a`/`r` — shows an error if the user attempts to approve an environment they don't have access to
- The `s`/`p` filter toggles are constrained by role — engineers cannot toggle production on
- `CanApprove` returns `false` for any unknown environment name (safe default)

**Role matrix:**

| Environment | Engineer | Tech Lead |
|-------------|----------|-----------|
| development | auto (no approval needed) | auto |
| staging | ✅ can approve | ✅ can approve |
| production | ⛔ hidden | ✅ can approve |

---

## Daemon & Notification Flow

### Poll Loop

```
daemon.Start(repos, interval)
  │
  ├── load config → get AllowedEnvironments()
  ├── poll immediately (first run)
  └── loop every {interval} seconds:
        │
        ├── gh.GetPendingApprovalsAll(repos, allowedEnvs)
        │     → same flow as TUI fetch
        │
        ├── acquire seenMu.Lock()
        │     pruneSeen()     ← evict entries older than 24h
        │     build newCandidates (deployments not yet in seen map)
        │   seenMu.Unlock()
        │
        ├── if no new candidates → skip
        │
        ├── notify.SendBulkNotification(newItems)
        │     → if error: log, do NOT mark seen (will retry next poll)
        │
        └── acquire seenMu.Lock()
              mark all candidates as seen with current timestamp
            seenMu.Unlock()
```

**Key design decisions:**
- **Mark seen only after notification succeeds.** If `SendBulkNotification` returns an error, the keys are not added to the `seen` map, so the next poll will retry the notification.
- **Thread-safe seen map.** `seenMu sync.Mutex` guards all reads and writes to the `seen` map. Both the read section (pruning + candidate filtering) and the write section (marking seen) are wrapped in separate lock/unlock pairs.
- **24h TTL eviction.** `pruneSeen()` removes entries older than 24 hours, so a deployment that was re-queued after rejection or expiry will generate a new notification.

### Notification System

**Primary: `terminal-notifier`** (recommended, install via `brew install terminal-notifier`)

```go
args := []string{"-title", title, "-message", body, "-sound", "default"}
if bundleID := preferredTerminalBundleID(); bundleID != "" {
    args = append(args, "-activate", bundleID)
}
exec.Command("/opt/homebrew/.../terminal-notifier", args...).Run()
```

The `-activate` flag makes the notification banner open the correct terminal app when clicked. The bundle ID is detected automatically:
1. Check `$TERM_PROGRAM` env var (set by most modern terminals when interactive)
2. If not set (LaunchAgent / daemon context), scan known app paths in `/Applications`
3. Supported terminals: Ghostty, iTerm2, Terminal.app, WezTerm, VS Code, Hyper, Alacritty

Binary search order:
1. `/opt/homebrew/opt/terminal-notifier/bin/terminal-notifier` (Apple Silicon brew)
2. `/usr/local/opt/terminal-notifier/bin/terminal-notifier` (Intel brew)
3. `terminal-notifier` (in `$PATH`)

**Fallback: `osascript`** (no install required, but clicking opens Script Editor)

```applescript
display notification "..." with title "..." sound name "default"
```

**AppleScript string escaping (`asStr`):**

AppleScript uses `& quote &` for literal double-quotes and `& return &` for newlines. Backslash and apostrophe need no escaping in AppleScript string literals.

```go
func asStr(s string) string {
    s = strings.ReplaceAll(s, `"`, `" & quote & "`)
    s = strings.ReplaceAll(s, "\n", `" & return & "`)
    return `"` + s + `"`
}
```

This handles commit messages and tags that may contain any character.

### Auto-start as LaunchAgent

The daemon is designed to run as a macOS LaunchAgent via `com.adhaniscuber.greenlight.plist`, started automatically at login. In this context `$TERM_PROGRAM` is not set, so the notification system falls back to scanning `/Applications` for installed terminals.

---

## TUI Architecture (bubbletea)

Greenlight follows **The Elm Architecture** via bubbletea:

### Init / Update / View

```
Init() → tea.Cmd
  └── fetchDeployments() + refreshHistory() + spinner.Tick + tickEvery(30s)

Update(msg tea.Msg) → (Model, tea.Cmd)
  ├── tea.WindowSizeMsg    → update width/height, resize table heights
  ├── msgTick              → trigger fetchDeployments()
  ├── spinner.TickMsg      → update spinner animation
  ├── msgDeployments       → store rawDeployments, call applyFilter(), refresh history
  ├── msgChangelog         → update changelogVP content
  ├── []history.Entry      → update historyTable rows (local)
  ├── msgGitHubHistory     → update historyTable rows (github)
  ├── msgActionDone        → reset state, trigger refresh, write history
  └── tea.KeyMsg           → handleKey() → approve/reject/filter/tab/quit

View() → string
  ├── (1 blank line padding)
  ├── Title section   (centered "🚦 greenlight" + subtitle on separate line)
  ├── Need Approval box:
  │     Tab bar   (Pending / History / Changelog + env filter badges)
  │     Status    (pending count / success / error message)
  │     Main panel:
  │       panelPending   → pendingTable.View()
  │       panelHistory   → historyTable.View() + source hint
  │       panelChangelog → changelogVP.View()
  │       viewConfirm    → renderConfirm() dialog (overlaid)
  └── Repository section  (repo pills, collapsible with `v`)
      Footer              (key hints)
```

### View State Machine

```
viewList ◄──── (n / esc / msgActionDone)
   │
   │ (a/r pressed, role check passes)
   ▼
viewConfirm
   │
   │ (y pressed)
   ▼
viewAction ──── msgActionDone ────► viewList
```

### Panel Navigation

```
[tab]
Pending (0) → History (1) → Changelog (2) → Pending (0) → ...
```

### Key Model Fields

```go
type Model struct {
    // Data
    rawDeployments []gh.PendingDeployment  // unfiltered, from last fetch
    deployments    []gh.PendingDeployment  // filtered, shown in table

    // UI state
    viewState     viewState   // viewList | viewConfirm | viewAction
    activePanel   int         // 0=pending, 1=history, 2=changelog
    selectedIdx   int         // cursor position in pending table
    historySource histSrc     // histLocal | histGitHub

    // Tables / viewport
    pendingTable  table.Model
    historyTable  table.Model
    changelogVP   viewport.Model

    // Config
    cfg           config.Config
    repos         []string
    reposCollapsed bool
}
```

### Filter Logic

```go
func (m *Model) applyFilter() {
    m.deployments = m.filterDeployments(m.rawDeployments)
    m.pendingTable.SetRows(m.toTableRows(m.deployments))
}

func (m Model) filterDeployments(items []gh.PendingDeployment) []gh.PendingDeployment {
    var out []gh.PendingDeployment
    for _, d := range items {
        switch strings.ToLower(d.Environment) {
        case "staging":
            if m.cfg.EnvFilter.Staging {
                out = append(out, d)
            }
        case "production":
            if m.cfg.EnvFilter.Production {
                out = append(out, d)
            }
        }
    }
    return out
}
```

`applyFilter` always reads from `m.rawDeployments`. This ensures the filter toggle (`s`/`p`) works correctly — toggling staging back on restores all staging items without requiring a GitHub re-fetch.

### Title Rendering

```go
func (m Model) renderTitleSection() string {
    center := lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center)
    title := lipgloss.NewStyle().
        Foreground(colorAccent).Bold(true).Padding(0, 2).
        Render("🚦 greenlight")
    sub := lipgloss.NewStyle().
        Foreground(colorSubtle).
        Render("GitHub Actions deployment approvals")
    return center.Render(title) + "\n" + center.Render(sub)
}
```

The title and subtitle are each centered independently using `lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center)`.
