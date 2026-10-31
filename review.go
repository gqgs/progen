package progen

import (
	"fmt"
	"os"
	"path/filepath"
)

// RejectUnkept rejects unreviewed candidates, preserving explicit keep decisions.
// A nil ID list applies to the entire workspace.
func (e *Engine) RejectUnkept(ids []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	all, err := e.List()
	if err != nil {
		return 0, err
	}
	include := map[string]bool{}
	for _, id := range ids {
		include[id] = true
	}
	count := 0
	for _, c := range all {
		if (ids != nil && !include[c.ID]) || c.Feedback != "neutral" {
			continue
		}
		c.Feedback = "rejected"
		if err := writeJSON(filepath.Join(e.Workspace, "candidates", c.ID, "candidate.json"), c); err != nil {
			return count, err
		}
		count++
	}
	return count, e.rebuildArchives()
}

// Purge removes generated data only. Callers must stop generation first and
// prevent other writing processes from using the workspace.
func (e *Engine) Purge() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, name := range []string{"candidates", "cache", "archives", "jobs", "runs"} {
		path := filepath.Join(e.Workspace, name)
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("purge %s: %w", name, err)
		}
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
	}
	return nil
}
