package notify

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DeploymentItem is a simplified view of a pending deployment for bulk notification.
type DeploymentItem struct {
	Repo        string
	RunID       int64
	Tag         string
	Environment string
	Actor       string
	ReleaseType string
}

// SendBulkNotification sends a macOS Notification Center banner for pending deployments.
// Appears top-right and stays in Notification Center.
func SendBulkNotification(items []DeploymentItem) error {
	if len(items) == 0 {
		return nil
	}

	title := fmt.Sprintf("Greenlight — %d Pending Approval(s)", len(items))

	var lines []string
	for _, d := range items {
		env := d.Environment
		if strings.ToLower(env) == "production" {
			env = "PROD(!)"
		}
		label := releaseTypeLabel(d.ReleaseType)
		lines = append(lines, fmt.Sprintf("[%s] %s %s -> %s (by %s)", label, repoName(d.Repo), d.Tag, env, d.Actor))
	}
	body := strings.Join(lines, "\n")

	return sendNotification(title, body)
}

// SendActionNotification sends a confirmation banner after approve/reject.
func SendActionNotification(title, body string) error {
	return sendNotification(title, body)
}

// terminalNotifierPaths are candidate locations for terminal-notifier binary.
var terminalNotifierPaths = []string{
	"/opt/homebrew/opt/terminal-notifier/bin/terminal-notifier", // Apple Silicon brew
	"/usr/local/opt/terminal-notifier/bin/terminal-notifier",    // Intel brew
	"terminal-notifier",                                          // in PATH
}

func sendNotification(title, body string) error {
	// Try terminal-notifier: click activates the user's terminal
	for _, tn := range terminalNotifierPaths {
		if !binaryExists(tn) {
			continue
		}
		args := []string{"-title", title, "-message", body, "-sound", "default"}
		if bundleID := preferredTerminalBundleID(); bundleID != "" {
			args = append(args, "-activate", bundleID)
		}
		cmd := exec.Command(tn, args...)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	// Fallback: osascript (click opens Script Editor, but at least shows notification)
	script := fmt.Sprintf(
		`display notification %s with title %s sound name "default"`,
		asStr(body), asStr(title),
	)
	cmd := exec.Command("osascript", "-")
	cmd.Stdin = strings.NewReader(script)
	return cmd.Run()
}

// preferredTerminalBundleID detects the user's terminal emulator bundle ID.
// Checks TERM_PROGRAM env first (set by most terminals), then checks installed apps.
func preferredTerminalBundleID() string {
	termProgramMap := map[string]string{
		"ghostty":        "com.mitchellh.ghostty",
		"iTerm.app":      "com.googlecode.iterm2",
		"Apple_Terminal": "com.apple.Terminal",
		"WezTerm":        "com.github.wez.wezterm",
		"vscode":         "com.microsoft.VSCode",
		"Hyper":          "co.zeit.hyper",
		"alacritty":      "io.alacritty",
	}
	if tp := os.Getenv("TERM_PROGRAM"); tp != "" {
		if id, ok := termProgramMap[tp]; ok {
			return id
		}
	}
	// Daemon/LaunchAgent case: TERM_PROGRAM not set — check installed apps
	type termApp struct {
		bundleID string
		path     string
	}
	candidates := []termApp{
		{"com.mitchellh.ghostty", "/Applications/Ghostty.app"},
		{"com.googlecode.iterm2", "/Applications/iTerm.app"},
		{"com.apple.Terminal", "/System/Applications/Utilities/Terminal.app"},
	}
	for _, t := range candidates {
		if _, err := os.Stat(t.path); err == nil {
			return t.bundleID
		}
	}
	return ""
}

func binaryExists(path string) bool {
	if strings.HasPrefix(path, "/") {
		_, err := os.Stat(path)
		return err == nil
	}
	_, err := exec.LookPath(path)
	return err == nil
}

// asStr wraps a Go string as an AppleScript expression.
// Uses AppleScript string concatenation so any character is safe:
//   - `"` → `" & quote & "`   (quote is a built-in AppleScript constant)
//   - newline → `" & return & "` (handles multi-line commit messages)
// Backslash and apostrophe need no escaping in AppleScript string literals.
func asStr(s string) string {
	s = strings.ReplaceAll(s, `"`, `" & quote & "`)
	s = strings.ReplaceAll(s, "\n", `" & return & "`)
	return `"` + s + `"`
}

func releaseTypeLabel(rt string) string {
	switch strings.ToLower(rt) {
	case "hotfix":
		return "HOTFIX"
	case "preview":
		return "PREVIEW"
	default:
		return "RELEASE"
	}
}

func repoName(repo string) string {
	if idx := strings.LastIndex(repo, "/"); idx != -1 {
		return repo[idx+1:]
	}
	return repo
}
