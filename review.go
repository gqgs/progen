package progen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RejectUnkept permanently removes every unkept specimen in the selection.
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
	removed := map[string]bool{}
	for _, c := range all {
		if (ids == nil || include[c.ID]) && c.Feedback != "favorite" {
			removed[c.ID] = true
		}
	}
	return len(removed), e.deleteCandidates(all, removed)
}

// Call with e.mu held and generation stopped. Cache entries can be shared by
// different specimens, including intermediate node outputs.
func (e *Engine) deleteCandidates(all []Candidate, removed map[string]bool) error {
	if len(removed) == 0 {
		return nil
	}
	retained, reclaim := map[string]bool{}, map[string]bool{}
	for _, c := range all {
		keys := retained
		if removed[c.ID] {
			keys = reclaim
		}
		for _, n := range c.Nodes {
			keys[n.CacheKey] = true
		}
		for _, a := range c.Artifacts {
			keys[a.Key] = true
		}
	}
	for id := range removed {
		if !safeID(id) {
			return fmt.Errorf("invalid candidate ID")
		}
		if err := os.RemoveAll(filepath.Join(e.Workspace, "candidates", id)); err != nil {
			return err
		}
	}
	for key := range reclaim {
		if retained[key] || !safeID(key) {
			continue
		}
		if err := os.Remove(filepath.Join(e.Workspace, "cache", key+".json.gz")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, c := range all {
		if !removed[c.ID] && removed[c.Parent] {
			c.Parent = ""
			if err := writeJSON(filepath.Join(e.Workspace, "candidates", c.ID, "candidate.json"), c); err != nil {
				return err
			}
		}
	}
	// Retain shared batch records, but remove all references to deleted specimens.
	for _, dir := range []string{"jobs", "runs"} {
		entries, err := os.ReadDir(filepath.Join(e.Workspace, dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(e.Workspace, dir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var record map[string]any
			if err := json.Unmarshal(data, &record); err != nil {
				return err
			}
			changed := false
			var scrub func(any) any
			scrub = func(value any) any {
				switch v := value.(type) {
				case map[string]any:
					for key, item := range v {
						if id, ok := item.(string); ok && removed[id] {
							delete(v, key)
							changed = true
						} else {
							v[key] = scrub(item)
						}
					}
				case []any:
					result := []any{}
					for _, item := range v {
						if id, ok := item.(string); ok && removed[id] {
							changed = true
							continue
						}
						result = append(result, scrub(item))
					}
					return result
				}
				return value
			}
			scrub(record)
			if !changed {
				continue
			}
			if ids, ok := record["candidates"].([]any); ok && len(ids) == 0 {
				if err := os.Remove(path); err != nil {
					return err
				}
			} else if err := writeJSON(path, record); err != nil {
				return err
			}
		}
	}
	if err := os.RemoveAll(filepath.Join(e.Workspace, "archives")); err != nil {
		return err
	}
	return e.rebuildArchives()
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
