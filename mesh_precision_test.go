package progen

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func precisionDomain() Domain {
	d := DefaultRecipe().Domain
	d.Box = Box{Vec3{-10, -10, -10}, Vec3{10, 10, 10}}
	return d
}

// A tetrahedron with an edge split a billionth of a unit from its endpoint.
// Both incident faces are subdivided, so welding must update their neighbors.
func splitPrecisionTetra() *Mesh {
	return &Mesh{
		Positions: []Vec3{{1, 0, 0}, {2, 0, 0}, {1, 1, 0}, {1, 0, 1}, {1 + 1e-9, 0, 0}},
		Normals:   []Vec3{{0, 1, 0}, {0, 1, 0}, {0, 1, 0}, {0, 1, 0}, {0, 1, 0}},
		Indices:   []uint32{0, 2, 4, 4, 2, 1, 0, 4, 3, 4, 1, 3, 0, 3, 2, 1, 2, 3},
	}
}

func TestPrecisionWeld(t *testing.T) {
	m := splitPrecisionTetra()
	before, _ := json.Marshal(m)
	if err := ValidateMesh(m, precisionDomain()); err != nil {
		t.Fatal(err)
	}
	q, err := repairGLBCollapse(m, precisionDomain())
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Positions) != 4 || len(q.Indices) != 12 {
		t.Fatalf("unexpected repair: %+v", q)
	}
	if err = validateClosedSurface(q); err != nil {
		t.Fatal(err)
	}
	for _, p := range q.Positions {
		if p != glbPrecisionVec(p) {
			t.Fatal("not export precision")
		}
	}
	for _, n := range q.Normals {
		if n.Len() < .999999 || n.Len() > 1.000001 {
			t.Fatal("bad normal")
		}
	}
	again, err := repairGLBCollapse(q, precisionDomain())
	if err != nil || !reflect.DeepEqual(q, again) {
		t.Fatal("not idempotent", err)
	}
	after, _ := json.Marshal(m)
	if !bytes.Equal(before, after) {
		t.Fatal("mutated input")
	}
	for n := 0; n < 3; n++ {
		again, err = repairGLBCollapse(m, precisionDomain())
		if err != nil || !reflect.DeepEqual(q, again) {
			t.Fatal("nondeterministic repair", err)
		}
	}
}

func TestPrecisionRepairRejectsUnsafeTopology(t *testing.T) {
	for _, kind := range []string{"boundary", "duplicate", "orientation", "pinch", "distinct sliver"} {
		t.Run(kind, func(t *testing.T) {
			m := splitPrecisionTetra()
			switch kind {
			case "boundary":
				m.Indices = m.Indices[:len(m.Indices)-3]
			case "duplicate":
				m.Indices = append(m.Indices, 0, 3, 2)
			case "orientation":
				m.Indices[13], m.Indices[14] = m.Indices[14], m.Indices[13]
			case "pinch":
				// A second closed tetrahedron sharing only vertex 0: edge tests alone pass.
				m.Positions = append(m.Positions, Vec3{0, 0, 0}, Vec3{1, -1, 0}, Vec3{1, 0, -1})
				m.Normals = append(m.Normals, Vec3{}, Vec3{}, Vec3{})
				m.Indices = append(m.Indices, 0, 6, 5, 0, 5, 7, 0, 7, 6, 5, 6, 7)
			case "distinct sliver":
				m.Positions = append(m.Positions, Vec3{5, 1, 0}, Vec3{6, 1, 0}, Vec3{7, 1 + 1e-9, 0}, Vec3{5, 1, 1})
				m.Normals = append(m.Normals, Vec3{}, Vec3{}, Vec3{}, Vec3{})
				m.Indices = append(m.Indices, 5, 7, 6, 5, 6, 8, 5, 8, 7, 6, 7, 8)
			}
			if _, err := repairGLBCollapse(m, precisionDomain()); !errors.Is(err, ErrMeshPrecision) {
				t.Fatalf("unsafe repair admitted: %v", err)
			}
		})
	}
}

func TestBridgeQuantizedCollapseRegression(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/bridge-quantized-collapse.json")
	if err != nil {
		t.Fatal(err)
	}
	var r Recipe
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	var first *Mesh
	var firstHash string
	for fresh := 0; fresh < 2; fresh++ {
		e := testEngine(t)
		for pass := 0; pass < 2; pass++ {
			c, err := e.Evaluate(ctx, r, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !c.Valid {
				t.Fatal(c.Problems)
			}
			a, err := e.Artifact(c, "mesh")
			if err != nil {
				t.Fatal(err)
			}
			m := a.Mesh
			if len(m.Positions) != 53355 || len(m.Indices)/3 != 107066 {
				t.Fatalf("unexpected geometry %d/%d", len(m.Positions), len(m.Indices)/3)
			}
			if err = validateClosedSurface(m); err != nil {
				t.Fatal(err)
			}
			if first == nil {
				first = m
				firstHash = c.Artifacts["mesh"].Hash
			} else if !reflect.DeepEqual(first, m) || firstHash != c.Artifacts["mesh"].Hash {
				t.Fatal("fresh/cached geometry differs")
			}
			for _, n := range c.Nodes {
				if n.Cached != (pass == 1) {
					t.Fatalf("unexpected cache state: %+v", n)
				}
			}
			ref, _ := e.Artifact(c, "reference_volume")
			vol, _ := e.Artifact(c, "volume")
			problems, err := ValidateConditioned(ctx, r.Source.Spec, ref.Volume, vol.Volume, m)
			if err != nil || len(problems) > 0 {
				t.Fatal(problems, err)
			}
			canonical, err := (Tetrahedra{}).Mesh(ctx, vol.Volume)
			if err != nil || !conditionedMeshMatches(m, canonical) {
				t.Fatal("canonical mismatch", err)
			}
			if fresh != 0 || pass != 0 {
				continue
			}
			var output bytes.Buffer
			if err = e.Export(ctx, c.ID, &output); err != nil {
				t.Fatal(err)
			}
			z, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range z.File {
				if f.Name != "asset.glb" {
					continue
				}
				found = true
				reader, err := f.Open()
				if err != nil {
					t.Fatal(err)
				}
				glb, err := io.ReadAll(reader)
				reader.Close()
				if err != nil {
					t.Fatal(err)
				}
				decoded := decodePrecisionExport(t, glb)
				for _, n := range decoded.Normals {
					if !n.Finite() || math.Abs(n.Len()-1) > 1e-6 {
						t.Fatal("invalid exported unit normal", n)
					}
				}
				if !conditionedMeshMatches(decoded, m) {
					t.Fatal("export changed canonical surface")
				}
				if err = ValidateMesh(decoded, r.Domain); err != nil {
					t.Fatal(err)
				}
				if err = validateClosedSurface(decoded); err != nil {
					t.Fatal(err)
				}
				problems, err = ValidateConditioned(ctx, r.Source.Spec, ref.Volume, vol.Volume, decoded)
				if err != nil || len(problems) > 0 {
					t.Fatal("exported constraints", problems, err)
				}
				// Optional external validator input, without changing normal test outputs.
				if target := os.Getenv("PROGEN_PRECISION_GLB"); target != "" {
					if err = os.WriteFile(target, glb, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !found {
				t.Fatal("missing exported GLB")
			}
			// Removing a face must still fail canonical admission.
			changed := *m
			changed.Indices = append([]uint32{}, m.Indices[3:]...)
			problems, err = ValidateConditioned(ctx, r.Source.Spec, ref.Volume, vol.Volume, &changed)
			if err != nil || !strings.Contains(strings.Join(problems, ";"), "canonical") {
				t.Fatal("substituted surface admitted", problems, err)
			}
		}
	}
	t.Logf("repaired mesh artifact hash: %s", firstHash)
}

// Read actual export bytes independently of the deliberately size-limited source importer.
func decodePrecisionExport(t *testing.T, glb []byte) *Mesh {
	t.Helper()
	doc, bin, err := parseArchetypeGLB(glb)
	if err != nil {
		t.Fatal(err)
	}
	m := &Mesh{}
	for i, a := range doc.Accessors {
		if i > 2 || a.BufferView == nil {
			t.Fatal("unexpected export accessor")
		}
		view := doc.BufferViews[*a.BufferView]
		if view.ByteStride != 0 {
			t.Fatal("unexpected export stride")
		}
		start := view.ByteOffset + a.ByteOffset
		width := 12
		if i == 2 {
			width = 4
		}
		if start < 0 || start+a.Count*width > len(bin) {
			t.Fatal("export accessor bounds")
		}
		if i == 2 {
			if a.ComponentType != 5125 || a.Type != "SCALAR" {
				t.Fatal("unexpected indices")
			}
			for j := 0; j < a.Count; j++ {
				m.Indices = append(m.Indices, binary.LittleEndian.Uint32(bin[start+4*j:]))
			}
		} else {
			if a.ComponentType != 5126 || a.Type != "VEC3" {
				t.Fatal("unexpected vertex representation")
			}
			for j := 0; j < a.Count; j++ {
				read := func(k int) float64 {
					return float64(math.Float32frombits(binary.LittleEndian.Uint32(bin[start+12*j+4*k:])))
				}
				v := Vec3{read(0), read(1), read(2)}
				if i == 0 {
					m.Positions = append(m.Positions, v)
				} else {
					m.Normals = append(m.Normals, v)
				}
			}
		}
	}
	return m
}

func TestPrecisionFailureIsCandidateLocal(t *testing.T) {
	for _, kind := range []string{"generate", "search"} {
		t.Run(kind, func(t *testing.T) {
			e, r := flakyMeshEngine(t, false)
			reg := e.Registry.entries["test-budget-mesh"]
			original := reg.factory
			reg.factory = func(p map[string]float64) (Operator, error) {
				op, err := original(p)
				if err != nil {
					return nil, err
				}
				return OperatorFunc(func(ctx context.Context, d Domain, s uint64, in Inputs) (Outputs, error) {
					if kind == "search" {
						return nil, fmt.Errorf("test repair: %w", ErrMeshPrecision)
					}
					out, err := op.Run(ctx, d, s, in)
					if errors.Is(err, ErrMeshTriangleLimit) {
						return nil, fmt.Errorf("test repair: %w", ErrMeshPrecision)
					}
					return out, err
				}), nil
			}
			e.Registry.entries["test-budget-mesh"] = reg
			_, failures, err := e.RunWithFailures(context.Background(), RunRequest{Kind: kind, Recipe: &r, Count: 4, SearchSeed: 7, Workers: 2}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(failures) == 0 {
				t.Fatal("expected reproducible precision skip")
			}
			for _, f := range failures {
				if !strings.Contains(f.Reason, ErrMeshPrecision.Error()) {
					t.Fatal(f)
				}
			}
		})
	}
}
