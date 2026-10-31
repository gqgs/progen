package progen

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func constraintFixture(t *testing.T, resolution int) (ConstraintSpec, *VolumeGrid) {
	t.Helper()
	s, err := ParseConstraintSpec(bridgeSidecar)
	if err != nil {
		t.Fatal(err)
	}
	d := s.Domain
	d.Resolution = resolution
	_, reference, err := DecodeArchetypeGLB(context.Background(), bridgeGLB, s.Transform, d)
	if err != nil {
		t.Fatal(err)
	}
	return s, reference
}

func constraintHas(problems []string, fragment string) bool {
	for _, p := range problems {
		if strings.Contains(p, fragment) {
			return true
		}
	}
	return false
}

func TestConstraintSidecarValidation(t *testing.T) {
	s, _ := constraintFixture(t, 32)
	for _, tc := range []struct {
		name   string
		change func(*ConstraintSpec)
		want   string
	}{
		{"version", func(s *ConstraintSpec) { s.Version = 2 }, "unsupported constraint version"},
		{"source scale", func(s *ConstraintSpec) { s.Transform.Scale = 0 }, "transform"},
		{"overlap", func(s *ConstraintSpec) { s.Forbidden = append(s.Forbidden, s.Required[0]) }, "overlaps"},
		{"slope", func(s *ConstraintSpec) { s.Traversal.Endpoints[1].Y = .2 }, "same floor height"},
		{"budget", func(s *ConstraintSpec) { s.Budget.MaxTriangles = maxMeshTriangles + 1 }, "budget"},
		{"resolution", func(s *ConstraintSpec) { s.Domain.Resolution = 128 }, "32 or 64"},
		{"duplicate anchors", func(s *ConstraintSpec) { s.Anchors = append(s.Anchors, s.Anchors[0]) }, "duplicate anchor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(s)
			var copy ConstraintSpec
			json.Unmarshal(b, &copy)
			tc.change(&copy)
			if err := copy.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	var object map[string]any
	json.Unmarshal(bridgeSidecar, &object)
	object["unexpected"] = true
	b, _ := json.Marshal(object)
	if _, err := ParseConstraintSpec(b); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field accepted: %v", err)
	}
	if _, err := ParseConstraintSpec(append(append([]byte{}, bridgeSidecar...), []byte(" {}")...)); err == nil {
		t.Fatal("trailing object accepted")
	}
}

func TestConstraintMasksAndReplacement(t *testing.T) {
	ctx := context.Background()
	s, ref := constraintFixture(t, 32)
	required, forbidden, editable, err := s.Masks(ctx, ref.Space)
	if err != nil {
		t.Fatal(err)
	}
	// An empty decoration removes reference rail material within editable
	// regions, demonstrating replacement rather than an unconditional union.
	empty := &VolumeGrid{Space: ref.Space, Values: make([]float64, len(ref.Values))}
	for i := range empty.Values {
		empty.Values[i] = 1
	}
	before, _ := json.Marshal(ref)
	out, err := ConstrainDecoration(ctx, ref, empty, required, forbidden, editable)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(ref)
	if string(before) != string(after) {
		t.Fatal("composition mutated reference")
	}
	removed := 0
	for i, v := range out.Values {
		if required.Values[i] < 0 && v >= 0 {
			t.Fatal("required solid removed")
		}
		if forbidden.Values[i] < 0 && v < 0 {
			t.Fatal("forbidden region filled")
		}
		if editable.Values[i] < 0 && required.Values[i] >= 0 && ref.Values[i] < 0 && v >= 0 {
			removed++
		}
	}
	if removed == 0 {
		t.Fatal("editable reference material was not replaced")
	}
	mesh, err := (Tetrahedra{}).Mesh(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	problems, err := ValidateConditioned(ctx, s, ref, out, mesh)
	if err != nil || len(problems) != 0 {
		t.Fatalf("valid bare-deck result: %v, %v", problems, err)
	}
	thin := s
	thin.Required = []Box{{Vec3{-.001, -.21, -.001}, Vec3{.001, -.20, .001}}}
	if _, _, _, err := thin.Masks(ctx, ref.Space); err == nil || !strings.Contains(err.Error(), "no interior samples") {
		t.Fatalf("thin region accepted: %v", err)
	}
	cancel, cancelFunc := context.WithCancel(ctx)
	cancelFunc()
	if _, err := ConstrainDecoration(cancel, ref, empty, required, forbidden, editable); err == nil {
		t.Fatal("composition ignored cancellation")
	}
	if _, _, _, err := s.Masks(cancel, ref.Space); err == nil {
		t.Fatal("mask sampling ignored cancellation")
	}
}

func TestConditionedDecorationAtExplorationResolutions(t *testing.T) {
	for _, resolution := range []int{32, 64} {
		t.Run(strconv.Itoa(resolution), func(t *testing.T) {
			ctx := context.Background()
			s, ref := constraintFixture(t, resolution)
			r, f, e, err := s.Masks(ctx, ref.Space)
			if err != nil {
				t.Fatal(err)
			}
			decoration, err := (TPMS{Surface: 0, Frequency: 2, Thickness: .5, Envelope: .95}).Generate(ctx, ref.Space, 42)
			if err != nil {
				t.Fatal(err)
			}
			v, err := ConstrainDecoration(ctx, ref, decoration, r, f, e)
			if err != nil {
				t.Fatal(err)
			}
			mesh, err := (Tetrahedra{}).Mesh(ctx, v)
			if err != nil {
				t.Fatal(err)
			}
			problems, err := ValidateConditioned(ctx, s, ref, v, mesh)
			if err != nil || len(problems) > 0 {
				t.Fatalf("resolution %d: %v; %v", resolution, problems, err)
			}
		})
	}
}

func TestConditionedFailuresAreIndependentOfConnectedSolid(t *testing.T) {
	ctx := context.Background()
	s, ref := constraintFixture(t, 32)
	mesh, err := (Tetrahedra{}).Mesh(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	// A cut through the middle of the walkable strip must fail even though
	// the continuous side rails still connect the solid around the gap.
	gap := &VolumeGrid{Space: ref.Space, Values: append([]float64{}, ref.Values...)}
	n := ref.Space.Resolution
	for i := range gap.Values {
		p := ref.Space.Point(i%n, (i/n)%n, i/(n*n))
		if p.X > -.3 && p.X < .3 && p.Z > -.8 && p.Z < .8 {
			gap.Values[i] = 1
		}
	}
	gapMesh, err := (Tetrahedra{}).Mesh(ctx, gap)
	if err != nil {
		t.Fatal(err)
	}
	problems, err := ValidateConditioned(ctx, s, ref, gap, gapMesh)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"required deck", "lacks deck support", "walkable mesh surface"} {
		if !constraintHas(problems, want) {
			t.Fatalf("missing %q in %v", want, problems)
		}
	}
	// A triangle crosses a forbidden box with every vertex outside it and
	// without any occupied volume samples. Exact clipping must still find it.
	intruding := &Mesh{Positions: append([]Vec3{}, mesh.Positions...), Normals: append([]Vec3{}, mesh.Normals...), Indices: append([]uint32{}, mesh.Indices...)}
	base := uint32(len(intruding.Positions))
	intruding.Positions = append(intruding.Positions, Vec3{-1, .5, -1.5}, Vec3{1, .5, -1.5}, Vec3{0, .5, 1.5})
	intruding.Normals = append(intruding.Normals, Vec3{0, 1, 0}, Vec3{0, 1, 0}, Vec3{0, 1, 0})
	intruding.Indices = append(intruding.Indices, base, base+1, base+2)
	problems, err = ValidateConditioned(ctx, s, ref, ref, intruding)
	if err != nil {
		t.Fatal(err)
	}
	if !constraintHas(problems, "mesh intrudes into forbidden") || !constraintHas(problems, "mesh intrudes into traversal") {
		t.Fatalf("between-sample intrusion missed: %v", problems)
	}
	anchorLost := &VolumeGrid{Space: ref.Space, Values: append([]float64{}, ref.Values...)}
	for i := range anchorLost.Values {
		p := ref.Space.Point(i%n, (i/n)%n, i/(n*n))
		if s.Anchors[0].Region.Contains(p) {
			anchorLost.Values[i] = 1
		}
	}
	anchorMesh, err := (Tetrahedra{}).Mesh(ctx, anchorLost)
	if err != nil {
		t.Fatal(err)
	}
	problems, err = ValidateConditioned(ctx, s, ref, anchorLost, anchorMesh)
	if err != nil {
		t.Fatal(err)
	}
	if !constraintHas(problems, "anchor west attachment lost") {
		t.Fatalf("anchor loss missed: %v", problems)
	}
}

func TestDeckSurfaceIndexMatchesBruteForce(t *testing.T) {
	ctx := context.Background()
	s, ref := constraintFixture(t, 32)
	mesh, err := (Tetrahedra{}).Mesh(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	index, err := newDeckSurfaceIndex(ctx, mesh, ref.Space, 0, s.Traversal.SurfaceTolerance)
	if err != nil {
		t.Fatal(err)
	}
	// Include exact bin boundaries, tiny boundary offsets, deck edges and
	// points with no supporting face. The index must preserve the old result.
	for i := 0; i <= 24; i++ {
		for j := 0; j <= 12; j++ {
			p := Vec3{-4 + float64(i)/3, 0, -1.5 + float64(j)/4}
			for _, delta := range []float64{0, -1e-10, 1e-10} {
				p.X += delta
				if got, want := index.at(p, s.Traversal.SurfaceTolerance), deckSurfaceAt(mesh, p, s.Traversal.SurfaceTolerance); got != want {
					t.Fatalf("indexed=%v brute=%v at %+v", got, want, p)
				}
			}
		}
	}
	// A large upward face uses the bounded global list. A second face above
	// the floor band is filtered out without changing the exact predicate.
	manual := &Mesh{Positions: []Vec3{{-4, 0, -1.5}, {0, 0, 1.5}, {4, 0, -1.5}, {-.1, .3, -.1}, {0, .3, .1}, {.1, .3, -.1}}, Indices: []uint32{0, 1, 2, 3, 4, 5}}
	index, err = newDeckSurfaceIndex(ctx, manual, ref.Space, 0, .1)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.large) != 1 {
		t.Fatalf("expected one oversized face, got %d", len(index.large))
	}
	for _, p := range []Vec3{{0, 0, 0}, {-4, 0, -1.5}, {4, 0, -1.5}, {4, 0, 1.5}, {0, 0, 1.5}, {0, 0, 1.5 + 1e-10}} {
		if got, want := index.at(p, .1), deckSurfaceAt(manual, p, .1); got != want {
			t.Fatalf("large triangle indexed=%v brute=%v at %+v", got, want, p)
		}
	}
	cancel, stop := context.WithCancel(ctx)
	stop()
	if _, err := newDeckSurfaceIndex(cancel, mesh, ref.Space, 0, .1); err != context.Canceled {
		t.Fatalf("index ignored cancellation: %v", err)
	}
	for len(manual.Indices)/3 < 66 {
		manual.Indices = append(manual.Indices, 0, 1, 2)
	}
	if _, err := newDeckSurfaceIndex(ctx, manual, ref.Space, 0, .1); err == nil || !strings.Contains(err.Error(), "oversized deck triangles") {
		t.Fatalf("unbounded oversized triangle list: %v", err)
	}
	small := &Mesh{Positions: []Vec3{{0, 0, 0}, {0, 0, .001}, {.001, 0, 0}}}
	for i := 0; i < 513; i++ {
		small.Indices = append(small.Indices, 0, 1, 2)
	}
	if _, err := newDeckSurfaceIndex(ctx, small, ref.Space, 0, .1); err == nil || !strings.Contains(err.Error(), "one spatial bin") {
		t.Fatalf("unbounded bin query work: %v", err)
	}
}

func TestConditionedMeshEnclosureAndRequiredCuts(t *testing.T) {
	ctx := context.Background()
	s, ref := constraintFixture(t, 32)
	base, err := (Tetrahedra{}).Mesh(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	appendBox := func(bounds Box) *Mesh {
		mesh := &Mesh{Positions: append([]Vec3{}, base.Positions...), Normals: append([]Vec3{}, base.Normals...), Indices: append([]uint32{}, base.Indices...)}
		box := archetypeTestBox()
		offset := uint32(len(mesh.Positions))
		size := bounds.Max.Sub(bounds.Min)
		for i, p := range box.Positions {
			mesh.Positions = append(mesh.Positions, bounds.Min.Add(Vec3{(p.X + .5) * size.X, (p.Y + .5) * size.Y, (p.Z + .5) * size.Z}))
			mesh.Normals = append(mesh.Normals, box.Normals[i])
		}
		for _, index := range box.Indices {
			mesh.Indices = append(mesh.Indices, index+offset)
		}
		return mesh
	}
	// No enclosing-shell triangle crosses the forbidden corridor: it is
	// wholly inside the extra closed solid. Volume samples intentionally
	// remain unchanged to verify the exported mesh independently.
	enclosed := appendBox(Box{Vec3{-4, -.6, -1.8}, Vec3{4, 2.2, 1.8}})
	problems, err := ValidateConditioned(ctx, s, ref, ref, enclosed)
	if err != nil {
		t.Fatal(err)
	}
	if !constraintHas(problems, "does not match the canonical") {
		t.Fatalf("closed mesh enclosure admitted: %v", problems)
	}
	// A narrow internal surface cuts required material between the regular
	// traversal rays; there need be no missing volume-grid samples.
	cut := appendBox(Box{Vec3{.013, -.31, .013}, Vec3{.023, -.11, .023}})
	problems, err = ValidateConditioned(ctx, s, ref, ref, cut)
	if err != nil {
		t.Fatal(err)
	}
	if !constraintHas(problems, "mesh cuts required") {
		t.Fatalf("required-volume mesh cut admitted: %v", problems)
	}
	// Keep every occupied grid sample occupied while moving a canonical
	// surface into the small gap between a required box face and its last
	// interior sample. This is a consistent mesh/volume pair, so occupancy
	// signs and canonical correspondence alone cannot detect the loss.
	shallow := &VolumeGrid{Space: ref.Space, Values: append([]float64{}, ref.Values...)}
	n := ref.Space.Resolution
	for i, value := range shallow.Values {
		p := ref.Space.Point(i%n, (i/n)%n, i/(n*n))
		if value < 0 && p.Y > -.09 && p.Y < -.08 && p.X > 3.1 && p.X < 3.6 && p.Z > -.3 && p.Z < .3 {
			shallow.Values[i] = -1e-6
		}
	}
	shallowMesh, err := (Tetrahedra{}).Mesh(ctx, shallow)
	if err != nil {
		t.Fatal(err)
	}
	problems, err = ValidateConditioned(ctx, s, ref, shallow, shallowMesh)
	if err != nil {
		t.Fatal(err)
	}
	if constraintHas(problems, "missing samples") || constraintHas(problems, "canonical") {
		t.Fatalf("fixture was rejected by preexisting sign/canonical checks: %v", problems)
	}
	if !constraintHas(problems, "mesh cuts required") || !constraintHas(problems, "mesh cuts anchor east") {
		t.Fatalf("continuous required/anchor loss admitted: %v", problems)
	}
}

func TestConditionedGLBPrecisionGeometry(t *testing.T) {
	ctx := context.Background()
	s, ref := constraintFixture(t, 32)
	s.Forbidden = []Box{{Vec3{1.100000001, .2, -.3}, Vec3{1.5, .8, .3}}}
	mesh, err := (Tetrahedra{}).Mesh(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	// 1.1 rounds upward to 1.10000002384 in GLB. Only the exported
	// float32 triangle crosses the declared forbidden boundary.
	tri := [3]Vec3{{1.1, .4, -.2}, {1.1, .6, 0}, {1.1, .4, .2}}
	epsilon := ref.Space.Box.Max.Sub(ref.Space.Box.Min).Len() * 1e-9
	if triangleBoxInterior(tri, s.Forbidden[0], epsilon) {
		t.Fatal("double-precision fixture already intrudes")
	}
	base := uint32(len(mesh.Positions))
	for _, p := range tri {
		mesh.Positions = append(mesh.Positions, p)
		mesh.Normals = append(mesh.Normals, Vec3{1, 0, 0})
	}
	mesh.Indices = append(mesh.Indices, base, base+1, base+2)
	problems, err := ValidateConditioned(ctx, s, ref, ref, mesh)
	if err != nil {
		t.Fatal(err)
	}
	if !constraintHas(problems, "mesh intrudes into forbidden") {
		t.Fatalf("export precision intrusion missed: %v", problems)
	}
	degenerate := &Mesh{Positions: []Vec3{{1, 0, 0}, {1 + 1e-9, 0, 0}, {1, 0, 1}}, Normals: []Vec3{{0, 1, 0}, {0, 1, 0}, {0, 1, 0}}, Indices: []uint32{0, 1, 2}}
	if _, err := quantizedConditionedMesh(degenerate, ref.Space); err == nil || !strings.Contains(err.Error(), "degenerate") {
		t.Fatalf("float32 collapsed triangle admitted: %v", err)
	}
}
