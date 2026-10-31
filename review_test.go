package progen

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMalformedMeshCacheRegenerates(t *testing.T) {
	e := testEngine(t)
	c, err := e.Evaluate(context.Background(), smallRecipe(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := c.Artifacts["mesh"]
	out, err := e.readCache(ref.Key)
	if err != nil {
		t.Fatal(err)
	}
	out[ref.Port].Mesh.Indices[0] = uint32(len(out[ref.Port].Mesh.Positions))
	// Write a valid checksum envelope containing semantically invalid topology.
	if err := e.writeCache(ref.Key, out); err != nil {
		t.Fatal(err)
	}
	repaired, err := e.Evaluate(context.Background(), smallRecipe(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired.Valid {
		t.Fatal(repaired.Problems)
	}
	for _, node := range repaired.Nodes {
		if node.CacheKey == ref.Key && node.Cached {
			t.Fatal("invalid mesh cache reused")
		}
	}
	if repaired.Artifacts["mesh"].Hash != c.Artifacts["mesh"].Hash {
		t.Fatal("repair changed deterministic geometry")
	}
}

func TestReviewRandomizationAndPurge(t *testing.T) {
	e := testEngine(t)
	s, err := NewServer(e)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	recipe := smallRecipe()
	c, err := e.Evaluate(context.Background(), recipe, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	recipe.Seed++
	kept, err := e.Evaluate(context.Background(), recipe, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Feedback(kept.ID, "favorite"); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", "/api/candidates/reject-unkept", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_, removedErr := e.Get(c.ID)
	kept, _ = e.Get(kept.ID)
	if !os.IsNotExist(removedErr) || kept.Feedback != "favorite" {
		t.Fatal("bulk reject lost keep decision")
	}
	body, _ := json.Marshal(recipe)
	for i := 0; i < 10; i++ {
		w := request("POST", "/api/recipe/randomize", string(body))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var randomized Recipe
		if err := json.Unmarshal(w.Body.Bytes(), &randomized); err != nil {
			t.Fatal(err)
		}
		if randomized.Seed == recipe.Seed {
			t.Fatal("seed unchanged")
		}
		if _, _, err := e.Registry.Compile(randomized); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"cache", "candidates", "jobs", "archives", "runs"} {
		if err := writeJSON(filepath.Join(e.Workspace, dir, "purge-fixture.json"), true); err != nil {
			t.Fatal(err)
		}
	}
	if w := request("DELETE", "/api/generations", "{}"); w.Code != 400 {
		t.Fatal("purge accepted without confirmation")
	}
	s.cancels["busy"] = func() {}
	if w := request("DELETE", "/api/generations", `{"confirm":"purge-all-generations"}`); w.Code != 409 {
		t.Fatal("purge accepted while busy")
	}
	delete(s.cancels, "busy")
	// Delete the intentionally non-candidate fixture before exercising normal reads.
	if err := os.Remove(filepath.Join(e.Workspace, "candidates", "purge-fixture.json")); err != nil {
		t.Fatal(err)
	}
	if w := request("DELETE", "/api/generations", `{"confirm":"purge-all-generations"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, dir := range []string{"cache", "candidates", "jobs", "archives", "runs"} {
		entries, err := os.ReadDir(filepath.Join(e.Workspace, dir))
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s not empty: %v", dir, err)
		}
	}
	if w := request("GET", "/api/candidates", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String())
	}
	if _, err := e.Evaluate(context.Background(), smallRecipe(), "", nil); err != nil {
		t.Fatal("generation after purge:", err)
	}
}

// This recipe failed with "mesh index out of bounds" in a user workspace.
// It exercises a 96^3 volume and indices beyond the signed 16-bit boundary.
func TestSpecimenA0477AFreshAndCachedMesh(t *testing.T) {
	raw, err := os.ReadFile("testdata/a0477a-recipe.json")
	if err != nil {
		t.Fatal(err)
	}
	var recipe Recipe
	if err := json.Unmarshal(raw, &recipe); err != nil {
		t.Fatal(err)
	}
	e := testEngine(t)
	cleanMeshHash := ""
	for attempt := 0; attempt < 3; attempt++ {
		c, err := e.Evaluate(context.Background(), recipe, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Valid {
			t.Fatalf("attempt %d: %v", attempt, c.Problems)
		}
		if attempt == 0 {
			cleanMeshHash = c.Artifacts["mesh"].Hash
		}
		if c.Artifacts["mesh"].Hash != cleanMeshHash {
			t.Fatal("cache repair changed the regenerated mesh")
		}
		if c.Metrics.Vertices != 37562 || c.Metrics.Triangles != 75220 {
			t.Fatalf("recipe geometry changed: %+v", c.Metrics)
		}
		if got := c.Artifacts["volume"].Hash; got != "375769b2ff77e8a515693d7df4a098a7b3f13f60bf0c2593ce16fa67566dad2e" {
			t.Fatalf("volume differs from original specimen: %s", got)
		}
		a, err := e.Artifact(c, "mesh")
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateMesh(a.Mesh, c.Recipe.Domain); err != nil {
			t.Fatal(err)
		}
		for _, node := range c.Nodes {
			if node.ID == "surface" && node.Cached != (attempt == 2) {
				t.Fatal("unexpected cache state")
			}
		}
		if attempt == 0 {
			// Replay the 118 actual damaged indices from the uploaded artifact.
			// Positions and normals matched regeneration exactly.
			raw, err := os.ReadFile("testdata/a0477a-corrupt-indices.json")
			if err != nil {
				t.Fatal(err)
			}
			var damaged [][2]uint32
			if err := json.Unmarshal(raw, &damaged); err != nil {
				t.Fatal(err)
			}
			if len(damaged) != 118 {
				t.Fatal("incomplete corruption fixture")
			}
			ref := c.Artifacts["mesh"]
			out, err := e.readCache(ref.Key)
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range damaged {
				out[ref.Port].Mesh.Indices[change[0]] = change[1]
			}
			if err := ValidateMesh(out[ref.Port].Mesh, c.Recipe.Domain); err == nil {
				t.Fatal("actual corrupt indices accepted")
			}
			if err := e.writeCache(ref.Key, out); err != nil {
				t.Fatal(err)
			}
		}
	}
}
