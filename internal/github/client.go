package github

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ReleaseType matches your create-release.yml strategy
type ReleaseType string

const (
	ReleaseRegular ReleaseType = "regular"
	ReleaseHotfix  ReleaseType = "hotfix"
	ReleasePreview ReleaseType = "preview" // -preview suffix tag
)

// PendingDeployment represents a workflow run waiting for environment approval
type PendingDeployment struct {
	Repo          string // "owner/repo" — which repo this belongs to
	RunID         int64
	RunURL        string
	Environment   string // "staging" | "production"
	WaitingSince  time.Time
	WorkflowName  string
	Actor         string
	Branch        string
	Tag           string      // e.g. "v1.7.2"
	CommitMessage string
	ReleaseType   ReleaseType // from workflow input; falls back to tag/branch heuristic
	IsProduction  bool
}

type ChangelogEntry struct {
	SHA     string
	Message string
	Author  string
	Date    string
}

// GetPendingApprovals fetches all runs in "waiting" state that need approval.
// It filters to only the environments the caller cares about.
func GetPendingApprovals(repo string, allowedEnvs []string) ([]PendingDeployment, error) {
	out, err := runGH("api",
		fmt.Sprintf("repos/%s/actions/runs?status=waiting&per_page=30", repo),
		"-q", `.workflow_runs[] | {id, name, display_title, head_branch, head_sha, actor: .actor.login, commit: .head_commit.message, url: .html_url, updated_at, input_release_type: (.inputs.release_type // ""), input_tag: (.inputs.tag // ""), input_branch: (.inputs.branch // "")}`,
	)
	if err != nil {
		return nil, fmt.Errorf("fetching workflow runs: %w", err)
	}

	allowedSet := make(map[string]bool)
	for _, e := range allowedEnvs {
		allowedSet[strings.ToLower(e)] = true
	}

	var deployments []PendingDeployment

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}

		var raw struct {
			ID               int64     `json:"id"`
			Name             string    `json:"name"`
			DisplayTitle     string    `json:"display_title"`
			HeadBranch       string    `json:"head_branch"`
			Actor            string    `json:"actor"`
			Commit           string    `json:"commit"`
			URL              string    `json:"url"`
			UpdatedAt        time.Time `json:"updated_at"`
			InputReleaseType string    `json:"input_release_type"` // workflow_dispatch inputs.release_type
			InputTag         string    `json:"input_tag"`          // workflow_dispatch inputs.tag
			InputBranch      string    `json:"input_branch"`       // workflow_dispatch inputs.branch
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		// inputs.tag is the authoritative source; fall back to parsing display_title
		tag := raw.InputTag
		if tag == "" {
			tag = extractTag(raw.DisplayTitle, raw.HeadBranch)
		}

		// inputs.branch is the true source branch (release.yml is always triggered --ref main,
		// so head_branch is always "main" regardless of which release branch was used)
		branch := raw.InputBranch
		if branch == "" {
			branch = raw.HeadBranch
		}

		releaseType := detectReleaseType(tag, raw.InputReleaseType)

		envOut, err := runGH("api",
			fmt.Sprintf("repos/%s/actions/runs/%d/pending_deployments", repo, raw.ID),
			"-q", `.[] | {env: .environment.name, can_approve: .current_user_can_approve}`,
		)
		if err != nil {
			continue
		}

		for _, envLine := range strings.Split(strings.TrimSpace(envOut), "\n") {
			if envLine == "" {
				continue
			}
			var envData struct {
				Env        string `json:"env"`
				CanApprove bool   `json:"can_approve"`
			}
			if err := json.Unmarshal([]byte(envLine), &envData); err != nil {
				continue
			}

			envName := strings.ToLower(envData.Env)
			if len(allowedSet) > 0 && !allowedSet[envName] {
				continue
			}

			deployments = append(deployments, PendingDeployment{
				Repo:          repo,
				RunID:         raw.ID,
				RunURL:        raw.URL,
				Environment:   envData.Env,
				WaitingSince:  raw.UpdatedAt,
				WorkflowName:  raw.Name,
				Actor:         raw.Actor,
				Branch:        branch,
				Tag:           tag,
				CommitMessage: firstLine(raw.Commit),
				ReleaseType:   releaseType,
				IsProduction:  envName == "production",
			})
		}
	}

	return deployments, nil
}

// GetPendingApprovalsAll polls multiple repos in parallel and merges results.
//
// Returns (items, repoWarnings, error):
//   - items:        all pending deployments collected across repos
//   - repoWarnings: short human-readable strings for repos that failed (e.g. "voila-ggl-router: not found")
//   - error:        non-nil only when every single repo failed (nothing to show at all)
func GetPendingApprovalsAll(repos []string, allowedEnvs []string) ([]PendingDeployment, []string, error) {
	type result struct {
		repo  string
		items []PendingDeployment
		err   error
	}
	ch := make(chan result, len(repos))
	for _, r := range repos {
		go func(repo string) {
			items, err := GetPendingApprovals(repo, allowedEnvs)
			ch <- result{repo, items, err}
		}(r)
	}
	var all []PendingDeployment
	var warnings []string
	failCount := 0
	for range repos {
		res := <-ch
		if res.err != nil {
			failCount++
			warnings = append(warnings, fmt.Sprintf("%s: %s", repoShortName(res.repo), humanizeGHError(res.err)))
		} else {
			all = append(all, res.items...)
		}
	}
	if failCount == len(repos) {
		// Every repo failed — return the first warning as a hard error.
		return nil, warnings, fmt.Errorf("%s", warnings[0])
	}
	return all, warnings, nil
}

// repoShortName returns the repo part of "owner/repo".
func repoShortName(repo string) string {
	if idx := strings.LastIndex(repo, "/"); idx != -1 {
		return repo[idx+1:]
	}
	return repo
}

// humanizeGHError converts a gh CLI error into a short, readable reason.
func humanizeGHError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "404") || strings.Contains(msg, "Not Found"):
		return "not found (404)"
	case strings.Contains(msg, "403") || strings.Contains(msg, "Forbidden"):
		return "access denied (403)"
	case strings.Contains(msg, "401") || strings.Contains(msg, "Unauthorized"):
		return "unauthorized (401)"
	case strings.Contains(msg, "422"):
		return "unprocessable (422)"
	default:
		return "unreachable"
	}
}

// GetChangelog fetches commits between two tags/refs
func GetChangelog(repo, fromTag, toRef string) ([]ChangelogEntry, error) {
	if fromTag == "" || toRef == "" {
		return nil, nil
	}
	out, err := runGH("api",
		fmt.Sprintf("repos/%s/compare/%s...%s", repo, fromTag, toRef),
		"-q", `.commits[] | {sha: .sha[:7], message: (.commit.message | split("\n")[0]), author: .commit.author.name}`,
	)
	if err != nil {
		return nil, err
	}

	var entries []ChangelogEntry
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var e struct {
			SHA     string `json:"sha"`
			Message string `json:"message"`
			Author  string `json:"author"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, ChangelogEntry{SHA: e.SHA, Message: e.Message, Author: e.Author})
	}
	return entries, nil
}

// GetPreviousTag finds the tag before the given one
func GetPreviousTag(repo, currentTag string) (string, error) {
	out, err := runGH("api",
		fmt.Sprintf("repos/%s/tags?per_page=20", repo),
		"-q", ".[].name",
	)
	if err != nil {
		return "", err
	}
	tags := strings.Split(strings.TrimSpace(out), "\n")
	for i, t := range tags {
		if t == currentTag && i+1 < len(tags) {
			return tags[i+1], nil
		}
	}
	return "", nil
}

// ApproveDeployment approves a pending deployment environment
func ApproveDeployment(repo string, runID int64, envName, comment string) error {
	envID, err := getEnvironmentID(repo, runID, envName)
	if err != nil {
		return fmt.Errorf("resolving environment ID: %w", err)
	}
	_, err = runGH("api", "--method", "POST",
		fmt.Sprintf("repos/%s/actions/runs/%d/pending_deployments", repo, runID),
		"-f", fmt.Sprintf("environment_ids[]=%d", envID),
		"-f", "state=approved",
		"-f", fmt.Sprintf("comment=%s", comment),
	)
	return err
}

// RejectDeployment rejects a pending deployment environment
func RejectDeployment(repo string, runID int64, envName, comment string) error {
	envID, err := getEnvironmentID(repo, runID, envName)
	if err != nil {
		return fmt.Errorf("resolving environment ID: %w", err)
	}
	_, err = runGH("api", "--method", "POST",
		fmt.Sprintf("repos/%s/actions/runs/%d/pending_deployments", repo, runID),
		"-f", fmt.Sprintf("environment_ids[]=%d", envID),
		"-f", "state=rejected",
		"-f", fmt.Sprintf("comment=%s", comment),
	)
	return err
}

func getEnvironmentID(repo string, runID int64, envName string) (int, error) {
	out, err := runGH("api",
		fmt.Sprintf("repos/%s/actions/runs/%d/pending_deployments", repo, runID),
		"-q", fmt.Sprintf(`.[] | select(.environment.name == "%s") | .environment.id`, envName),
	)
	if err != nil {
		return 0, err
	}
	var id int
	fmt.Sscan(strings.TrimSpace(out), &id)
	if id == 0 {
		return 0, fmt.Errorf("environment %q not found in pending deployments", envName)
	}
	return id, nil
}

// DeploymentReview represents an approve/reject action recorded on GitHub.
type DeploymentReview struct {
	RunID        int64
	WorkflowName string
	Tag          string
	Branch       string
	Actor        string    // who triggered the workflow
	Reviewer     string    // who approved/rejected
	State        string    // "approved" | "rejected"
	Environment  string
	ReviewedAt   time.Time
}

// GetDeploymentReviews fetches recent deployment approvals/rejections from GitHub.
// It queries up to limit*3 runs and returns those that had review actions.
func GetDeploymentReviews(repo string, limit int) ([]DeploymentReview, error) {
	fetchLimit := limit * 3
	if fetchLimit > 100 {
		fetchLimit = 100
	}

	out, err := runGH("api",
		fmt.Sprintf("repos/%s/actions/runs?per_page=%d", repo, fetchLimit),
		"-q", `.workflow_runs[] | {id, name, display_title, head_branch, actor: .actor.login, updated_at}`,
	)
	if err != nil {
		return nil, fmt.Errorf("fetching workflow runs: %w", err)
	}

	var reviews []DeploymentReview

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || len(reviews) >= limit {
			break
		}

		var run struct {
			ID           int64     `json:"id"`
			Name         string    `json:"name"`
			DisplayTitle string    `json:"display_title"`
			HeadBranch   string    `json:"head_branch"`
			Actor        string    `json:"actor"`
			UpdatedAt    time.Time `json:"updated_at"`
		}
		if err := json.Unmarshal([]byte(line), &run); err != nil {
			continue
		}

		appOut, err := runGH("api",
			fmt.Sprintf("repos/%s/actions/runs/%d/approvals", repo, run.ID),
			"-q", `.[] | {state, user: .user.login, submitted_at, env: (.environments[0].name // "")}`,
		)
		if err != nil || strings.TrimSpace(appOut) == "" {
			continue
		}

		tag := extractTag(run.DisplayTitle, run.HeadBranch)

		for _, appLine := range strings.Split(strings.TrimSpace(appOut), "\n") {
			if appLine == "" {
				continue
			}
			var a struct {
				State       string    `json:"state"`
				User        string    `json:"user"`
				SubmittedAt time.Time `json:"submitted_at"`
				Env         string    `json:"env"`
			}
			if err := json.Unmarshal([]byte(appLine), &a); err != nil {
				continue
			}
			reviews = append(reviews, DeploymentReview{
				RunID:        run.ID,
				WorkflowName: run.Name,
				Tag:          tag,
				Branch:       run.HeadBranch,
				Actor:        run.Actor,
				Reviewer:     a.User,
				State:        a.State,
				Environment:  a.Env,
				ReviewedAt:   a.SubmittedAt,
			})
		}
	}

	return reviews, nil
}

func GetCurrentRepo() (string, error) {
	out, err := runGH("repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func GetCurrentUser() (string, error) {
	out, err := runGH("api", "user", "-q", ".login")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// detectReleaseType determines the release type from workflow_dispatch inputs.
//
// release.yml only has two release_type choices: "regular" and "hotfix".
// Preview is NOT a separate release_type — it is "regular" triggered from main
// with a tag that carries a "-preview" suffix. So the correct hierarchy is:
//
//  1. inputType == "hotfix"         → HOTFIX   (explicit, cherry-picked to release/vX.X.x)
//  2. tag ends with "-preview"      → PREVIEW  (regular deploy from main)
//  3. everything else               → REGULAR  (official release from release/vX.X.x)
//
// Fallback (empty inputType) handles re-deployed runs or manual workflow_dispatch
// where inputs may not be populated.
func detectReleaseType(tag, inputType string) ReleaseType {
	if strings.ToLower(inputType) == "hotfix" {
		return ReleaseHotfix
	}
	if strings.HasSuffix(tag, "-preview") {
		return ReleasePreview
	}
	return ReleaseRegular
}

func extractTag(displayTitle, branch string) string {
	for _, f := range strings.Fields(displayTitle) {
		if strings.HasPrefix(f, "v") {
			return f
		}
	}
	if strings.HasPrefix(branch, "release/v") {
		return strings.TrimSuffix(branch, ".x")
	}
	return ""
}

func firstLine(s string) string {
	if idx := strings.Index(s, "\n"); idx != -1 {
		return s[:idx]
	}
	return s
}

func runGH(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			// Show at most the first 2 args to avoid leaking long arg lists.
			prefix := args
			if len(prefix) > 2 {
				prefix = prefix[:2]
			}
			return "", fmt.Errorf("gh %v: %s", prefix, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}
