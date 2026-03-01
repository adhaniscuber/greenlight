package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	gh "github.com/wirya/greenlight/internal/github"
	"github.com/wirya/greenlight/internal/daemon"
	"github.com/wirya/greenlight/internal/ui"
	"github.com/wirya/greenlight/pkg/config"
	"github.com/wirya/greenlight/pkg/history"
)

// ── Root ──────────────────────────────────────────────────────────────────────

var rootCmd = &cobra.Command{
	Use:   "greenlight",
	Short: "TUI dashboard for GitHub Actions deployment approvals",
	Long:  `Monitor, approve, and reject GitHub Actions deployment environment approvals.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mock, _ := cmd.Flags().GetBool("mock")
		if mock {
			return ui.StartMock()
		}
		return ui.Start()
	},
}

// ── daemon ────────────────────────────────────────────────────────────────────

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run in background, send macOS notifications on pending approvals",
	RunE: func(cmd *cobra.Command, args []string) error {
		mock, _ := cmd.Flags().GetBool("mock")
		if mock {
			return daemon.StartMock()
		}
		interval, _ := cmd.Flags().GetInt("interval")
		repoFlag, _ := rootCmd.PersistentFlags().GetString("repo")
		var repos []string
		if repoFlag != "" {
			repos = []string{repoFlag}
		}
		return daemon.Start(repos, interval)
	},
}

// ── approve / reject ──────────────────────────────────────────────────────────

var approveCmd = &cobra.Command{
	Use:   "approve [run-id] [environment]",
	Short: "Approve a deployment (called by notification or CLI)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		repo, _ := rootCmd.PersistentFlags().GetString("repo")
		if repo == "" {
			return fmt.Errorf("--repo is required (e.g. --repo owner/repo)")
		}
		runID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid run-id %q: must be a number", args[0])
		}
		if err := gh.ApproveDeployment(repo, runID, args[1], "Approved via greenlight"); err != nil {
			return err
		}
		fmt.Printf("✅ Approved run %s env %s on %s\n", args[0], args[1], repo)
		return nil
	},
}

var rejectCmd = &cobra.Command{
	Use:   "reject [run-id] [environment]",
	Short: "Reject a deployment (called by notification or CLI)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		repo, _ := rootCmd.PersistentFlags().GetString("repo")
		if repo == "" {
			return fmt.Errorf("--repo is required (e.g. --repo owner/repo)")
		}
		runID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid run-id %q: must be a number", args[0])
		}
		if err := gh.RejectDeployment(repo, runID, args[1], "Rejected via greenlight"); err != nil {
			return err
		}
		fmt.Printf("❌ Rejected run %s env %s on %s\n", args[0], args[1], repo)
		return nil
	},
}

// ── config ────────────────────────────────────────────────────────────────────

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or update configuration",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current config",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		fmt.Printf("Config file : %s\n", config.ConfigFilePath())
		fmt.Printf("GitHub user : %s\n", cfg.GitHubUser)
		fmt.Printf("Role        : %s\n", cfg.Role)
		fmt.Printf("Poll interval: %ds\n", cfg.PollInterval)
		fmt.Printf("Allowed envs: %v\n", cfg.AllowedEnvironments())
		repos := cfg.AllRepos()
		if len(repos) == 0 {
			fmt.Printf("Repos       : (none — use 'greenlight config add-repo owner/repo')\n")
		} else {
			fmt.Printf("Repos (%d)   :\n", len(repos))
			for _, r := range repos {
				fmt.Printf("              %s\n", r)
			}
		}
		return nil
	},
}

var configSetRoleCmd = &cobra.Command{
	Use:   "set-role [engineer|techlead]",
	Short: "Set your role (determines which environments you can approve)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		role := config.Role(args[0])
		if role != config.RoleEngineer && role != config.RoleTechLead {
			return fmt.Errorf("invalid role %q — must be 'engineer' or 'techlead'", args[0])
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.Role = role
		// tech leads see production by default
		if role == config.RoleTechLead {
			cfg.EnvFilter.Production = true
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("✅ Role set to: %s\n", role)
		fmt.Printf("   Allowed envs: %v\n", cfg.AllowedEnvironments())
		return nil
	},
}

var configSetRepoCmd = &cobra.Command{
	Use:   "set-repo [owner/repo]",
	Short: "Set single default repo (legacy, prefer add-repo for multi-repo)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.DefaultRepo = args[0]
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("✅ Default repo set to: %s\n", args[0])
		return nil
	},
}

var configAddRepoCmd = &cobra.Command{
	Use:   "add-repo [owner/repo or GitHub URL]",
	Short: "Add a repo to the watch list (accepts owner/repo or https://github.com/owner/repo)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		repo, err := parseRepoInput(args[0])
		if err != nil {
			return err
		}
		for _, r := range cfg.Repos {
			if r == repo {
				fmt.Printf("ℹ️  %s is already in the watch list\n", repo)
				return nil
			}
		}
		cfg.Repos = append(cfg.Repos, repo)
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("✅ Added: %s\n", repo)
		fmt.Printf("   Watching: %v\n", cfg.AllRepos())
		return nil
	},
}

var configRemoveRepoCmd = &cobra.Command{
	Use:   "remove-repo [owner/repo or GitHub URL]",
	Short: "Remove a repo from the watch list",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		repo, err := parseRepoInput(args[0])
		if err != nil {
			return err
		}
		updated := cfg.Repos[:0]
		found := false
		for _, r := range cfg.Repos {
			if r == repo {
				found = true
				continue
			}
			updated = append(updated, r)
		}
		if !found {
			fmt.Printf("ℹ️  %s not found in watch list\n", repo)
			return nil
		}
		cfg.Repos = updated
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("✅ Removed: %s\n", repo)
		fmt.Printf("   Watching: %v\n", cfg.AllRepos())
		return nil
	},
}

// ── history ───────────────────────────────────────────────────────────────────

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "Show approval/rejection history",
	RunE: func(cmd *cobra.Command, args []string) error {
		env, _ := cmd.Flags().GetString("env")
		n, _ := cmd.Flags().GetInt("limit")
		source, _ := cmd.Flags().GetString("source")

		if source == "github" {
			repo, _ := rootCmd.PersistentFlags().GetString("repo")
			if repo == "" {
				var err error
				repo, err = gh.GetCurrentRepo()
				if err != nil {
					return fmt.Errorf("no --repo specified and not in a GitHub repo: %w", err)
				}
			}
			fmt.Printf("Fetching deployment reviews from GitHub (%s)...\n\n", repo)
			reviews, err := gh.GetDeploymentReviews(repo, n)
			if err != nil {
				return fmt.Errorf("fetching GitHub history: %w", err)
			}
			if len(reviews) == 0 {
				fmt.Println("No deployment reviews found.")
				return nil
			}
			fmt.Printf("%-16s  %-10s  %-14s  %-12s  %-22s  %s\n",
				"Time", "Action", "Tag", "Environment", "Workflow", "Reviewer")
			fmt.Println(strings.Repeat("─", 95))
			for _, r := range reviews {
				if env != "" && !strings.EqualFold(r.Environment, env) {
					continue
				}
				action := "✅ approved"
				if r.State == "rejected" {
					action = "❌ rejected"
				}
				fmt.Printf("%-16s  %-10s  %-14s  %-12s  %-22s  %s\n",
					r.ReviewedAt.Format("15:04 02-Jan-06"),
					action,
					truncate(r.Tag, 14),
					r.Environment,
					truncate(r.WorkflowName, 20),
					r.Reviewer,
				)
			}
			return nil
		}

		// local JSON history (default)
		entries, err := history.Recent(n, env)
		if err != nil {
			return fmt.Errorf("reading history: %w", err)
		}
		if len(entries) == 0 {
			fmt.Println("No history yet.")
			return nil
		}
		fmt.Printf("%-16s  %-10s  %-12s  %-12s  %-22s  %s\n",
			"Time", "Action", "Tag", "Environment", "Workflow", "By")
		fmt.Println(strings.Repeat("─", 90))
		for _, e := range entries {
			action := "✅ approved"
			if e.Action == history.ActionRejected {
				action = "❌ rejected"
			}
			fmt.Printf("%-16s  %-10s  %-12s  %-12s  %-22s  %s\n",
				e.Timestamp.Format("15:04 02-Jan-06"),
				action,
				e.Tag,
				e.Environment,
				truncate(e.Workflow, 20),
				e.ApprovedBy,
			)
		}
		return nil
	},
}

// parseRepoInput normalises a repo argument to "owner/repo" format.
// Accepts:
//   - owner/repo               → owner/repo
//   - https://github.com/owner/repo        → owner/repo
//   - https://github.com/owner/repo/tree/… → owner/repo (extra path stripped)
func parseRepoInput(input string) (string, error) {
	input = strings.TrimSpace(input)
	input = strings.TrimRight(input, "/")

	// URL form: extract the two path segments after github.com/
	if strings.Contains(input, "github.com/") {
		idx := strings.Index(input, "github.com/")
		path := input[idx+len("github.com/"):]
		parts := strings.SplitN(path, "/", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", fmt.Errorf("cannot parse repo from %q — expected github.com/owner/repo", input)
		}
		return parts[0] + "/" + parts[1], nil
	}

	// Plain owner/repo form
	if strings.Count(input, "/") == 1 {
		return input, nil
	}

	return "", fmt.Errorf("invalid repo %q — use 'owner/repo' or a GitHub URL", input)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ── Execute ───────────────────────────────────────────────────────────────────

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringP("repo", "r", "", "GitHub repo (owner/repo)")
	rootCmd.Flags().Bool("mock", false, "Run with mock data (no GitHub connection needed)")

	daemonCmd.Flags().IntP("interval", "i", 0, "Polling interval in seconds (default: from config)")
	daemonCmd.Flags().Bool("mock", false, "Show mock bulk dialog immediately (no GitHub connection)")

	historyCmd.Flags().StringP("env", "e", "", "Filter by environment (staging, production)")
	historyCmd.Flags().IntP("limit", "n", 20, "Number of entries to show")
	historyCmd.Flags().String("source", "local", "History source: local | github")

	configCmd.AddCommand(configShowCmd, configSetRoleCmd, configSetRepoCmd, configAddRepoCmd, configRemoveRepoCmd)

	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(approveCmd)
	rootCmd.AddCommand(rejectCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(historyCmd)
}
