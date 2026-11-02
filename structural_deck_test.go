package progen

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStructuralDeckExperiment(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t)
	for _, resolution := range []int{32, 64} {
		for _, seed := range []Seed{42, 314159, 14644082841431334434} {
			r := BridgeStructuralRecipe()
			r.Domain.Resolution = resolution
			r.Seed = seed
			if seed == 14644082841431334434 {
				raw, err := os.ReadFile("testdata/bridge-quantized-collapse.json")
				if err != nil {
					t.Fatal(err)
				}
				var exact Recipe
				if err = json.Unmarshal(raw, &exact); err != nil {
					t.Fatal(err)
				}
				for i := range r.Nodes {
					for _, n := range exact.Nodes {
						if n.ID == r.Nodes[i].ID {
							r.Nodes[i].Params = n.Params
						}
					}
				}
			}
			c, err := e.Evaluate(ctx, r, "", nil)
			if err != nil {
				t.Error(resolution, seed, err)
				continue
			}
			t.Logf("res=%d seed=%d valid=%v hard=%v walking=%+v triangles=%d", resolution, seed, c.Valid, c.Problems, c.WalkingChecks, c.Metrics.Triangles)
			if !c.Valid {
				t.Error("invalid structural result")
				continue
			}
			a, err := e.Artifact(c, "mesh")
			if err != nil {
				t.Fatal(err)
			}
			v, err := e.Artifact(c, "volume")
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := connectedAnchors(ctx, r.Source.Spec, v.Volume); err != nil || !ok {
				t.Fatal("disconnected endpoints", err)
			}
			ref, _ := e.Artifact(c, "reference_volume")
			if c.WalkingChecks == nil || !c.WalkingChecks.Evaluated || c.WalkingChecks.Passed || len(c.WalkingChecks.Problems) == 0 {
				t.Fatal("rough specimen mislabeled walkable")
			}
			strictProblems, err := ValidateConditioned(ctx, BridgeRecipe().Source.Spec, ref.Volume, v.Volume, a.Mesh)
			if err != nil || len(strictProblems) == 0 {
				t.Fatal("rough specimen admitted under original strict contract", err)
			}
			glb, err := EncodeCandidateGLB(a.Mesh, c)
			if err != nil {
				t.Fatal(err)
			}
			decoded := decodePrecisionExport(t, glb)
			if err = validateClosedSurface(decoded); err != nil {
				t.Fatal(err)
			}
			hard, err := ValidateConditioned(ctx, r.Source.Spec, ref.Volume, v.Volume, decoded)
			if err != nil || len(hard) > 0 {
				t.Fatal("exported hard contract", hard, err)
			}
			broken := *decoded
			broken.Indices = decoded.Indices[3:]
			hard, err = ValidateConditioned(ctx, r.Source.Spec, ref.Volume, v.Volume, &broken)
			if err != nil || !strings.Contains(strings.Join(hard, ";"), "manifold") {
				t.Fatal("relaxed policy admitted a broken mesh", hard, err)
			}
			if !conditionedMeshMatches(decoded, a.Mesh) {
				t.Fatal("export identity")
			}
			again, err := e.Evaluate(ctx, r, "", nil)
			if err != nil || !reflect.DeepEqual(c.Artifacts, again.Artifacts) || !reflect.DeepEqual(c.WalkingChecks, again.WalkingChecks) {
				t.Fatal("cached replay differs", err)
			}
			if folder := os.Getenv("PROGEN_STRUCTURAL_EVIDENCE"); folder != "" {
				if err = os.MkdirAll(folder, 0755); err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("structural-%d-%d", resolution, seed)
				if err = os.WriteFile(filepath.Join(folder, name+".glb"), glb, 0600); err != nil {
					t.Fatal(err)
				}
				b, err := json.MarshalIndent(c, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(folder, name+".json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestStructuralDeckControlsAndPolicy(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t)
	r := BridgeStructuralRecipe()
	if r.Source.SidecarSHA256 == BridgeRecipe().Source.SidecarSHA256 || r.Source.SidecarSHA256 == BridgeReliefRecipe().Source.SidecarSHA256 {
		t.Fatal("contract identity reused")
	}
	strict := BridgeRecipe().Source.Spec
	if strict.DeckPolicy != "" || BridgeReliefRecipe().Source.Spec.DeckPolicy != "" {
		t.Fatal("strict policy relaxed")
	}
	bad := r.Source.Spec
	bad.DeckPolicy = "ignore-all"
	if bad.Validate() == nil {
		t.Fatal("unknown policy accepted")
	}
	bad = r.Source.Spec
	bad.Anchors = nil
	if bad.Validate() == nil {
		t.Fatal("missing endpoint contract accepted")
	}
	for _, resolution := range []int{32, 64} {
		for _, params := range []map[string]float64{{"density": 0, "contour": 0}, {"density": .18, "contour": 1}} {
			candidate := cloneRecipe(r)
			candidate.Domain.Resolution = resolution
			for i := range candidate.Nodes {
				if candidate.Nodes[i].ID == "deck" {
					candidate.Nodes[i].Params = params
				}
			}
			c, err := e.Evaluate(ctx, candidate, "", nil)
			if err != nil || !c.Valid {
				t.Fatal("extreme", resolution, params, c, err)
			}
			volume, _ := e.Artifact(c, "volume")
			cut := &VolumeGrid{Space: volume.Volume.Space, Values: append([]float64(nil), volume.Volume.Values...)}
			n := resolution
			for i := range cut.Values {
				p := cut.Space.Point(i%n, (i/n)%n, i/(n*n))
				if p.X > -.25 && p.X < .25 {
					cut.Values[i] = 1
				}
			}
			if ok, err := connectedAnchors(ctx, candidate.Source.Spec, cut); err != nil || ok {
				t.Fatal("disconnected endpoint chain accepted", err)
			}
		}
	}
	for _, key := range []string{"density", "contour"} {
		if !e.Registry.entries["structural_deck"].desc.Parameters[key].Mutable {
			t.Fatal("structural controls cannot vary in batches")
		}
	}
	mutated := e.mutate(r, rand.New(rand.NewPCG(23, 47)), .5, true)
	for _, node := range mutated.Nodes {
		if node.ID == "deck" && node.Params["density"] == .09 && node.Params["contour"] == 1 {
			t.Fatal("batch did not vary structural controls")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.Evaluate(cancelled, r, "", nil); err != context.Canceled {
		t.Fatal("cancelled generation continued", err)
	}
	v := &VolumeGrid{Space: r.Domain, Values: make([]float64, r.Domain.Resolution*r.Domain.Resolution*r.Domain.Resolution)}
	op := structuralDeckOperator{density: .09, contour: 1, source: r.Source}
	if _, err := op.Run(cancelled, r.Domain, 0, Inputs{"field": {Kind: "volume", Volume: v}}); err != context.Canceled {
		t.Fatal("operator ignored cancellation", err)
	}
	for _, op := range []structuralDeckOperator{{density: -1, source: r.Source}, {density: .19, source: r.Source}, {contour: 2, source: r.Source}, {density: .09}} {
		if _, err := op.Run(ctx, r.Domain, 0, Inputs{"field": {Kind: "volume", Volume: v}}); err == nil {
			t.Fatal("invalid controls/source admitted")
		}
	}
}
