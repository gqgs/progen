package progen

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFloorReliefExperiment(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t)
	for _, resolution := range []int{32, 64} {
		for _, seed := range []Seed{42, 314159, 14644082841431334434} {
			for _, strength := range []float64{0, 1} {
				r := BridgeReliefRecipe()
				r.Domain.Resolution = resolution
				r.Seed = seed
				// Replay the actual previously failing wall recipe, not only its seed.
				if seed == 14644082841431334434 {
					raw, _ := os.ReadFile("testdata/bridge-quantized-collapse.json")
					var exact Recipe
					json.Unmarshal(raw, &exact)
					for i := range r.Nodes {
						for _, n := range exact.Nodes {
							if n.ID == r.Nodes[i].ID {
								r.Nodes[i].Params = n.Params
							}
						}
					}
				}
				for i := range r.Nodes {
					if r.Nodes[i].ID == "floor" {
						r.Nodes[i].Params["strength"] = strength
					}
				}
				c, err := e.Evaluate(ctx, r, "", nil)
				if err != nil {
					t.Fatal(resolution, seed, strength, err)
				}
				if !c.Valid {
					t.Error(resolution, seed, strength, c.Problems)
					continue
				}
				a, err := e.Artifact(c, "mesh")
				if err != nil {
					t.Fatal(err)
				}
				again, err := e.Evaluate(ctx, r, "", nil)
				if err != nil || !reflect.DeepEqual(c.Artifacts, again.Artifacts) {
					t.Fatal("cached replay differs", err)
				}
				for _, n := range again.Nodes {
					if !n.Cached {
						t.Fatal("uncached replay", n.ID)
					}
				}
				v, _ := e.Artifact(c, "volume")
				ref, _ := e.Artifact(c, "reference_volume")
				if strength == 0 {
					legacy := BridgeRecipe()
					legacy.Domain = r.Domain
					legacy.Seed = r.Seed
					for i := range legacy.Nodes {
						for _, node := range r.Nodes {
							if legacy.Nodes[i].ID == node.ID {
								legacy.Nodes[i].Params = node.Params
							}
						}
					}
					baseline, err := e.Evaluate(ctx, legacy, "", nil)
					if err != nil || !baseline.Valid {
						t.Fatal("legacy baseline", err)
					}
					baseMesh, _ := e.Artifact(baseline, "mesh")
					baseVolume, _ := e.Artifact(baseline, "volume")
					if !reflect.DeepEqual(a.Mesh, baseMesh.Mesh) || !reflect.DeepEqual(v.Volume, baseVolume.Volume) {
						t.Fatal("off changed legacy geometry")
					}
				}
				glb, err := EncodeCandidateGLB(a.Mesh, c)
				if err != nil {
					t.Fatal(err)
				}
				decoded := decodePrecisionExport(t, glb)
				if !conditionedMeshMatches(decoded, a.Mesh) {
					t.Fatal("export/canonical mismatch")
				}
				problems, err := ValidateConditioned(ctx, r.Source.Spec, ref.Volume, v.Volume, decoded)
				if err != nil || len(problems) > 0 {
					t.Fatal("exported constraints", problems, err)
				}
				q, err := quantizedConditionedMesh(a.Mesh, r.Domain)
				if err != nil {
					t.Fatal(err)
				}
				if err = validateClosedSurface(q); err != nil {
					t.Error(resolution, seed, strength, err)
				}
				lo, hi, minNormal := math.Inf(1), math.Inf(-1), 1.0
				for i := 0; i < len(q.Indices); i += 3 {
					p0, p1, p2 := q.Positions[q.Indices[i]], q.Positions[q.Indices[i+1]], q.Positions[q.Indices[i+2]]

					if triangleBoxInterior([3]Vec3{p0, p1, p2}, Box{Vec3{-3.4, -.1, -.6}, Vec3{3.4, .1, .6}}, 1e-9) {
						normal := p1.Sub(p0).Cross(p2.Sub(p0))
						ny := normal.Y / normal.Len()
						if ny > 0 {
							lo = min(lo, p0.Y, p1.Y, p2.Y)
							hi = max(hi, p0.Y, p1.Y, p2.Y)
							minNormal = min(minNormal, ny)
						}
					}
				}
				if lo < -.08 || hi > .1 || minNormal < .7 {
					t.Error("walking strip height/slope", resolution, seed, strength, lo, hi, minNormal)
				}
				if strength > 0 && hi-lo < .04 {
					t.Error("relief is not geometrically meaningful", hi-lo)
				}
				t.Logf("res=%d seed=%d strength=%g valid=%v heights=[%.5f,%.5f] minUp=%.5f faces=%d", resolution, seed, strength, c.Valid, lo, hi, minNormal, len(q.Indices)/3)
				if folder := os.Getenv("PROGEN_FLOOR_EVIDENCE"); folder != "" {
					os.MkdirAll(folder, 0755)
					name := fmt.Sprintf("floor-%d-%d-%g", resolution, seed, strength)
					glb, _ := EncodeCandidateGLB(a.Mesh, c)
					os.WriteFile(filepath.Join(folder, name+".glb"), glb, 0600)
					data, _ := json.MarshalIndent(c, "", "  ")
					os.WriteFile(filepath.Join(folder, name+".json"), data, 0600)
				}
			}
		}
	}
}

func TestFloorReliefBoundsAndControls(t *testing.T) {
	ctx := context.Background()
	for _, resolution := range []int{32, 64} {
		r := BridgeReliefRecipe()
		r.Domain.Resolution = resolution
		s := r.Source.Spec
		v := &VolumeGrid{Space: r.Domain, Values: make([]float64, resolution*resolution*resolution)}
		field := &VolumeGrid{Space: r.Domain, Values: make([]float64, len(v.Values))}
		for i := range v.Values {
			p := r.Domain.Point(i%resolution, (i/resolution)%resolution, i/(resolution*resolution))
			v.Values[i] = max(p.Y, -.4-p.Y)
			field.Values[i] = .15 * math.Sin(p.X*3) * math.Cos(p.Z*3)
		}
		original := append([]float64(nil), v.Values...)
		var full *VolumeGrid
		for _, controls := range [][2]float64{{0, .06}, {1, 0}, {.5, .03}, {1, .06}} {
			out, err := floorRelief(ctx, v, field, s, controls[0], controls[1])
			if err != nil {
				t.Fatal(err)
			}
			if controls[0] == 0 || controls[1] == 0 {
				if !reflect.DeepEqual(out, v) {
					t.Fatal("off changed samples")
				}
				continue
			}
			changed := 0
			for i, value := range out.Values {
				p := r.Domain.Point(i%resolution, (i/resolution)%resolution, i/(resolution*resolution))
				if value != v.Values[i] {
					changed++
					if !s.FloorRelief.Region.Contains(p) || p.Y <= -.16 {
						t.Fatal("changed outside deformation band", p)
					}
				}
				if regionValue(p, s.Required) < 0 && value >= 0 {
					t.Fatal("required core removed")
				}
				if regionValue(p, s.Forbidden) < 0 && value < 0 {
					t.Fatal("clearance occupied")
				}
			}
			if changed == 0 {
				t.Fatal("controls had no effect")
			}
			if controls[0] == 1 {
				full = out
			}
		}
		if !reflect.DeepEqual(v.Values, original) {
			t.Fatal("mutated input")
		}
		// Changing the shared wall field changes the floor with the same controls.
		for i := range field.Values {
			field.Values[i] *= -1
		}
		opposite, err := floorRelief(ctx, v, field, s, 1, .06)
		if err != nil || reflect.DeepEqual(full, opposite) {
			t.Fatal("floor ignores shared field", err)
		}
		for _, controls := range [][2]float64{{-1, .06}, {1.01, .06}, {1, -.01}, {1, .061}, {math.NaN(), .06}} {
			if _, err := floorRelief(ctx, v, field, s, controls[0], controls[1]); err == nil {
				t.Fatal("invalid controls accepted", controls)
			}
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := floorRelief(cancelled, v, field, s, 1, .06); err != context.Canceled {
			t.Fatal("ignored cancellation", err)
		}
	}
}

func TestFloorReliefContractIdentity(t *testing.T) {
	old, new := BridgeRecipe(), BridgeReliefRecipe()
	if old.Source.Spec.FloorRelief != nil || new.Source.Spec.FloorRelief == nil || old.Source.SidecarSHA256 == new.Source.SidecarSHA256 {
		t.Fatal("floor authorization missing from source identity")
	}
	if !reflect.DeepEqual(old.Source.Spec.Required, new.Source.Spec.Required) || !reflect.DeepEqual(old.Source.Spec.Anchors, new.Source.Spec.Anchors) || !reflect.DeepEqual(old.Source.Spec.Forbidden, new.Source.Spec.Forbidden) || old.Source.Spec.Traversal != new.Source.Spec.Traversal {
		t.Fatal("protected contract changed")
	}
	// An edited in-memory contract cannot retain the old source fingerprint.
	e := testEngine(t)
	new.Source.SidecarSHA256 = old.Source.SidecarSHA256
	if _, err := e.Evaluate(context.Background(), new, "", nil); err == nil {
		t.Fatal("forged floor authorization accepted")
	}
	s := BridgeReliefRecipe().Source.Spec
	s.FloorRelief.Region.Max.Y = .01
	if err := s.Validate(); err == nil {
		t.Fatal("unresolved floor band accepted")
	}
}
