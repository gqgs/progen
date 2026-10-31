package progen

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBridgeBatchExportReplay(t *testing.T) {
	ctx := context.Background()
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Whitespace is semantically irrelevant but part of exact source identity.
	sidecar := append(append([]byte{}, bridgeSidecar...), '\n')
	imported, err := e.ImportArchetype(ctx, bridgeGLB, sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Source.SidecarSHA256 == BridgeRecipe().Source.SidecarSHA256 {
		t.Fatal("raw sidecar bytes absent from fingerprint")
	}
	for _, resolution := range []int{32, 64} {
		r := cloneRecipe(imported)
		r.Domain.Resolution = resolution
		list, failures, err := e.RunWithFailures(ctx, RunRequest{Kind: "generate", Recipe: &r, Count: 2, SearchSeed: 12, Workers: 2}, nil)
		if err != nil || len(list) != 2 || len(failures) != 0 {
			t.Fatalf("%d batch: %v %v", resolution, err, failures)
		}
		if list[0].Artifacts["mesh"].Hash == list[1].Artifacts["mesh"].Hash {
			t.Fatal("variation has identical geometry")
		}
		for _, c := range list {
			if !c.Valid {
				t.Fatalf("%d invalid %v", resolution, c.Problems)
			}
			again, err := e.Evaluate(ctx, c.Recipe, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if c.ID != again.ID || !reflect.DeepEqual(c.Artifacts, again.Artifacts) {
				t.Fatal("deterministic replay changed geometry")
			}
			for _, n := range again.Nodes {
				if !n.Cached {
					t.Fatalf("uncached replay node %s", n.ID)
				}
			}
		}
		c := list[0]
		var export bytes.Buffer
		if err = e.Export(ctx, c.ID, &export); err != nil {
			t.Fatal(err)
		}
		z, err := zip.NewReader(bytes.NewReader(export.Bytes()), int64(export.Len()))
		if err != nil {
			t.Fatal(err)
		}
		files := map[string][]byte{}
		for _, f := range z.File {
			reader, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			files[f.Name], err = io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(files["source/archetype.glb"], bridgeGLB) || !bytes.Equal(files["source/constraints.json"], sidecar) {
			t.Fatal("export lost exact source bytes")
		}
		raw := files["asset.glb"]
		length := binary.LittleEndian.Uint32(raw[12:16])
		var doc struct {
			Extras struct {
				Progen struct {
					Recipe Recipe `json:"recipe"`
				} `json:"progen"`
			} `json:"extras"`
		}
		if err = json.Unmarshal(raw[20:20+length], &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Extras.Progen.Recipe.Source.GLBSHA256 != c.Recipe.Source.GLBSHA256 {
			t.Fatal("GLB lost provenance")
		}
		fresh, err := NewEngine(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fresh.ImportArchetype(ctx, files["source/archetype.glb"], files["source/constraints.json"]); err != nil {
			t.Fatal(err)
		}
		var replay Recipe
		if err = json.Unmarshal(files["recipe.json"], &replay); err != nil {
			t.Fatal(err)
		}
		restored, err := fresh.Evaluate(ctx, replay, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if restored.ID != c.ID || !reflect.DeepEqual(restored.Artifacts, c.Artifacts) {
			t.Fatal("fresh workspace source reload changed identity")
		}
	}
}

func TestBridgeSourceIntegrityAndBudgets(t *testing.T) {
	ctx := context.Background()
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sidecar := append(append([]byte{}, bridgeSidecar...), ' ')
	r, err := e.ImportArchetype(ctx, bridgeGLB, sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.Prepare(RunRequest{Kind: "search", Recipe: &r, Count: 1}); err == nil || !strings.Contains(err.Error(), "separate milestone") {
		t.Fatal("conditioned search was silently enabled", err)
	}
	if _, _, err = e.Prepare(RunRequest{Kind: "generate", Recipe: &r, Count: 33}); err == nil {
		t.Fatal("ignored source batch budget")
	}
	changed := cloneRecipe(r)
	changed.Source.Spec.Traversal.Width = .4
	if _, err = e.Evaluate(ctx, changed, "", nil); err == nil || !strings.Contains(err.Error(), "semantics differ") {
		t.Fatal("edited constraints bypassed fingerprint", err)
	}
	changed = cloneRecipe(r)
	changed.Outputs["reference_volume"] = Reference{"deform", "volume"}
	if _, _, err = e.Registry.Compile(changed); err == nil {
		t.Fatal("untyped source output accepted")
	}
	if err = os.WriteFile(filepath.Join(e.Workspace, "sources", r.Source.SidecarSHA256+".json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Evaluate(ctx, r, "", nil); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatal("corrupted source reused", err)
	}
	// Cancellation does not leave a source record or partially evaluate a candidate.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = e.ImportArchetype(cancelled, bridgeGLB, bridgeSidecar); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestBridgeInvalidCandidatesRemainInspectable(t *testing.T) {
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := BridgeRecipe().Source.Spec
	s.Budget.MaxTriangles = 1
	raw, _ := json.Marshal(s)
	r, err := e.ImportArchetype(context.Background(), bridgeGLB, raw)
	if err != nil {
		t.Fatal(err)
	}
	cs, failures, err := e.RunWithFailures(context.Background(), RunRequest{Kind: "generate", Recipe: &r, Count: 2, SearchSeed: 12}, nil)
	if err != nil || len(cs) != 2 || len(failures) != 0 {
		t.Fatal(err, failures)
	}
	for _, c := range cs {
		if c.Valid || !strings.Contains(strings.Join(c.Problems, ";"), "triangle budget exceeded") {
			t.Fatal(c.Problems)
		}
		if _, err = e.Artifact(c, "mesh"); err != nil {
			t.Fatal("invalid mesh cannot be inspected", err)
		}
		if err = e.Export(context.Background(), c.ID, io.Discard); err == nil {
			t.Fatal("invalid candidate exported")
		}
	}
	a, err := e.Archive(r)
	if err != nil || len(a.Cells) != 0 {
		t.Fatal("invalid bridge admitted", err)
	}
}

func TestBridgeSourceLibraryBound(t *testing.T) {
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.Workspace, "sources")
	if err = os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "existing-asset"))
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(sourceStoreLimit)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.ImportArchetype(context.Background(), bridgeGLB, bridgeSidecar); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Fatal("ignored source library bound", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("over-budget import left source files", err)
	}
}

func TestBridgeBoundedSourceReload(t *testing.T) {
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".glb", ".json"} {
		r := BridgeRecipe()
		digest := strings.Repeat("a", 64)
		if ext == ".glb" {
			r.Source.GLBSHA256 = digest
		} else {
			r.Source.SidecarSHA256 = digest
		}
		dir := filepath.Join(e.Workspace, "sources")
		if err = os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, digest+ext))
		if err != nil {
			t.Fatal(err)
		}
		err = f.Truncate(128 << 20)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = e.sourceBytes(r.Source); err == nil || !strings.Contains(err.Error(), "reload limit") {
			t.Fatal("unbounded source reload", ext, err)
		}
	}
}

func TestBridgeTransformedSourceReload(t *testing.T) {
	ctx := context.Background()
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := BridgeRecipe().Source.Spec
	spec.Transform = ImportTransform{Scale: .99, Translation: Vec3{.02, 0, 0}}
	raw, _ := json.Marshal(spec)
	r, err := e.ImportArchetype(ctx, bridgeGLB, raw)
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.Evaluate(ctx, r, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Valid {
		t.Fatal(c.Problems)
	}
	reference, err := e.Artifact(c, "reference")
	if err != nil {
		t.Fatal(err)
	}
	want := Vec3{-3.8, -.4, -.95}.Mul(.99).Add(Vec3{.02, 0, 0})
	if reference.Mesh.Positions[0].Sub(want).Len() > 1e-6 {
		t.Fatal("lost normalization transform")
	}
	fresh, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.ImportArchetype(ctx, bridgeGLB, raw); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(c.Recipe)
	var replay Recipe
	json.Unmarshal(encoded, &replay)
	again, err := fresh.Evaluate(ctx, replay, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Valid || again.ID != c.ID || !reflect.DeepEqual(again.Artifacts, c.Artifacts) || again.Recipe.Source.Spec.Transform != spec.Transform {
		t.Fatal("transformed replay changed geometry or provenance", again.Problems)
	}
	if again.Recipe.Source.GLBSHA256 != sourceDigest(bridgeGLB) || again.Recipe.Source.SidecarSHA256 != sourceDigest(raw) {
		t.Fatal("source fingerprints lost")
	}
}
