package progen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
)

// ConstraintSpec version 1 describes normalized, right-handed Y-up meter space.
// Transform applies only to GLB coordinates. All boxes and endpoints are already
// expressed in normalized space. Regions are unions of axis-aligned boxes.
type ConstraintSpec struct {
	FloorRelief     *FloorReliefSpec                     `json:"floor_relief,omitempty"`
	Version         int                                  `json:"version"`
	Frame           string                               `json:"frame"`
	Units           string                               `json:"units"`
	Transform       ImportTransform                      `json:"transform"`
	Domain          Domain                               `json:"domain"`
	Required        []Box                                `json:"required"`
	Forbidden       []Box                                `json:"forbidden"`
	Editable        []Box                                `json:"editable"`
	Anchors         []ConstraintAnchor                   `json:"anchors,omitempty"`
	Traversal       TraversalSpec                        `json:"traversal"`
	Budget          ConstraintBudget                     `json:"budget"`
	ParameterBounds map[string]map[string]ParameterRange `json:"parameter_bounds,omitempty"`
}

// ParameterRange narrows a recipe node parameter without changing its operator contract.
type ParameterRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

type ConstraintAnchor struct {
	Name   string `json:"name"`
	Region Box    `json:"region"`
}

// TraversalSpec is a straight, horizontal walkable strip. Endpoints specify
// nominal floor height. SurfaceTolerance explicitly permits sampling error in
// the deck height; clearance starts that far above the nominal floor. It is not
// a physics, navigation-mesh or real-world structural-safety certificate.
type TraversalSpec struct {
	Endpoints        [2]Vec3 `json:"endpoints"`
	Width            float64 `json:"width"`
	Height           float64 `json:"height"`
	DeckDepth        float64 `json:"deck_depth"`
	SurfaceTolerance float64 `json:"surface_tolerance"`
}

type ConstraintBudget struct {
	MaxTriangles  int `json:"max_triangles"`
	MaxCandidates int `json:"max_candidates"`
}

func ParseConstraintSpec(data []byte) (ConstraintSpec, error) {
	var s ConstraintSpec
	if len(data) == 0 || len(data) > 1<<20 {
		return s, fmt.Errorf("constraint sidecar must contain 1..1048576 bytes")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, fmt.Errorf("constraint sidecar: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return s, fmt.Errorf("constraint sidecar must contain exactly one JSON value")
	}
	return s, s.Validate()
}

func (s ConstraintSpec) Validate() error {
	if s.Version != 1 {
		return fmt.Errorf("unsupported constraint version %d", s.Version)
	}
	if s.Frame != "right-handed-y-up" || s.Units != "meters" {
		return fmt.Errorf("constraints require normalized right-handed-y-up meter coordinates")
	}
	if !finite(s.Transform.Scale) || s.Transform.Scale <= 0 || !s.Transform.Translation.Finite() {
		return fmt.Errorf("invalid source coordinate transform")
	}
	if err := s.Domain.Validate(); err != nil {
		return fmt.Errorf("constraint domain: %w", err)
	}
	if s.Domain.Resolution != 32 && s.Domain.Resolution != 64 {
		return fmt.Errorf("conditioned exploration supports resolution 32 or 64")
	}
	if len(s.Domain.Exclusions) != 0 {
		return fmt.Errorf("conditioned recipes use forbidden regions instead of domain exclusions")
	}
	if s.Budget.MaxTriangles < 1 || s.Budget.MaxTriangles > maxMeshTriangles || s.Budget.MaxCandidates < 1 || s.Budget.MaxCandidates > 256 {
		return fmt.Errorf("constraint budget requires 1..%d triangles and 1..256 candidates", maxMeshTriangles)
	}
	for _, group := range []struct {
		name  string
		boxes []Box
	}{{"required", s.Required}, {"forbidden", s.Forbidden}, {"editable", s.Editable}} {
		if len(group.boxes) == 0 || len(group.boxes) > 64 {
			return fmt.Errorf("%s regions require 1..64 boxes", group.name)
		}
		for i, b := range group.boxes {
			if !constraintBoxValid(b, s.Domain.Box) {
				return fmt.Errorf("%s region %d has invalid bounds or touches the domain boundary", group.name, i)
			}
		}
	}
	for i, a := range s.Required {
		for j, b := range s.Forbidden {
			if boxesInteriorOverlap(a, b) {
				return fmt.Errorf("required region %d overlaps forbidden region %d", i, j)
			}
		}
	}
	if len(s.Anchors) > 64 {
		return fmt.Errorf("at most 64 anchor zones are supported")
	}
	names := map[string]bool{}
	for i, a := range s.Anchors {
		if !validName(a.Name) || names[a.Name] || !constraintBoxValid(a.Region, s.Domain.Box) {
			return fmt.Errorf("invalid or duplicate anchor zone %d", i)
		}
		names[a.Name] = true
	}
	t := s.Traversal
	a, b := t.Endpoints[0], t.Endpoints[1]
	if !a.Finite() || !b.Finite() || math.Abs(a.Y-b.Y) > 1e-9 || a.Sub(b).Len() <= 1e-6 {
		return fmt.Errorf("traversal requires two distinct endpoints at the same floor height; sloping and curved paths are unsupported")
	}
	if !finite(t.Width) || !finite(t.Height) || !finite(t.DeckDepth) || !finite(t.SurfaceTolerance) || t.Width <= 0 || t.Height <= 0 || t.DeckDepth <= 0 || t.SurfaceTolerance <= 0 || t.SurfaceTolerance >= t.DeckDepth/2 || t.SurfaceTolerance >= t.Height {
		return fmt.Errorf("invalid traversal width, height, deck depth or surface tolerance")
	}
	lateral := Vec3{-(b.Z - a.Z), 0, b.X - a.X}.Unit()
	for _, p := range []Vec3{a, b} {
		for _, sign := range []float64{-1, 1} {
			q := p.Add(lateral.Mul(sign * t.Width / 2))
			for _, height := range []float64{-t.DeckDepth, t.Height} {
				if !s.Domain.Box.Contains(q.Add(Vec3{0, height, 0})) {
					return fmt.Errorf("traversal strip is outside the domain")
				}
			}
		}
	}
	if f := s.FloorRelief; f != nil {
		if !constraintBoxValid(f.Region, s.Domain.Box) || a.Z != 0 || b.Z != 0 || a.X >= b.X || a.Y != 0 || t.Width != 1.2 || t.SurfaceTolerance != .1 || f.Region.Min.Y > -.24 || f.Region.Max.Y < .18 {
			return fmt.Errorf("floor relief experiment requires a centered X-axis bridge with a resolved vertical region and unchanged walking contract")
		}
	}
	if len(s.ParameterBounds) > 64 {
		return fmt.Errorf("too many parameter bound nodes")
	}
	for node, params := range s.ParameterBounds {
		if !validName(node) || len(params) > 64 {
			return fmt.Errorf("invalid parameter bound node")
		}
		for key, bounds := range params {
			if !validName(key) || !finite(bounds.Min) || !finite(bounds.Max) || bounds.Min > bounds.Max {
				return fmt.Errorf("invalid parameter bounds for %s.%s", node, key)
			}
		}
	}
	return nil
}

// ValidateDomain allows exploration resolution changes but preserves all other
// coordinate and domain semantics declared by the immutable source sidecar.
func (s ConstraintSpec) ValidateDomain(d Domain) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if d.Resolution != 32 && d.Resolution != 64 {
		return fmt.Errorf("conditioned exploration supports resolution 32 or 64")
	}
	want := s.Domain
	want.Resolution = d.Resolution
	if !sameDomain(want, d) {
		return fmt.Errorf("conditioned domain differs from sidecar coordinates or bounds")
	}
	return nil
}

func constraintBoxValid(b, bounds Box) bool {
	return b.Min.Finite() && b.Max.Finite() && b.Min.X < b.Max.X && b.Min.Y < b.Max.Y && b.Min.Z < b.Max.Z &&
		b.Min.X > bounds.Min.X && b.Min.Y > bounds.Min.Y && b.Min.Z > bounds.Min.Z &&
		b.Max.X < bounds.Max.X && b.Max.Y < bounds.Max.Y && b.Max.Z < bounds.Max.Z
}

func boxesInteriorOverlap(a, b Box) bool {
	return a.Min.X < b.Max.X && a.Max.X > b.Min.X && a.Min.Y < b.Max.Y && a.Max.Y > b.Min.Y && a.Min.Z < b.Max.Z && a.Max.Z > b.Min.Z
}

func regionValue(p Vec3, boxes []Box) float64 {
	v := math.MaxFloat64
	for _, b := range boxes {
		v = math.Min(v, boxSigned(p, b))
	}
	return v
}

// Masks returns typed-port-ready negative-interior box-union fields. Their
// interpolation is a sampled implicit mask, not an exact signed distance field.
func (s ConstraintSpec) Masks(ctx context.Context, d Domain) (required, forbidden, editable *VolumeGrid, err error) {
	if err = s.ValidateDomain(d); err != nil {
		return
	}
	n := d.Resolution
	makeGrid := func() *VolumeGrid { return &VolumeGrid{Space: d, Values: make([]float64, n*n*n)} }
	required, forbidden, editable = makeGrid(), makeGrid(), makeGrid()
	groups := [][]Box{s.Required, s.Forbidden, s.Editable}
	grids := []*VolumeGrid{required, forbidden, editable}
	counts := make([][]int, len(groups))
	for i := range groups {
		counts[i] = make([]int, len(groups[i]))
	}
	for z := 0; z < n; z++ {
		if err = ctx.Err(); err != nil {
			return
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				p, index := d.Point(x, y, z), x+n*(y+n*z)
				for i, boxes := range groups {
					grids[i].Values[index] = regionValue(p, boxes)
					for j, box := range boxes {
						if boxSigned(p, box) < 0 {
							counts[i][j]++
						}
					}
				}
			}
		}
	}
	for i, group := range counts {
		for j, count := range group {
			if count == 0 {
				err = fmt.Errorf("%s region %d has no interior samples at resolution %d; thin features require a finer grid or thicker region", []string{"required", "forbidden", "editable"}[i], j, n)
				return
			}
		}
	}
	return
}

// ConstrainDecoration replaces editable reference material with decoration,
// retains reference material elsewhere, preserves required solids, then carves
// forbidden solids. Every output sample is new; inputs remain immutable.
func ConstrainDecoration(ctx context.Context, reference, decoration, required, forbidden, editable *VolumeGrid) (*VolumeGrid, error) {
	grids := []*VolumeGrid{reference, decoration, required, forbidden, editable}
	if reference == nil {
		return nil, fmt.Errorf("missing reference volume")
	}
	d := reference.Space
	for _, g := range grids {
		if g == nil || !sameDomain(d, g.Space) || len(g.Values) != d.Resolution*d.Resolution*d.Resolution {
			return nil, fmt.Errorf("constrained composition requires five compatible volume grids")
		}
	}
	out := &VolumeGrid{Space: d, Values: make([]float64, len(reference.Values))}
	for i, ref := range reference.Values {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		retained := math.Max(ref, -editable.Values[i])
		replaced := math.Max(decoration.Values[i], editable.Values[i])
		out.Values[i] = math.Max(math.Min(math.Min(retained, replaced), required.Values[i]), -forbidden.Values[i])
		if !finite(out.Values[i]) {
			return nil, fmt.Errorf("non-finite constrained composition sample")
		}
	}
	applyConstraints(out)
	return out, nil
}

// ValidateConditioned measures functional constraints independently of solid
// connected-component quality. It verifies the source contract, occupancy,
// actual protected-region triangle intersections, canonical volume/mesh
// correspondence, and upward-facing deck intersections along the sampled strip.
// Geometry predicates use the float32 coordinates actually emitted by GLB.
// The result remains a
// resolution-bounded geometry check, not a real-world safety assessment.
func ValidateConditioned(ctx context.Context, s ConstraintSpec, reference, result *VolumeGrid, mesh *Mesh) ([]string, error) {
	if reference == nil || result == nil || mesh == nil {
		return []string{"conditioned outputs require reference volume, result volume and mesh"}, nil
	}
	if err := s.ValidateDomain(result.Space); err != nil {
		return nil, err
	}
	d := result.Space
	if !sameDomain(reference.Space, d) || len(reference.Values) != len(result.Values) || len(result.Values) != d.Resolution*d.Resolution*d.Resolution {
		return nil, fmt.Errorf("conditioned evaluator volume mismatch")
	}
	if err := validateMeshStructure(mesh, d); err != nil {
		return []string{err.Error()}, nil
	}
	if len(mesh.Indices)/3 > s.Budget.MaxTriangles {
		return []string{fmt.Sprintf("conditioned triangle budget exceeded: %d > %d", len(mesh.Indices)/3, s.Budget.MaxTriangles)}, nil
	}
	var err error
	mesh, err = quantizedConditionedMesh(mesh, d)
	if err != nil {
		return []string{err.Error()}, nil
	}
	if s.FloorRelief != nil {
		if err := validateClosedSurface(mesh); err != nil {
			return []string{"floor relief requires a closed manifold surface: " + err.Error()}, nil
		}
	}
	required, forbidden, editable, err := s.Masks(ctx, d)
	if err != nil {
		return []string{err.Error()}, nil
	}
	problems := []string{}
	refMissing, missing, intrusions, outside := 0, 0, 0, 0
	for i, v := range result.Values {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !finite(v) || !finite(reference.Values[i]) {
			return []string{"non-finite conditioned volume"}, nil
		}
		if required.Values[i] < 0 {
			if reference.Values[i] >= 0 {
				refMissing++
			}
			if v >= 0 {
				missing++
			}
		}
		if forbidden.Values[i] < 0 && v < 0 {
			intrusions++
		}
		floorEditable := s.FloorRelief != nil && s.FloorRelief.Region.Contains(d.Point(i%d.Resolution, (i/d.Resolution)%d.Resolution, i/(d.Resolution*d.Resolution)))
		if !floorEditable && editable.Values[i] > 0 && required.Values[i] >= 0 && forbidden.Values[i] >= 0 && (v < 0) != (reference.Values[i] < 0) {
			outside++
		}
	}
	if refMissing > 0 {
		problems = append(problems, fmt.Sprintf("source does not contain required solid: %d missing samples", refMissing))
	}
	if missing > 0 {
		problems = append(problems, fmt.Sprintf("required deck or endpoint attachment lost: %d missing samples", missing))
	}
	if intrusions > 0 {
		problems = append(problems, fmt.Sprintf("forbidden clearance occupied: %d samples", intrusions))
	}
	if outside > 0 {
		problems = append(problems, fmt.Sprintf("material changed outside editable regions: %d samples", outside))
	}
	for _, anchor := range s.Anchors {
		count, sourceMissing, resultMissing := 0, 0, 0
		n := d.Resolution
		for i, value := range result.Values {
			if i%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if boxSigned(d.Point(i%n, (i/n)%n, i/(n*n)), anchor.Region) >= 0 {
				continue
			}
			count++
			if reference.Values[i] >= 0 {
				sourceMissing++
			}
			if value >= 0 {
				resultMissing++
			}
		}
		if count == 0 {
			problems = append(problems, fmt.Sprintf("anchor %s has no interior samples; increase resolution or attachment thickness", anchor.Name))
		}
		if sourceMissing > 0 {
			problems = append(problems, fmt.Sprintf("source anchor %s lacks attachment solid: %d missing samples", anchor.Name, sourceMissing))
		}
		if resultMissing > 0 {
			problems = append(problems, fmt.Sprintf("anchor %s attachment lost: %d missing samples", anchor.Name, resultMissing))
		}
	}
	canonical, err := (Tetrahedra{}).Mesh(ctx, result)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return append(problems, "conditioned canonical meshing failed: "+err.Error()), nil
	}
	if !conditionedMeshMatches(mesh, canonical) {
		problems = append(problems, "conditioned mesh does not match the canonical volume surface in GLB precision")
	}
	meshIntrusion := false
	requiredCut := false
	anchorCuts := make([]bool, len(s.Anchors))
	epsilon := d.Box.Max.Sub(d.Box.Min).Len() * 1e-9
	for i := 0; i < len(mesh.Indices); i += 3 {
		if i%12288 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		tri := [3]Vec3{mesh.Positions[mesh.Indices[i]], mesh.Positions[mesh.Indices[i+1]], mesh.Positions[mesh.Indices[i+2]]}
		if !meshIntrusion {
			for _, box := range s.Forbidden {
				if triangleBoxInterior(tri, box, epsilon) {
					meshIntrusion = true
					break
				}
			}
		}
		if !requiredCut {
			for _, box := range s.Required {
				if triangleBoxInterior(tri, box, epsilon) {
					requiredCut = true
					break
				}
			}
		}
		for j, anchor := range s.Anchors {
			if !anchorCuts[j] && triangleBoxInterior(tri, anchor.Region, epsilon) {
				anchorCuts[j] = true
			}
		}
	}
	if meshIntrusion {
		problems = append(problems, "mesh intrudes into forbidden clearance between grid samples")
	}
	if requiredCut {
		problems = append(problems, "mesh cuts required solid between grid samples")
	}
	for j, cut := range anchorCuts {
		if cut {
			problems = append(problems, fmt.Sprintf("mesh cuts anchor %s attachment between grid samples", s.Anchors[j].Name))
		}
	}
	traversalProblems, err := validateTraversal(ctx, s.Traversal, result, mesh)
	if err != nil {
		return nil, err
	}
	problems = append(problems, traversalProblems...)
	return problems, nil
}

// Conditioned assets deliberately use the existing canonical mesher. A custom
// surface cannot substitute a visually different mesh while retaining a valid
// occupancy grid. Comparing in GLB precision also covers export quantization.
func conditionedMeshMatches(mesh, canonical *Mesh) bool {
	if len(mesh.Positions) != len(canonical.Positions) || len(mesh.Indices) != len(canonical.Indices) {
		return false
	}
	for i, p := range canonical.Positions {
		if mesh.Positions[i] != glbPrecisionVec(p) {
			return false
		}
	}
	for i, index := range canonical.Indices {
		if mesh.Indices[i] != index {
			return false
		}
	}
	return true
}

func glbPrecisionVec(p Vec3) Vec3 {
	return Vec3{float64(float32(p.X)), float64(float32(p.Y)), float64(float32(p.Z))}
}

func quantizedConditionedMesh(mesh *Mesh, d Domain) (*Mesh, error) {
	q := &Mesh{Positions: make([]Vec3, len(mesh.Positions)), Normals: make([]Vec3, len(mesh.Normals)), Indices: mesh.Indices}
	for i, p := range mesh.Positions {
		q.Positions[i] = glbPrecisionVec(p)
		q.Normals[i] = glbPrecisionVec(mesh.Normals[i])
	}
	if err := ValidateMesh(q, d); err != nil {
		return nil, fmt.Errorf("conditioned mesh is invalid after GLB float32 quantization: %w", err)
	}
	return q, nil
}

func validateTraversal(ctx context.Context, t TraversalSpec, result *VolumeGrid, mesh *Mesh) ([]string, error) {
	d := result.Space
	spacing := d.Box.Max.Sub(d.Box.Min).Mul(1 / float64(d.Resolution-1))
	a, b := t.Endpoints[0], t.Endpoints[1]
	length := b.Sub(a).Len()
	forward := b.Sub(a).Unit()
	lateral := Vec3{-(b.Z - a.Z), 0, b.X - a.X}.Unit()
	directionalStep := func(direction Vec3) float64 {
		return .5 / math.Sqrt(math.Pow(direction.X/spacing.X, 2)+math.Pow(direction.Z/spacing.Z, 2))
	}
	stepsFloat, widthFloat := math.Ceil(length/directionalStep(forward)), math.Ceil(t.Width/directionalStep(lateral))
	clearanceFloat := math.Ceil((t.Height - t.SurfaceTolerance) / (spacing.Y * .5))
	if !finite(stepsFloat) || !finite(widthFloat) || !finite(clearanceFloat) || stepsFloat > 4096 || widthFloat > 1024 || clearanceFloat > 4096 || (stepsFloat+1)*(widthFloat+1) > 16384 || (stepsFloat+1)*(widthFloat+1)*(clearanceFloat+1) > 4_000_000 {
		return []string{"traversal sampling budget exceeded; reduce domain anisotropy or traversal extent"}, nil
	}
	steps, widthSteps, clearanceSteps := int(stepsFloat), int(widthFloat), int(clearanceFloat)
	missingSupport, blocked, missingSurface := false, false, false
	meshBlocked := false
	corridor := Box{Vec3{0, t.SurfaceTolerance, -t.Width / 2}, Vec3{length, t.Height, t.Width / 2}}
	epsilon := d.Box.Max.Sub(d.Box.Min).Len() * 1e-9
	for i := 0; i < len(mesh.Indices); i += 3 {
		if i%12288 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		var tri [3]Vec3
		for j := range tri {
			q := mesh.Positions[mesh.Indices[i+j]].Sub(a)
			tri[j] = Vec3{q.Dot(forward), q.Y, q.Dot(lateral)}
		}
		if triangleBoxInterior(tri, corridor, epsilon) {
			meshBlocked = true
			break
		}
	}
	deck, err := newDeckSurfaceIndex(ctx, mesh, d, a.Y, t.SurfaceTolerance)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []string{err.Error()}, nil
	}
	for i := 0; i <= steps; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		center := a.Add(b.Sub(a).Mul(float64(i) / float64(steps)))
		for j := 0; j <= widthSteps; j++ {
			p := center.Add(lateral.Mul(t.Width * (float64(j)/float64(widthSteps) - .5)))
			// A supported floor needs material below it, plus an actual upward
			// mesh intersection near the declared floor. A connected solid
			// somewhere below a hole does not satisfy either condition.
			probe := p.Add(Vec3{0, -(t.DeckDepth + t.SurfaceTolerance) / 2, 0})
			if result.SignedValue(probe) >= 0 {
				missingSupport = true
			}
			if !missingSurface && !deck.at(p, t.SurfaceTolerance) {
				missingSurface = true
			}
			for k := 0; k <= clearanceSteps; k++ {
				height := t.SurfaceTolerance + (t.Height-t.SurfaceTolerance)*float64(k)/float64(clearanceSteps)
				if result.SignedValue(p.Add(Vec3{0, height, 0})) < 0 {
					blocked = true
					break
				}
			}
		}
	}
	problems := []string{}
	if missingSupport {
		problems = append(problems, "traversal strip or endpoint lacks deck support")
	}
	if missingSurface {
		problems = append(problems, "traversal strip or endpoint lacks an upward walkable mesh surface within surface_tolerance")
	}
	if blocked {
		problems = append(problems, "traversal headroom is obstructed")
	}
	if meshBlocked {
		problems = append(problems, "mesh intrudes into traversal headroom between grid samples")
	}
	return problems, nil
}

func deckSurfaceAt(mesh *Mesh, p Vec3, tolerance float64) bool {
	for i := 0; i < len(mesh.Indices); i += 3 {
		if deckTriangleAt(mesh, i, p, tolerance) {
			return true
		}
	}
	return false
}

// The index changes candidate lookup only; the exact triangle predicate is
// shared with the brute-force reference. A bounded global list handles large
// triangles without copying them into every bin. Per-bin and total limits also
// bound pathological overlapping geometry and query work.
type deckSurfaceIndex struct {
	mesh   *Mesh
	domain Domain
	bins   [][]int
	large  []int
	n      int
}

func (index *deckSurfaceIndex) cell(x, z float64) (int, int) {
	b := index.domain.Box
	i := int(math.Max(0, math.Min(float64(index.n-1), math.Floor((x-b.Min.X)/(b.Max.X-b.Min.X)*float64(index.n)))))
	j := int(math.Max(0, math.Min(float64(index.n-1), math.Floor((z-b.Min.Z)/(b.Max.Z-b.Min.Z)*float64(index.n)))))
	return i, j
}

func newDeckSurfaceIndex(ctx context.Context, mesh *Mesh, d Domain, floor, tolerance float64) (*deckSurfaceIndex, error) {
	const maxReferences = 1_000_000
	const maxPerBin = 512
	const maxLarge = 64
	index := &deckSurfaceIndex{mesh: mesh, domain: d, n: min(64, d.Resolution)}
	index.bins = make([][]int, index.n*index.n)
	references := 0
	for i := 0; i < len(mesh.Indices); i += 3 {
		if i%12288 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		a, b, c := mesh.Positions[mesh.Indices[i]], mesh.Positions[mesh.Indices[i+1]], mesh.Positions[mesh.Indices[i+2]]
		normal := b.Sub(a).Cross(c.Sub(a))
		if normal.Y <= .7*normal.Len() {
			continue
		}
		// Barycentric tolerances can permit a height just outside the vertex
		// interval. Expand by a conservative bound before discarding a face.
		heightEpsilon := 1e-9 * (1 + 4*(math.Abs(a.Y)+math.Abs(b.Y)+math.Abs(c.Y)))
		if math.Min(a.Y, math.Min(b.Y, c.Y)) > floor+tolerance+heightEpsilon || math.Max(a.Y, math.Max(b.Y, c.Y)) < floor-tolerance-heightEpsilon {
			continue
		}
		x0, z0 := index.cell(math.Min(a.X, math.Min(b.X, c.X))-1e-9, math.Min(a.Z, math.Min(b.Z, c.Z))-1e-9)
		x1, z1 := index.cell(math.Max(a.X, math.Max(b.X, c.X))+1e-9, math.Max(a.Z, math.Max(b.Z, c.Z))+1e-9)
		count := (x1 - x0 + 1) * (z1 - z0 + 1)
		if count > 256 {
			if len(index.large) >= maxLarge {
				return nil, fmt.Errorf("traversal mesh lookup budget exceeded: more than %d oversized deck triangles", maxLarge)
			}
			index.large = append(index.large, i)
			continue
		}
		if references+count > maxReferences {
			return nil, fmt.Errorf("traversal mesh lookup budget exceeded: more than %d triangle references", maxReferences)
		}
		for z := z0; z <= z1; z++ {
			for x := x0; x <= x1; x++ {
				bin := x + index.n*z
				if len(index.bins[bin]) >= maxPerBin {
					return nil, fmt.Errorf("traversal mesh lookup budget exceeded: more than %d deck triangles in one spatial bin", maxPerBin)
				}
				index.bins[bin] = append(index.bins[bin], i)
			}
		}
		references += count
	}
	return index, nil
}

func (index *deckSurfaceIndex) at(p Vec3, tolerance float64) bool {
	x, z := index.cell(p.X, p.Z)
	for _, triangle := range index.bins[x+index.n*z] {
		if deckTriangleAt(index.mesh, triangle, p, tolerance) {
			return true
		}
	}
	for _, triangle := range index.large {
		if deckTriangleAt(index.mesh, triangle, p, tolerance) {
			return true
		}
	}
	return false
}

func deckTriangleAt(mesh *Mesh, i int, p Vec3, tolerance float64) bool {
	a, b, c := mesh.Positions[mesh.Indices[i]], mesh.Positions[mesh.Indices[i+1]], mesh.Positions[mesh.Indices[i+2]]
	if p.X < math.Min(a.X, math.Min(b.X, c.X))-1e-9 || p.X > math.Max(a.X, math.Max(b.X, c.X))+1e-9 || p.Z < math.Min(a.Z, math.Min(b.Z, c.Z))-1e-9 || p.Z > math.Max(a.Z, math.Max(b.Z, c.Z))+1e-9 {
		return false
	}
	normal := b.Sub(a).Cross(c.Sub(a))
	if normal.Y <= .7*normal.Len() {
		return false
	}
	den := (b.Z-c.Z)*(a.X-c.X) + (c.X-b.X)*(a.Z-c.Z)
	if math.Abs(den) < 1e-15 {
		return false
	}
	u := ((b.Z-c.Z)*(p.X-c.X) + (c.X-b.X)*(p.Z-c.Z)) / den
	v := ((c.Z-a.Z)*(p.X-c.X) + (a.X-c.X)*(p.Z-c.Z)) / den
	w := 1 - u - v
	if u < -1e-9 || v < -1e-9 || w < -1e-9 {
		return false
	}
	y := u*a.Y + v*b.Y + w*c.Y
	return math.Abs(y-p.Y) <= tolerance+1e-9
}

// Clip the triangle to a slightly shrunken box. This detects triangles crossing
// a forbidden interior even if all three vertices lie outside the box.
func triangleBoxInterior(tri [3]Vec3, b Box, epsilon float64) bool {
	lo, hi := b.Min.Add(Vec3{epsilon, epsilon, epsilon}), b.Max.Sub(Vec3{epsilon, epsilon, epsilon})
	if lo.X >= hi.X || lo.Y >= hi.Y || lo.Z >= hi.Z {
		return false
	}
	if math.Max(tri[0].X, math.Max(tri[1].X, tri[2].X)) < lo.X || math.Min(tri[0].X, math.Min(tri[1].X, tri[2].X)) > hi.X ||
		math.Max(tri[0].Y, math.Max(tri[1].Y, tri[2].Y)) < lo.Y || math.Min(tri[0].Y, math.Min(tri[1].Y, tri[2].Y)) > hi.Y ||
		math.Max(tri[0].Z, math.Max(tri[1].Z, tri[2].Z)) < lo.Z || math.Min(tri[0].Z, math.Min(tri[1].Z, tri[2].Z)) > hi.Z {
		return false
	}
	poly := append([]Vec3{}, tri[:]...)
	for _, plane := range []struct {
		axis        int
		bound, sign float64
	}{{0, lo.X, 1}, {0, hi.X, -1}, {1, lo.Y, 1}, {1, hi.Y, -1}, {2, lo.Z, 1}, {2, hi.Z, -1}} {
		component := func(p Vec3) float64 { return []float64{p.X, p.Y, p.Z}[plane.axis] }
		distance := func(p Vec3) float64 { return (component(p) - plane.bound) * plane.sign }
		if len(poly) == 0 {
			return false
		}
		next := make([]Vec3, 0, len(poly)+1)
		prev := poly[len(poly)-1]
		prevD := distance(prev)
		for _, cur := range poly {
			curD := distance(cur)
			if (prevD >= 0) != (curD >= 0) {
				next = append(next, prev.Add(cur.Sub(prev).Mul(prevD/(prevD-curD))))
			}
			if curD >= 0 {
				next = append(next, cur)
			}
			prev, prevD = cur, curD
		}
		poly = next
	}
	return len(poly) > 0
}
