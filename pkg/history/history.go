package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Action string

const (
	ActionApproved Action = "approved"
	ActionRejected Action = "rejected"
)

type Entry struct {
	Timestamp   time.Time `json:"timestamp"`
	Action      Action    `json:"action"`
	RunID       int64     `json:"run_id"`
	Environment string    `json:"environment"`
	Workflow    string    `json:"workflow"`
	Tag         string    `json:"tag"`
	Branch      string    `json:"branch"`
	Actor       string    `json:"actor"`
	ApprovedBy  string    `json:"approved_by"`
	Comment     string    `json:"comment,omitempty"`
}

func historyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "greenlight")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "history.json"), nil
}

func Load() ([]Entry, error) {
	path, err := historyPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func Append(e Entry) error {
	entries, err := Load()
	if err != nil {
		return fmt.Errorf("loading history before append: %w", err)
	}
	entries = append([]Entry{e}, entries...) // prepend (newest first)

	// Keep last 200 entries
	if len(entries) > 200 {
		entries = entries[:200]
	}

	path, err := historyPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Recent returns the N most recent entries, optionally filtered by env.
func Recent(n int, env string) ([]Entry, error) {
	entries, err := Load()
	if err != nil {
		return nil, err
	}
	var result []Entry
	for _, e := range entries {
		if env != "" && e.Environment != env {
			continue
		}
		result = append(result, e)
		if len(result) >= n {
			break
		}
	}
	return result, nil
}
