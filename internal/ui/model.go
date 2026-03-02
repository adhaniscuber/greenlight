package ui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	gh "github.com/wirya/greenlight/internal/github"
	"github.com/wirya/greenlight/pkg/config"
	"github.com/wirya/greenlight/pkg/history"
)

// appVersion is injected by cmd.SetVersion at startup.
var appVersion = "dev"

// SetVersion is called from cmd.SetVersion to propagate the build version into the TUI.
func SetVersion(v string) { appVersion = v }

// ── Palette ───────────────────────────────────────────────────────────────────

var (
	colorAccent     = lipgloss.Color("86")  // teal
	colorProduction = lipgloss.Color("196") // red
	colorStaging    = lipgloss.Color("214") // amber
	colorPreview    = lipgloss.Color("63")  // purple
	colorHotfix     = lipgloss.Color("196") // red
	colorMuted      = lipgloss.Color("240")
	colorSubtle     = lipgloss.Color("244")
	colorSuccess    = lipgloss.Color("82")
	colorBorder     = lipgloss.Color("238")
	colorCyan       = lipgloss.Color("51")  // bright cyan — section labels
	colorPink       = lipgloss.Color("205") // hot pink — active panel border

	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent).
			Background(lipgloss.Color("235")).
			Padding(0, 2)

	styleMeta = lipgloss.NewStyle().Foreground(colorSubtle).Padding(0, 1)

	styleHelp = lipgloss.NewStyle().Foreground(colorMuted)

	styleSuccess = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleError   = lipgloss.NewStyle().Foreground(colorProduction).Bold(true)
	styleWarn    = lipgloss.NewStyle().Foreground(colorStaging).Bold(true)

	stylePanelActive = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				Padding(0, 1)

	stylePanelInactive = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorBorder).
				Padding(0, 1)

	styleTag = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	styleEnvProduction = lipgloss.NewStyle().
				Foreground(colorProduction).
				Bold(true)

	styleEnvStaging = lipgloss.NewStyle().
			Foreground(colorStaging)

	styleBadge = func(color lipgloss.Color) lipgloss.Style {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(color).
			Padding(0, 1).
			Bold(true)
	}
)

// ── Panels ────────────────────────────────────────────────────────────────────

type panel int

const (
	panelPending panel = iota
	panelHistory
	panelChangelog
	panelCount
)

var panelNames = [panelCount]string{"Pending", "History", "Changelog"}

// ── App state ─────────────────────────────────────────────────────────────────

type viewState int

const (
	viewList    viewState = iota // main list
	viewConfirm                  // approve/reject confirm dialog
	viewAction                   // waiting for API response
)

// ── Messages ──────────────────────────────────────────────────────────────────

type msgDeployments struct {
	items    []gh.PendingDeployment
	warnings []string // per-repo soft errors, e.g. "voila-ggl-router: not found (404)"
	err      error
}

type msgChangelog struct {
	entries []gh.ChangelogEntry
	fromTag string
	toTag   string
	err     error
}

type msgActionDone struct {
	action     string
	deployment gh.PendingDeployment
	err        error
}

type msgGitHubHistory struct {
	reviews []gh.DeploymentReview
	err     error
}

type msgTick time.Time

// ── Keys ──────────────────────────────────────────────────────────────────────

type keys struct {
	Approve       key.Binding
	Reject        key.Binding
	Refresh       key.Binding
	Quit          key.Binding
	Up            key.Binding
	Down          key.Binding
	Open          key.Binding
	Tab           key.Binding
	FilterStg     key.Binding
	FilterPrd     key.Binding
	Yes           key.Binding
	No            key.Binding
	GitHubHistory key.Binding
	ToggleRepos   key.Binding
}

var kb = keys{
	Approve:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "approve")),
	Reject:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reject")),
	Refresh:       key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "refresh")),
	Quit:          key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	Up:            key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:          key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Open:          key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open web")),
	Tab:           key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch panel")),
	FilterStg:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "toggle staging")),
	FilterPrd:     key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "toggle prod")),
	Yes:           key.NewBinding(key.WithKeys("y", "Y")),
	No:            key.NewBinding(key.WithKeys("n", "N", "esc")),
	GitHubHistory: key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "github history")),
	ToggleRepos:   key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "toggle repos")),
}

// ── Model ─────────────────────────────────────────────────────────────────────

type Model struct {
	cfg   config.Config
	repos []string // all repos being watched

	// data
	rawDeployments []gh.PendingDeployment // unfiltered, from last API fetch
	deployments    []gh.PendingDeployment // filtered view shown in table
	histEntries    []history.Entry

	// ui state
	activePanel   panel
	viewState     viewState
	confirmAction string // "approve" | "reject"
	loading       bool

	// components
	pendingTable table.Model
	historyTable table.Model
	changelogVP  viewport.Model
	spin         spinner.Model

	// status bar
	statusMsg  string
	statusKind string // "success" | "error" | "warn"

	// env filter (can be toggled at runtime)
	showStaging    bool
	showProduction bool

	// changelog state
	changelogRunning bool
	changelogEntries []gh.ChangelogEntry
	changelogTitle   string

	// layout
	width  int
	height int

	lastRefresh time.Time

	// derived
	multiRepo      bool // true when watching more than one repo
	reposCollapsed bool // Section 4 visibility toggle

	// mock mode
	mockMode           bool
	mockPending        []gh.PendingDeployment
	mockHistoryEntries []history.Entry

	// github history mode (History panel)
	githubHistoryMode    bool
	githubHistoryLoading bool
}

func newModel(cfg config.Config, repos []string, mockMode bool) Model {
	// Role determines initial env filter
	showStaging := cfg.EnvFilter.Staging
	showProd := cfg.Role == config.RoleTechLead || cfg.EnvFilter.Production

	multiRepo := len(repos) > 1

	// Spinner
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorAccent)

	// Pending table columns — add Repo column when watching multiple repos
	pendingCols := []table.Column{}
	if multiRepo {
		pendingCols = append(pendingCols, table.Column{Title: "Repo", Width: 14})
	}
	pendingCols = append(pendingCols,
		table.Column{Title: "Tag", Width: 20},
		table.Column{Title: "Type", Width: 9},
		table.Column{Title: "Environment", Width: 13},
		table.Column{Title: "Workflow", Width: 22},
		table.Column{Title: "Branch", Width: 18},
		table.Column{Title: "Actor", Width: 10},
		table.Column{Title: "Wait", Width: 6},
	)
	pt := newStyledTable(pendingCols, 10)

	// History table columns
	historyCols := []table.Column{
		{Title: "Time", Width: 16},
		{Title: "Action", Width: 12},
		{Title: "Tag", Width: 18},
		{Title: "Environment", Width: 12},
		{Title: "Workflow", Width: 22},
		{Title: "By", Width: 14},
	}
	ht := newStyledTable(historyCols, 10)

	// Changelog viewport
	cvp := viewport.New(80, 15)

	m := Model{
		cfg:            cfg,
		repos:          repos,
		multiRepo:      multiRepo,
		activePanel:    panelPending,
		viewState:      viewList,
		spin:           sp,
		pendingTable:   pt,
		historyTable:   ht,
		changelogVP:    cvp,
		showStaging:    showStaging,
		showProduction: showProd,
		loading:        true,
		mockMode:       mockMode,
	}
	if mockMode {
		m.mockPending = buildMockDeployments()
		m.mockHistoryEntries = buildMockHistory()
	}
	return m
}

func newStyledTable(cols []table.Column, height int) table.Model {
	t := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(height),
	)
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(colorBorder).
		BorderBottom(true).
		Bold(true).
		Foreground(colorAccent)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(false)
	t.SetStyles(s)
	return t
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spin.Tick,
		m.fetchDeployments(),
		m.refreshHistory(),
		tickEvery(30*time.Second),
	)
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		tableH := m.calcTableHeight()
		m.pendingTable.SetHeight(tableH)
		m.historyTable.SetHeight(tableH)
		m.changelogVP.Width = msg.Width - 6
		m.changelogVP.Height = tableH

	case msgTick:
		cmds = append(cmds, m.fetchDeployments(), tickEvery(30*time.Second))

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		cmds = append(cmds, cmd)

	case msgDeployments:
		m.loading = false
		m.lastRefresh = time.Now()
		if msg.err != nil {
			// All repos failed — hard error, nothing to show.
			m.setStatus("error", fmt.Sprintf("Error: %v", msg.err))
		} else {
			// At least one repo responded. Update the table regardless of warnings.
			m.rawDeployments = msg.items
			m.applyFilter()

			switch {
			case len(msg.warnings) > 0 && len(m.deployments) == 0:
				// No pending items AND some repos failed — show the warning prominently.
				m.setStatus("warn", fmt.Sprintf("⚠ %s", msg.warnings[0]))
			case len(msg.warnings) > 0:
				// Some pending items + some repos failed — show count with a soft note.
				m.setStatus("warn", fmt.Sprintf("%d pending · ⚠ %d repo error(s)", len(m.deployments), len(msg.warnings)))
			case len(m.deployments) == 0:
				m.setStatus("success", "No pending deployments 🎉")
			default:
				m.setStatus("warn", fmt.Sprintf("%d pending deployment(s)", len(m.deployments)))
			}
		}

	case msgChangelog:
		m.changelogRunning = false
		if msg.err != nil || len(msg.entries) == 0 {
			m.changelogVP.SetContent(styleHelp.Render("  No changelog available."))
		} else {
			m.changelogEntries = msg.entries
			m.changelogTitle = fmt.Sprintf("Changelog  %s → %s", msg.fromTag, msg.toTag)
			m.changelogVP.SetContent(renderChangelog(msg.entries))
		}

	case []history.Entry:
		m.applyHistoryTable(msg)

	case msgGitHubHistory:
		m.githubHistoryLoading = false
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("GitHub: %v", msg.err))
			m.githubHistoryMode = false
		} else {
			m.applyGitHubHistoryTable(msg.reviews)
		}

	case msgActionDone:
		m.viewState = viewList
		m.loading = true
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("❌ %s failed: %v", msg.action, msg.err))
		} else {
			m.setStatus("success", fmt.Sprintf("✅ Successfully %sd", msg.action))
			if m.mockMode {
				m.mockPending = removePending(m.mockPending, msg.deployment.RunID, msg.deployment.Environment)
				act := history.ActionApproved
				if msg.action == "reject" {
					act = history.ActionRejected
				}
				entry := history.Entry{
					Timestamp:   time.Now(),
					Action:      act,
					RunID:       msg.deployment.RunID,
					Environment: msg.deployment.Environment,
					Workflow:    msg.deployment.WorkflowName,
					Tag:         msg.deployment.Tag,
					Branch:      msg.deployment.Branch,
					Actor:       msg.deployment.Actor,
					ApprovedBy:  m.cfg.GitHubUser,
				}
				m.mockHistoryEntries = append([]history.Entry{entry}, m.mockHistoryEntries...)
			}
		}
		cmds = append(cmds, m.spin.Tick, m.fetchDeployments(), m.refreshHistory())

	case tea.KeyMsg:
		cmds = append(cmds, m.handleKey(msg, &cmds)...)
	}

	// Forward to active component
	switch m.activePanel {
	case panelPending:
		var cmd tea.Cmd
		m.pendingTable, cmd = m.pendingTable.Update(msg)
		cmds = append(cmds, cmd)
		// Load changelog whenever cursor moves
		if _, ok := msg.(tea.KeyMsg); ok {
			cmds = append(cmds, m.loadChangelog())
		}
	case panelHistory:
		var cmd tea.Cmd
		m.historyTable, cmd = m.historyTable.Update(msg)
		cmds = append(cmds, cmd)
	case panelChangelog:
		var cmd tea.Cmd
		m.changelogVP, cmd = m.changelogVP.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleKey(msg tea.KeyMsg, _ *[]tea.Cmd) []tea.Cmd {
	var cmds []tea.Cmd

	// Global
	if key.Matches(msg, kb.Quit) {
		cmds = append(cmds, tea.Quit)
		return cmds
	}

	// Confirm dialog
	if m.viewState == viewConfirm {
		switch {
		case key.Matches(msg, kb.Yes):
			idx := m.pendingTable.Cursor()
			if idx < len(m.deployments) {
				d := m.deployments[idx]
				m.viewState = viewAction
				m.loading = true
				cmds = append(cmds, m.spin.Tick, m.doAction(m.confirmAction, d))
			}
		case key.Matches(msg, kb.No):
			m.viewState = viewList
			m.confirmAction = ""
		}
		return cmds
	}

	// Normal mode
	switch {
	case key.Matches(msg, kb.Tab):
		m.activePanel = (m.activePanel + 1) % panelCount
		// Unfocus table when leaving it
		m.pendingTable.Blur()
		m.historyTable.Blur()
		if m.activePanel == panelPending {
			m.pendingTable.Focus()
		} else if m.activePanel == panelHistory {
			m.historyTable.Focus()
		}

	case key.Matches(msg, kb.Refresh):
		m.loading = true
		cmds = append(cmds, m.spin.Tick, m.fetchDeployments(), m.refreshHistory())

	case key.Matches(msg, kb.FilterStg):
		m.showStaging = !m.showStaging
		m.applyFilter()

	case key.Matches(msg, kb.FilterPrd):
		if m.cfg.Role == config.RoleTechLead {
			m.showProduction = !m.showProduction
			m.applyFilter()
		}

	case key.Matches(msg, kb.Open):
		if m.activePanel == panelPending && len(m.deployments) > 0 {
			idx := m.pendingTable.Cursor()
			if idx < len(m.deployments) {
				cmds = append(cmds, openURL(m.deployments[idx].RunURL))
			}
		}

	case key.Matches(msg, kb.Approve):
		if m.activePanel == panelPending && len(m.deployments) > 0 {
			idx := m.pendingTable.Cursor()
			if idx < len(m.deployments) {
				d := m.deployments[idx]
				if m.cfg.CanApprove(strings.ToLower(d.Environment)) {
					m.confirmAction = "approve"
					m.viewState = viewConfirm
				} else {
					m.setStatus("error", "⛔ Your role cannot approve "+d.Environment)
				}
			}
		}

	case key.Matches(msg, kb.GitHubHistory):
		if m.activePanel == panelHistory {
			if m.githubHistoryMode {
				m.githubHistoryMode = false
				m.applyHistoryTable(m.histEntries) // restore local
			} else {
				m.githubHistoryMode = true
				m.githubHistoryLoading = true
				cmds = append(cmds, m.fetchGitHubHistory())
			}
		}

	case key.Matches(msg, kb.ToggleRepos):
		m.reposCollapsed = !m.reposCollapsed
		tableH := m.calcTableHeight()
		m.pendingTable.SetHeight(tableH)
		m.historyTable.SetHeight(tableH)
		m.changelogVP.Height = tableH

	case key.Matches(msg, kb.Reject):
		if m.activePanel == panelPending && len(m.deployments) > 0 {
			idx := m.pendingTable.Cursor()
			if idx < len(m.deployments) {
				d := m.deployments[idx]
				if m.cfg.CanApprove(strings.ToLower(d.Environment)) {
					m.confirmAction = "reject"
					m.viewState = viewConfirm
				} else {
					m.setStatus("error", "⛔ Your role cannot reject "+d.Environment)
				}
			}
		}
	}

	return cmds
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	var b strings.Builder

	// Section 1 — title (no border)
	b.WriteString("\n")
	b.WriteString(m.renderTitleSection())
	b.WriteString("\n\n")

	// Section 2 — Information
	b.WriteString(renderTitledBox("Information", m.renderInfoContent(), m.width, colorBorder, colorCyan))
	b.WriteString("\n\n")

	// Section 3 — Need Approval (pink border = main active panel)
	b.WriteString(renderTitledBox("Need Approval", m.renderNeedApprovalContent(), m.width, colorPink, colorCyan))
	b.WriteString("\n\n")

	// Section 4 — Repository (collapsible)
	if m.reposCollapsed {
		hint := lipgloss.NewStyle().Foreground(colorCyan).Bold(true).Render("Repository")
		lineLen := max(0, m.width-lipgloss.Width(hint)-12)
		line := lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("─", lineLen))
		show := styleHelp.Render("[v] show")
		b.WriteString(" " + hint + " " + line + " " + show + "\n")
	} else {
		b.WriteString(renderTitledBox("Repository", m.renderRepoContent(), m.width, colorBorder, colorCyan))
		b.WriteString("\n\n")
	}

	// Footer
	b.WriteString(m.renderHelp())
	return b.String()
}

// renderTitledBox draws a rounded panel with title embedded in the top-right border.
// Layout: ╭─────── Title ╮  │ content │  ╰──────────────╯
func renderTitledBox(title, content string, w int, borderCol, titleCol lipgloss.Color) string {
	bc := lipgloss.NewStyle().Foreground(borderCol)
	tc := lipgloss.NewStyle().Foreground(titleCol).Bold(true)

	titleStr := " " + tc.Render(title) + " "
	titleVisW := lipgloss.Width(titleStr)
	lineLen := max(1, w-titleVisW-2)

	top := bc.Render("╭") + bc.Render(strings.Repeat("─", lineLen)) + titleStr + bc.Render("╮")
	bottom := bc.Render("╰") + bc.Render(strings.Repeat("─", w-2)) + bc.Render("╯")

	innerW := max(1, w-4) // w - 2 (borders) - 2 (padding)
	lines := strings.Split(content, "\n")
	// Strip trailing empty line produced by content ending with \n
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var sb strings.Builder
	sb.WriteString(top + "\n")
	for _, line := range lines {
		pad := max(0, innerW-lipgloss.Width(line))
		sb.WriteString(bc.Render("│") + " " + line + strings.Repeat(" ", pad) + " " + bc.Render("│") + "\n")
	}
	sb.WriteString(bottom)
	return sb.String()
}

// renderTitleSection — Section 1: brand title, no border. Centered.
func (m Model) renderTitleSection() string {
	center := lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center)
	// ASCII art — backtick chars split via concatenation; backslashes escaped.
	const bq = "`"
	logo := "\n" +
		"                                 ___       __    __\n" +
		"     ____ _________  ___  ____  / (_)___ _/ /_  / /_\n" +
		"🔴  / __ " + bq + "/ ___/ _ \\/ _ \\/ __ \\/ / / __ " + bq + "/ __ \\/ __/\n" +
		"🟡 / /_/ / /  /  __/  __/ / / / / / /_/ / / / / /_\n" +
		"🟢 \\__, /_/   \\___/\\___/_/ /_/_/_/\\__, /_/ /_/\\__/\n" +
		"/____/                         /____/             "
	title := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Padding(0, 2).Render(logo)
	sub := lipgloss.NewStyle().Foreground(colorSubtle).Render("GitHub Actions deployment approvals")
	return center.Render(title) + "\n\n" + center.Render(sub)
}

// renderInfoContent — Section 2 content: user / role / refresh + pending summary.
func (m Model) renderInfoContent() string {
	userStr := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("@" + m.cfg.GitHubUser)
	roleStr := lipgloss.NewStyle().Foreground(colorMuted).Render("[" + string(m.cfg.Role) + "]")
	refreshStr := lipgloss.NewStyle().Foreground(colorSubtle).Render("⟳ " + m.lastRefresh.Format("15:04:05"))
	metaLine := userStr + "  " + roleStr + "  " + refreshStr

	var summaryLine string
	if m.loading {
		summaryLine = fmt.Sprintf("%s loading...", m.spin.View())
	} else if m.statusMsg != "" {
		summaryLine = m.renderStatus()
	} else {
		var prodCount, stagCount int
		for _, d := range m.deployments {
			if strings.ToLower(d.Environment) == "production" {
				prodCount++
			} else {
				stagCount++
			}
		}
		total := len(m.deployments)
		if total == 0 {
			summaryLine = styleSuccess.Render("No pending deployments")
		} else {
			parts := []string{styleWarn.Render(fmt.Sprintf("%d pending", total))}
			if prodCount > 0 {
				parts = append(parts, styleEnvProduction.Render(fmt.Sprintf("● %d production", prodCount)))
			}
			if stagCount > 0 {
				parts = append(parts, styleEnvStaging.Render(fmt.Sprintf("● %d staging", stagCount)))
			}
			summaryLine = strings.Join(parts, "  ")
		}
	}
	return metaLine + "\n" + summaryLine
}

// renderNeedApprovalContent — Section 3 content: tabs + panel content.
func (m Model) renderNeedApprovalContent() string {
	var b strings.Builder
	b.WriteString(m.renderTabs())
	b.WriteString("\n\n")
	if m.loading && m.viewState != viewConfirm {
		b.WriteString(fmt.Sprintf("%s Loading...", m.spin.View()))
	} else {
		switch m.viewState {
		case viewConfirm:
			b.WriteString(m.renderConfirm())
		default:
			switch m.activePanel {
			case panelPending:
				b.WriteString(m.renderPending())
			case panelHistory:
				b.WriteString(m.renderHistory())
			case panelChangelog:
				b.WriteString(m.renderChangelogPanel())
			}
		}
	}
	return b.String()
}

// renderRepoContent — Section 4 content: 3-column grid of watched repos.
func (m Model) renderRepoContent() string {
	if len(m.repos) == 0 {
		return styleHelp.Render("No repositories. Add with: greenlight config add-repo owner/repo")
	}

	const cols = 3
	const gap = 2 // spaces between columns

	innerW := max(1, m.width-4) // matches renderTitledBox inner width
	colW := max(20, (innerW-gap*(cols-1))/cols)

	dot := lipgloss.NewStyle().Foreground(colorAccent).Render("●")

	var sb strings.Builder
	for i, r := range m.repos {
		item := dot + " " + r
		col := i % cols
		isLast := i == len(m.repos)-1
		isRowEnd := col == cols-1

		if isRowEnd || isLast {
			sb.WriteString(item)
			if !isLast {
				sb.WriteString("\n")
			}
		} else {
			pad := max(0, colW-lipgloss.Width(item))
			sb.WriteString(item + strings.Repeat(" ", pad+gap))
		}
	}
	return sb.String()
}

func (m Model) renderTabs() string {
	var tabs []string
	for i := panel(0); i < panelCount; i++ {
		name := panelNames[i]
		if i == panelPending {
			name = fmt.Sprintf("%s (%d)", name, len(m.deployments))
		}
		if i == m.activePanel {
			tabs = append(tabs, lipgloss.NewStyle().
				Foreground(colorPink).
				Bold(true).
				BorderStyle(lipgloss.NormalBorder()).
				BorderBottom(true).
				BorderForeground(colorPink).
				Padding(0, 1).
				Render(name))
		} else {
			tabs = append(tabs, lipgloss.NewStyle().
				Foreground(colorMuted).
				Padding(0, 1).
				Render(name))
		}
	}

	stgState := "●"
	if !m.showStaging {
		stgState = "○"
	}
	prdState := "●"
	if !m.showProduction {
		prdState = "○"
	}
	stgBadge := styleHelp.Render(fmt.Sprintf("[s] %s staging", stgState))
	var prdBadge string
	if m.cfg.Role == config.RoleTechLead {
		prdBadge = styleHelp.Render(fmt.Sprintf("  [p] %s prod", prdState))
	}

	left := lipgloss.JoinHorizontal(lipgloss.Left, tabs...)
	right := stgBadge + prdBadge
	// Subtract 4 for inner box padding (2 padding left/right from renderTitledBox)
	pad := max(0, m.width-lipgloss.Width(left)-lipgloss.Width(right)-6)
	return left + strings.Repeat(" ", pad) + right
}

func (m Model) renderStatus() string {
	switch m.statusKind {
	case "success":
		return styleSuccess.Render(m.statusMsg)
	case "error":
		return styleError.Render(m.statusMsg)
	case "warn":
		return styleWarn.Render(m.statusMsg)
	default:
		return ""
	}
}

func (m Model) renderPending() string {
	if len(m.deployments) == 0 {
		return styleHelp.Render("No pending deployments for your visible environments.\nUse [s]/[p] to toggle filters.")
	}
	return m.pendingTable.View()
}

func (m Model) renderHistory() string {
	if m.githubHistoryLoading {
		return fmt.Sprintf("%s Fetching from GitHub...", m.spin.View())
	}

	var sourceHint string
	if m.githubHistoryMode {
		sourceHint = lipgloss.NewStyle().Foreground(colorCyan).Render("◎ github") + "  " + styleHelp.Render("[g] back to local")
	} else {
		sourceHint = lipgloss.NewStyle().Foreground(colorSubtle).Render("● local") + "  " + styleHelp.Render("[g] load from github")
	}

	if len(m.histEntries) == 0 && !m.githubHistoryMode {
		return styleHelp.Render("No history yet. Approve or reject deployments to see history here.") +
			"\n" + sourceHint
	}
	return sourceHint + "\n" + m.historyTable.View()
}

func (m Model) renderChangelogPanel() string {
	if m.changelogRunning {
		return fmt.Sprintf("%s Loading changelog...", m.spin.View())
	}
	title := lipgloss.NewStyle().Foreground(colorCyan).Render(m.changelogTitle)
	return title + "\n" + m.changelogVP.View()
}

func (m Model) renderConfirm() string {
	if m.pendingTable.Cursor() >= len(m.deployments) {
		return ""
	}
	d := m.deployments[m.pendingTable.Cursor()]

	icon := "✅"
	if m.confirmAction == "reject" {
		icon = "❌"
	}

	envStr := renderEnvLabel(d.Environment)
	typeStr := renderTypeBadge(d.ReleaseType)

	content := fmt.Sprintf(
		"%s %s deployment?\n\n"+
			"  Tag:         %s\n"+
			"  Type:        %s\n"+
			"  Environment: %s\n"+
			"  Workflow:    %s\n"+
			"  Branch:      %s\n"+
			"  Triggered by: %s\n\n"+
			"  [y] Confirm   [n/esc] Cancel",
		icon, m.confirmAction,
		styleTag.Render(d.Tag),
		typeStr,
		envStr,
		d.WorkflowName,
		d.Branch,
		d.Actor,
	)

	borderColor := colorSuccess
	if m.confirmAction == "reject" {
		borderColor = colorProduction
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(1, 3).
		Render(content)
}

func (m Model) renderHelp() string {
	hints := []string{
		keyHint(kb.Approve), keyHint(kb.Reject),
		keyHint(kb.Open), keyHint(kb.Tab),
		keyHint(kb.Refresh), keyHint(kb.ToggleRepos), keyHint(kb.Quit),
	}
	if m.activePanel == panelHistory {
		hints = append(hints, keyHint(kb.GitHubHistory))
	}
	helpStr := styleHelp.Render("  " + strings.Join(hints, "  "))
	verStr := lipgloss.NewStyle().Foreground(colorSubtle).Render(appVersion + "  ")
	gap := max(0, m.width-lipgloss.Width(helpStr)-lipgloss.Width(verStr))
	return helpStr + strings.Repeat(" ", gap) + verStr
}

// ── Commands ──────────────────────────────────────────────────────────────────

func (m Model) fetchDeployments() tea.Cmd {
	if m.mockMode {
		pending := m.mockPending
		return func() tea.Msg {
			return msgDeployments{items: pending}
		}
	}
	envs := m.visibleEnvs()
	repos := m.repos
	return func() tea.Msg {
		items, warnings, err := gh.GetPendingApprovalsAll(repos, envs)
		return msgDeployments{items: items, warnings: warnings, err: err}
	}
}

func (m Model) refreshHistory() tea.Cmd {
	if m.mockMode {
		entries := m.mockHistoryEntries
		return func() tea.Msg { return entries }
	}
	return func() tea.Msg {
		entries, err := history.Recent(50, "")
		if err != nil {
			// History is non-critical; return empty list on error
			return []history.Entry{}
		}
		return entries
	}
}

func (m Model) fetchGitHubHistory() tea.Cmd {
	if m.mockMode {
		return func() tea.Msg {
			time.Sleep(500 * time.Millisecond)
			return msgGitHubHistory{reviews: buildMockGitHubHistory()}
		}
	}
	repos := m.repos
	return func() tea.Msg {
		var all []gh.DeploymentReview
		var firstErr error
		for _, repo := range repos {
			reviews, err := gh.GetDeploymentReviews(repo, 50)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			all = append(all, reviews...)
		}
		if len(all) == 0 && firstErr != nil {
			return msgGitHubHistory{err: firstErr}
		}
		return msgGitHubHistory{reviews: all}
	}
}

func (m *Model) applyGitHubHistoryTable(reviews []gh.DeploymentReview) {
	rows := make([]table.Row, len(reviews))
	for i, r := range reviews {
		action := "✅ approved"
		if r.State == "rejected" {
			action = "❌ rejected"
		}
		rows[i] = table.Row{
			r.ReviewedAt.Format("15:04 02-Jan-06"),
			action,
			truncate(r.Tag, 12),
			r.Environment,
			truncate(r.WorkflowName, 20),
			r.Reviewer,
		}
	}
	m.historyTable.SetRows(rows)
}

func (m *Model) applyHistoryTable(entries []history.Entry) {
	m.histEntries = entries
	rows := make([]table.Row, len(entries))
	for i, e := range entries {
		action := "✅ approved"
		if e.Action == history.ActionRejected {
			action = "❌ rejected"
		}
		rows[i] = table.Row{
			e.Timestamp.Format("15:04 02-Jan-06"),
			action,
			e.Tag,
			e.Environment,
			truncate(e.Workflow, 20),
			e.ApprovedBy,
		}
	}
	m.historyTable.SetRows(rows)
}

func (m Model) loadChangelog() tea.Cmd {
	if len(m.deployments) == 0 {
		return nil
	}
	idx := m.pendingTable.Cursor()
	if idx >= len(m.deployments) {
		return nil
	}
	d := m.deployments[idx]
	if d.Tag == "" {
		return nil
	}

	if m.mockMode {
		entries, from, to := buildMockChangelog(d.Tag)
		return func() tea.Msg {
			return msgChangelog{entries: entries, fromTag: from, toTag: to}
		}
	}

	repo := d.Repo // use the repo the deployment belongs to
	currentTag := d.Tag
	return func() tea.Msg {
		prevTag, _ := gh.GetPreviousTag(repo, currentTag)
		if prevTag == "" {
			return msgChangelog{err: fmt.Errorf("no previous tag")}
		}
		entries, err := gh.GetChangelog(repo, prevTag, currentTag)
		return msgChangelog{entries: entries, fromTag: prevTag, toTag: currentTag, err: err}
	}
}

func (m Model) doAction(action string, d gh.PendingDeployment) tea.Cmd {
	if m.mockMode {
		return func() tea.Msg {
			time.Sleep(600 * time.Millisecond)
			return msgActionDone{action: action, deployment: d}
		}
	}
	repo := d.Repo // use the repo the deployment belongs to
	user := m.cfg.GitHubUser
	return func() tea.Msg {
		comment := fmt.Sprintf("%sd via greenlight by %s", action, user)
		var err error
		if action == "approve" {
			err = gh.ApproveDeployment(repo, d.RunID, d.Environment, comment)
		} else {
			err = gh.RejectDeployment(repo, d.RunID, d.Environment, comment)
		}

		if err == nil {
			act := history.ActionApproved
			if action == "reject" {
				act = history.ActionRejected
			}
			_ = history.Append(history.Entry{
				Timestamp:   time.Now(),
				Action:      act,
				RunID:       d.RunID,
				Environment: d.Environment,
				Workflow:    d.WorkflowName,
				Tag:         d.Tag,
				Branch:      d.Branch,
				Actor:       d.Actor,
				ApprovedBy:  user,
			})
		}

		return msgActionDone{action: action, deployment: d, err: err}
	}
}

func openURL(url string) tea.Cmd {
	return func() tea.Msg {
		_ = exec.Command("open", url).Start()
		return nil
	}
}

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return msgTick(t) })
}

// calcTableHeight computes the table row height based on terminal height and layout.
// Overhead: title(3) + info panel(4) + gap(2) + need-approval top(1) + tabs(1) + gap(1)
//   - table header(2) + need-approval bottom(1) + gap(2) + repo(4 or 1) + footer(1)
func (m Model) calcTableHeight() int {
	overhead := 22 // repos shown
	if m.reposCollapsed {
		overhead = 19
	}
	return clamp(m.height-overhead, 3, 30)
}

// ── Filtering ─────────────────────────────────────────────────────────────────

func (m Model) visibleEnvs() []string {
	var envs []string
	if m.showStaging {
		envs = append(envs, "staging")
	}
	if m.showProduction {
		envs = append(envs, "production")
	}
	return envs
}

func (m Model) filterDeployments(items []gh.PendingDeployment) []gh.PendingDeployment {
	visible := m.visibleEnvs()
	set := make(map[string]bool)
	for _, e := range visible {
		set[strings.ToLower(e)] = true
	}
	var result []gh.PendingDeployment
	for _, d := range items {
		if set[strings.ToLower(d.Environment)] {
			result = append(result, d)
		}
	}
	return result
}

func (m *Model) applyFilter() {
	// Always filter from the full unfiltered set so toggling env filters
	// can restore items that were previously hidden.
	m.deployments = m.filterDeployments(m.rawDeployments)
	m.pendingTable.SetRows(m.toTableRows(m.deployments))
}

// ── Render helpers ────────────────────────────────────────────────────────────

func (m Model) toTableRows(deps []gh.PendingDeployment) []table.Row {
	rows := make([]table.Row, len(deps))
	for i, d := range deps {
		base := table.Row{
			truncate(d.Tag, 20),
			renderTypeLabel(d.ReleaseType),
			renderEnvCell(d.Environment),
			truncate(d.WorkflowName, 20),
			truncate(d.Branch, 16),
			truncate(d.Actor, 9),
			formatDuration(time.Since(d.WaitingSince)),
		}
		if m.multiRepo {
			rows[i] = append(table.Row{shortRepo(d.Repo)}, base...)
		} else {
			rows[i] = base
		}
	}
	return rows
}

// shortRepo returns just the repo name part (without owner) for compact display.
func shortRepo(repo string) string {
	if idx := strings.LastIndex(repo, "/"); idx != -1 {
		return repo[idx+1:]
	}
	return repo
}

func renderTagCell(tag string) string {
	if tag == "" {
		return styleHelp.Render("—")
	}
	return tag
}

// renderEnvCell returns plain text for table cells (no ANSI — bubbles/table measures
// column width by byte length, so ANSI escape codes cause display corruption).
func renderEnvCell(env string) string {
	switch strings.ToLower(env) {
	case "production":
		return "▲ " + env
	case "staging":
		return "● " + env
	default:
		return env
	}
}

func renderEnvLabel(env string) string {
	switch strings.ToLower(env) {
	case "production":
		return styleEnvProduction.Render(env)
	case "staging":
		return styleEnvStaging.Render(env)
	default:
		return env
	}
}

// renderTypeLabel returns plain text for table cells.
// Lipgloss ANSI codes inside bubbles/table cells get clipped and show as artifacts.
func renderTypeLabel(rt gh.ReleaseType) string {
	switch rt {
	case gh.ReleaseHotfix:
		return "HOTFIX"
	case gh.ReleasePreview:
		return "PREVIEW"
	default:
		return "REGULAR"
	}
}

// renderTypeBadge is used in confirm dialogs and panels (full badge with background).
func renderTypeBadge(rt gh.ReleaseType) string {
	switch rt {
	case gh.ReleaseHotfix:
		return styleBadge(colorHotfix).Render("HOTFIX")
	case gh.ReleasePreview:
		return styleBadge(colorPreview).Render("PREVIEW")
	default:
		return styleBadge(lipgloss.Color("30")).Render("REGULAR")
	}
}

func renderChangelog(entries []gh.ChangelogEntry) string {
	var b strings.Builder
	for _, e := range entries {
		sha := lipgloss.NewStyle().Foreground(colorAccent).Render(e.SHA)
		author := styleHelp.Render("@" + e.Author)
		b.WriteString(fmt.Sprintf("  %s  %s  %s\n", sha, e.Message, author))
	}
	return b.String()
}

func keyHint(b key.Binding) string {
	return fmt.Sprintf("%s %s",
		lipgloss.NewStyle().Foreground(colorAccent).Render(b.Help().Key),
		lipgloss.NewStyle().Foreground(colorMuted).Render(b.Help().Desc),
	)
}

func (m *Model) setStatus(kind, msg string) {
	m.statusKind = kind
	m.statusMsg = msg
}

// ── Misc helpers ──────────────────────────────────────────────────────────────

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ── Entry point ───────────────────────────────────────────────────────────────

func Start() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Auto-detect GitHub user if not set
	if cfg.GitHubUser == "" {
		if user, err := gh.GetCurrentUser(); err == nil {
			cfg.GitHubUser = user
			_ = config.Save(cfg)
		}
	}

	repos := cfg.AllRepos()
	if len(repos) == 0 {
		if repo, err := gh.GetCurrentRepo(); err == nil {
			repos = []string{repo}
		}
	}

	p := tea.NewProgram(
		newModel(cfg, repos, false),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err = p.Run()
	return err
}

func StartMock() error {
	cfg := config.Config{
		GitHubUser:   "adhan",
		Role:         config.RoleTechLead,
		EnvFilter:    config.EnvFilter{Staging: true, Production: true},
		PollInterval: 30,
		Repos:        []string{"demo/greenlight", "demo/backend-api"},
	}
	p := tea.NewProgram(
		newModel(cfg, cfg.Repos, true),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}

// ── Mock data ─────────────────────────────────────────────────────────────────

func buildMockDeployments() []gh.PendingDeployment {
	now := time.Now()
	return []gh.PendingDeployment{
		{
			Repo:         "demo/greenlight",
			RunID:        1001,
			RunURL:       "https://github.com/demo/greenlight/actions/runs/1001",
			Environment:  "production",
			WaitingSince: now.Add(-3 * time.Minute),
			WorkflowName: "Release Deployment",
			Actor:        "wirya",
			Branch:       "hotfix/v1.6.3-db-fix",
			Tag:          "v1.6.3",
			ReleaseType:  gh.ReleaseHotfix,
			IsProduction: true,
		},
		{
			Repo:         "demo/greenlight",
			RunID:        1002,
			RunURL:       "https://github.com/demo/greenlight/actions/runs/1002",
			Environment:  "staging",
			WaitingSince: now.Add(-5 * time.Minute),
			WorkflowName: "Release Deployment",
			Actor:        "wirya",
			Branch:       "release/v1.7.x",
			Tag:          "v1.7.2",
			ReleaseType:  gh.ReleaseRegular,
			IsProduction: false,
		},
		{
			Repo:         "demo/backend-api",
			RunID:        2001,
			RunURL:       "https://github.com/demo/backend-api/actions/runs/2001",
			Environment:  "staging",
			WaitingSince: now.Add(-12 * time.Minute),
			WorkflowName: "Release Deployment",
			Actor:        "adhan",
			Branch:       "release/v3.1.x",
			Tag:          "v3.1.0",
			ReleaseType:  gh.ReleaseRegular,
			IsProduction: false,
		},
		{
			Repo:         "demo/backend-api",
			RunID:        2002,
			RunURL:       "https://github.com/demo/backend-api/actions/runs/2002",
			Environment:  "staging",
			WaitingSince: now.Add(-20 * time.Minute),
			WorkflowName: "Release Deployment",
			Actor:        "alex",
			Branch:       "main",
			Tag:          "v4.0.0-preview",
			ReleaseType:  gh.ReleasePreview,
			IsProduction: false,
		},
	}
}

func buildMockHistory() []history.Entry {
	now := time.Now()
	return []history.Entry{
		{Timestamp: now.Add(-1 * time.Hour), Action: history.ActionApproved, RunID: 991, Tag: "v1.7.1", Environment: "production", Workflow: "Release Deployment", Branch: "release/v1.7.x", Actor: "wirya", ApprovedBy: "adhan"},
		{Timestamp: now.Add(-2 * time.Hour), Action: history.ActionApproved, RunID: 990, Tag: "v1.7.1", Environment: "staging", Workflow: "Release Deployment", Branch: "release/v1.7.x", Actor: "wirya", ApprovedBy: "adhan"},
		{Timestamp: now.Add(-26 * time.Hour), Action: history.ActionRejected, RunID: 980, Tag: "v1.6.4", Environment: "staging", Workflow: "Release Deployment", Branch: "release/v1.6.x", Actor: "wirya", ApprovedBy: "adhan"},
		{Timestamp: now.Add(-48 * time.Hour), Action: history.ActionApproved, RunID: 970, Tag: "v1.6.3", Environment: "production", Workflow: "Release Deployment", Branch: "release/v1.6.x", Actor: "wirya", ApprovedBy: "adhan"},
		{Timestamp: now.Add(-50 * time.Hour), Action: history.ActionApproved, RunID: 969, Tag: "v1.6.3", Environment: "staging", Workflow: "Release Deployment", Branch: "release/v1.6.x", Actor: "wirya", ApprovedBy: "adhan"},
	}
}

func buildMockChangelog(tag string) ([]gh.ChangelogEntry, string, string) {
	// derive a fake "previous" tag from current
	prevTag := tag
	switch tag {
	case "v1.6.3":
		prevTag = "v1.6.2"
	case "v1.7.2":
		prevTag = "v1.7.1"
	case "v2.0.0-preview":
		prevTag = "v1.9.5"
	default:
		prevTag = "v0.0.0"
	}
	entries := []gh.ChangelogEntry{
		{SHA: "a1b2c3d", Message: "feat: add new payment gateway integration", Author: "wirya"},
		{SHA: "e4f5a6b", Message: "fix: resolve null pointer on checkout flow", Author: "adhan"},
		{SHA: "c7d8e9f", Message: "chore: update production dependencies", Author: "github-actions[bot]"},
		{SHA: "0a1b2c3", Message: "feat: add user segment analytics tracking", Author: "wirya"},
		{SHA: "d4e5f6a", Message: "fix: mobile layout broken on product listing", Author: "adhan"},
		{SHA: "b7c8d9e", Message: "test: add integration tests for cart service", Author: "wirya"},
		{SHA: "f0a1b2c", Message: "docs: update REST API documentation", Author: "adhan"},
	}
	return entries, prevTag, tag
}

func buildMockGitHubHistory() []gh.DeploymentReview {
	now := time.Now()
	return []gh.DeploymentReview{
		{RunID: 991, WorkflowName: "Release Deployment", Tag: "v1.7.1", Branch: "release/v1.7.x", Actor: "wirya", Reviewer: "adhan", State: "approved", Environment: "production", ReviewedAt: now.Add(-1 * time.Hour)},
		{RunID: 990, WorkflowName: "Release Deployment", Tag: "v1.7.1", Branch: "release/v1.7.x", Actor: "wirya", Reviewer: "adhan", State: "approved", Environment: "staging", ReviewedAt: now.Add(-2 * time.Hour)},
		{RunID: 980, WorkflowName: "Release Deployment", Tag: "v1.6.4", Branch: "release/v1.6.x", Actor: "wirya", Reviewer: "adhan", State: "rejected", Environment: "staging", ReviewedAt: now.Add(-26 * time.Hour)},
		{RunID: 970, WorkflowName: "Release Deployment", Tag: "v1.6.3", Branch: "release/v1.6.x", Actor: "wirya", Reviewer: "adhan", State: "approved", Environment: "production", ReviewedAt: now.Add(-48 * time.Hour)},
		// team members' actions — invisible in local history
		{RunID: 950, WorkflowName: "Release Deployment", Tag: "v1.5.9", Branch: "release/v1.5.x", Actor: "alex", Reviewer: "sari", State: "approved", Environment: "production", ReviewedAt: now.Add(-72 * time.Hour)},
		{RunID: 940, WorkflowName: "Release Deployment", Tag: "v1.5.9", Branch: "release/v1.5.x", Actor: "alex", Reviewer: "sari", State: "approved", Environment: "staging", ReviewedAt: now.Add(-74 * time.Hour)},
		{RunID: 930, WorkflowName: "Release Deployment", Tag: "v1.5.8", Branch: "release/v1.5.x", Actor: "wirya", Reviewer: "sari", State: "rejected", Environment: "production", ReviewedAt: now.Add(-96 * time.Hour)},
		{RunID: 920, WorkflowName: "Release Deployment", Tag: "v1.5.7", Branch: "release/v1.5.x", Actor: "alex", Reviewer: "sari", State: "approved", Environment: "production", ReviewedAt: now.Add(-120 * time.Hour)},
	}
}

func removePending(items []gh.PendingDeployment, runID int64, env string) []gh.PendingDeployment {
	result := make([]gh.PendingDeployment, 0, len(items))
	for _, d := range items {
		if d.RunID != runID || !strings.EqualFold(d.Environment, env) {
			result = append(result, d)
		}
	}
	return result
}
