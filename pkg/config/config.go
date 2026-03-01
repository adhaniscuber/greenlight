package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Role string

const (
	RoleEngineer Role = "engineer"
	RoleTechLead Role = "techlead"
)

type EnvFilter struct {
	Staging    bool `json:"staging"`
	Production bool `json:"production"`
}

type Config struct {
	GitHubUser   string    `json:"github_user"`
	Role         Role      `json:"role"`
	EnvFilter    EnvFilter `json:"env_filter"`
	PollInterval int       `json:"poll_interval"`
	DefaultRepo  string    `json:"default_repo,omitempty"` // legacy single-repo, kept for compat
	Repos        []string  `json:"repos,omitempty"`        // multi-repo list (preferred)
}

// AllRepos returns repos to watch. Prefers Repos slice over legacy DefaultRepo.
func (c Config) AllRepos() []string {
	if len(c.Repos) > 0 {
		return c.Repos
	}
	if c.DefaultRepo != "" {
		return []string{c.DefaultRepo}
	}
	return nil
}

func defaultConfig() Config {
	return Config{
		Role:         RoleEngineer,
		PollInterval: 600, // daemon polls every 10 minutes by default
		EnvFilter:    EnvFilter{Staging: true, Production: false},
	}
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "greenlight")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func Load() (Config, error) {
	path, err := configPath()
	if err != nil {
		return defaultConfig(), err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := defaultConfig()
		if saveErr := Save(cfg); saveErr != nil {
			// Config file couldn't be persisted; return defaults but surface the error.
			return cfg, fmt.Errorf("creating default config: %w", saveErr)
		}
		return cfg, nil
	}
	if err != nil {
		return defaultConfig(), err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultConfig(), err
	}
	return cfg, nil
}

func Save(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

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

func ConfigFilePath() string {
	p, _ := configPath()
	return p
}

func (c Config) Summary() string {
	return fmt.Sprintf("user=%s role=%s envs=%v", c.GitHubUser, c.Role, c.AllowedEnvironments())
}
