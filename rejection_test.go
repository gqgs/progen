package progen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectPurgesExclusiveArtifactsAndPreservesSharedCache(t *testing.T) {
	e := testEngine(t)
	r := smallRecipe()
	a, err := e.Evaluate(context.Background(), r, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Nodes[4].Params["amount"] = .2
	b, err := e.Evaluate(context.Background(), r, a.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Feedback(b.ID, "favorite"); err != nil {
		t.Fatal(err)
	}
	history := map[string]any{"candidates": []string{a.ID, b.ID}, "request": map[string]string{"parent": a.ID}}
	for _, dir := range []string{"jobs", "runs"} {
		if err := writeJSON(filepath.Join(e.Workspace, dir, "mixed.json"), history); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Feedback(a.ID, "rejected"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Workspace, "candidates", a.ID)); !os.IsNotExist(err) {
		t.Fatal("candidate directory survived")
	}
	keep := map[string]bool{}
	for _, n := range b.Nodes {
		keep[n.CacheKey] = true
	}
	exclusive, shared := 0, 0
	for _, n := range a.Nodes {
		_, err := os.Stat(filepath.Join(e.Workspace, "cache", n.CacheKey+".json.gz"))
		if keep[n.CacheKey] {
			shared++
			if err != nil {
				t.Fatal("shared artifact removed", err)
			}
		} else {
			exclusive++
			if !os.IsNotExist(err) {
				t.Fatal("exclusive artifact retained")
			}
		}
	}
	if exclusive == 0 || shared == 0 {
		t.Fatal("fixture lacks both shared and exclusive artifacts")
	}
	b, err = e.Get(b.ID)
	if err != nil || b.Parent != "" {
		t.Fatal("stale parent reference", err)
	}
	for name := range b.Artifacts {
		if _, err := e.Artifact(b, name); err != nil {
			t.Fatal("survivor artifact broken", name, err)
		}
	}
	for _, dir := range []string{"jobs", "runs"} {
		data, err := os.ReadFile(filepath.Join(e.Workspace, dir, "mixed.json"))
		if err != nil || strings.Contains(string(data), a.ID) || !strings.Contains(string(data), b.ID) {
			t.Fatal("history cleanup", dir, err)
		}
	}
	if err := e.Feedback(b.ID, "rejected"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"jobs", "runs"} {
		if _, err := os.Stat(filepath.Join(e.Workspace, dir, "mixed.json")); !os.IsNotExist(err) {
			t.Fatal("empty history retained")
		}
	}
}
