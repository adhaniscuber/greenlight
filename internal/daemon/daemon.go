package daemon

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	gh "github.com/wirya/greenlight/internal/github"
	"github.com/wirya/greenlight/internal/notify"
	"github.com/wirya/greenlight/pkg/config"
)

// seen tracks deployments already notified, with timestamp for TTL eviction.
var (
	seenMu sync.Mutex
	seen   = make(map[string]time.Time)
)

// seenTTL: entries older than this are re-eligible for notification.
// Covers the case where a deployment is re-queued after being rejected/expired.
const seenTTL = 24 * time.Hour

func pruneSeen() {
	cutoff := time.Now().Add(-seenTTL)
	for k, t := range seen {
		if t.Before(cutoff) {
			delete(seen, k)
		}
	}
}

// StartMock sends a mock notification banner immediately — for local testing.
func StartMock() error {
	fmt.Println("🧪 greenlight daemon mock — sending notification banner...")
	items := []notify.DeploymentItem{
		{Repo: "demo/greenlight", RunID: 1001, Tag: "v1.6.3", Environment: "production", Actor: "wirya", ReleaseType: "hotfix"},
		{Repo: "demo/greenlight", RunID: 1002, Tag: "v1.7.2", Environment: "staging", Actor: "wirya", ReleaseType: "regular"},
		{Repo: "demo/backend-api", RunID: 2001, Tag: "v3.1.0", Environment: "staging", Actor: "adhan", ReleaseType: "regular"},
		{Repo: "demo/backend-api", RunID: 2002, Tag: "v4.0.0-preview", Environment: "staging", Actor: "alex", ReleaseType: "preview"},
	}
	return notify.SendBulkNotification(items)
}

func Start(repos []string, interval int) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if len(repos) == 0 {
		repos = cfg.AllRepos()
	}
	if len(repos) == 0 {
		repo, err := gh.GetCurrentRepo()
		if err != nil {
			return fmt.Errorf("no repos configured (use 'greenlight config add-repo owner/repo'): %w", err)
		}
		repos = []string{repo}
	}
	if interval == 0 {
		interval = cfg.PollInterval
	}

	envs := cfg.AllowedEnvironments()

	fmt.Printf("👀 greenlight daemon started\n")
	fmt.Printf("   Repos:    %s\n", strings.Join(repos, ", "))
	fmt.Printf("   Role:     %s  (watching: %v)\n", cfg.Role, envs)
	fmt.Printf("   Interval: %ds\n", interval)
	fmt.Printf("   PID:      %d\n\n", os.Getpid())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	poll(repos, cfg, envs) // immediate first run

	for {
		select {
		case <-ticker.C:
			poll(repos, cfg, envs)
		case sig := <-sigCh:
			fmt.Printf("\n🛑 Received %s, shutting down...\n", sig)
			return nil
		}
	}
}

func poll(repos []string, cfg config.Config, envs []string) {
	deployments, repoWarnings, err := gh.GetPendingApprovalsAll(repos, envs)
	for _, w := range repoWarnings {
		log.Printf("repo warning: %s", w)
	}
	if err != nil {
		log.Printf("poll error: %v", err)
		return
	}

	if len(deployments) == 0 {
		return
	}

	fmt.Printf("[%s] %d pending deployment(s)\n", time.Now().Format("15:04:05"), len(deployments))

	// Collect only NEW (unseen) deployments — hold lock for the entire read+write section.
	// Keys tracked separately so we only mark seen after a successful notification.
	type candidate struct {
		key  string
		item notify.DeploymentItem
	}

	seenMu.Lock()
	pruneSeen()
	var newCandidates []candidate
	for _, d := range deployments {
		key := fmt.Sprintf("%s|%d|%s", d.Repo, d.RunID, d.Environment)
		if _, alreadySeen := seen[key]; alreadySeen {
			continue
		}
		newCandidates = append(newCandidates, candidate{
			key: key,
			item: notify.DeploymentItem{
				Repo:        d.Repo,
				RunID:       d.RunID,
				Tag:         d.Tag,
				Environment: d.Environment,
				Actor:       d.Actor,
				ReleaseType: string(d.ReleaseType),
			},
		})
	}
	seenMu.Unlock()

	if len(newCandidates) == 0 {
		return
	}

	newItems := make([]notify.DeploymentItem, len(newCandidates))
	for i, c := range newCandidates {
		newItems[i] = c.item
	}

	fmt.Printf("[%s] %d new — sending notification\n", time.Now().Format("15:04:05"), len(newItems))

	// Mark as seen only if notification succeeds — so a failed notification
	// will be retried on the next poll cycle.
	if err := notify.SendBulkNotification(newItems); err != nil {
		log.Printf("notify error: %v", err)
		return
	}
	seenMu.Lock()
	for _, c := range newCandidates {
		seen[c.key] = time.Now()
	}
	seenMu.Unlock()
}
