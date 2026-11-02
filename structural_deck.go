package progen

import (
	"context"
	"fmt"
	"math"
)

// WalkingChecks is diagnostic for the explicitly relaxed source only. Hard
// mesh/source validity still controls export; this is never a walkability claim.
type WalkingChecks struct {
	Policy    string   `json:"policy"`
	Evaluated bool     `json:"evaluated"`
	Passed    bool     `json:"passed"`
	Problems  []string `json:"problems"`
}

func reportWalkingChecks(ctx context.Context, s ConstraintSpec, v *VolumeGrid, m *Mesh) (*WalkingChecks, error) {
	report := &WalkingChecks{Policy: "diagnostic-only; not game-ready"}
	if v == nil || m == nil {
		report.Problems = []string{"walking diagnostics unavailable: missing volume or mesh"}
		return report, nil
	}
	q, err := quantizedConditionedMesh(m, v.Space)
	if err != nil {
		report.Problems = []string{err.Error()}
		return report, nil
	}
	report.Problems, err = validateTraversal(ctx, s.Traversal, v, q)
	report.Evaluated = err == nil
	report.Passed = err == nil && len(report.Problems) == 0
	return report, err
}

func connectedAnchors(ctx context.Context, s ConstraintSpec, v *VolumeGrid) (bool, error) {
	if len(s.Anchors) < 2 {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	n := v.Space.Resolution
	seen := make([]bool, len(v.Values))
	queue := []int{}
	for i, value := range v.Values {
		if value < 0 && s.Anchors[0].Region.Contains(v.Space.Point(i%n, (i/n)%n, i/(n*n))) {
			queue = append(queue, i)
			seen[i] = true
			break
		}
	}
	for head := 0; head < len(queue); head++ {
		if head%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return false, err
			}
		}
		i := queue[head]
		x, y, z := i%n, (i/n)%n, i/(n*n)
		for _, p := range [][3]int{{x - 1, y, z}, {x + 1, y, z}, {x, y - 1, z}, {x, y + 1, z}, {x, y, z - 1}, {x, y, z + 1}} {
			if p[0] < 0 || p[1] < 0 || p[2] < 0 || p[0] >= n || p[1] >= n || p[2] >= n {
				continue
			}
			j := p[0] + n*(p[1]+n*p[2])
			if !seen[j] && v.Values[j] < 0 {
				seen[j] = true
				queue = append(queue, j)
			}
		}
	}
	for _, anchor := range s.Anchors {
		found := false
		for i, value := range v.Values {
			if i%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return false, err
				}
			}
			if anchor.Region.Contains(v.Space.Point(i%n, (i/n)%n, i/(n*n))) && value < 0 {
				found = true
				if !seen[i] {
					return false, nil
				}
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

type structuralDeckOperator struct {
	density, contour float64
	source           *ConditionedSource
}

func (op structuralDeckOperator) BindSource(s *ConditionedSource, _ []byte) Operator {
	op.source = s
	return op
}
func (op structuralDeckOperator) Run(ctx context.Context, d Domain, _ uint64, in Inputs) (Outputs, error) {
	if op.source == nil || op.source.Spec.DeckPolicy != "structural-experiment" {
		return nil, fmt.Errorf("structural deck requires explicit less-protected source")
	}
	if !finite(op.density) || !finite(op.contour) || op.density < 0 || op.density > .18 || op.contour < 0 || op.contour > 1 {
		return nil, fmt.Errorf("invalid structural deck controls")
	}
	field := in["field"].Volume
	if field == nil || !sameDomain(field.Space, d) {
		return nil, fmt.Errorf("incompatible structural field")
	}
	v := &VolumeGrid{Space: d, Values: make([]float64, len(field.Values))}
	n := d.Resolution
	for z := 0; z < n; z++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				p := d.Point(x, y, z)
				shape := math.Tanh(field.SignedValue(Vec3{p.X, .2, p.Z}) / .10)
				side := math.Tanh(field.SignedValue(Vec3{p.X, 0, math.Copysign(.85, p.Z)}) / .08)
				width := .84 + .18*op.contour*side
				top := .12 + .22*op.contour*shape
				bottom := -.42 - .18*op.contour*shape
				envelope := max(math.Abs(p.X)-3.78, math.Abs(p.Z)-width, p.Y-top, bottom-p.Y)
				// The same solid/void field continues up the walls. This union broadens
				// their feet rather than retaining a separate rectangular deck slab.
				wallEnvelope := max(.90-math.Abs(p.Z), -.25-p.Y)
				bias := op.density * smoothFloor((1.04-math.Abs(p.Z))/.3) * smoothFloor((.55-p.Y)/.3)
				value := max(field.Values[x+n*(y+n*z)]-bias, min(envelope, wallEnvelope))
				// A one-cell guard around the explicitly reduced support contract keeps
				// marching tetrahedra from cutting anchor/core corners between samples.
				// This is a narrow internal spine and end attachments, not the full deck.
				padding := d.Box.Max.Sub(d.Box.Min).Mul(1 / float64(n-1))
				for _, support := range op.source.Spec.Required {
					value = min(value, boxSigned(p, Box{support.Min.Sub(padding), support.Max.Add(padding)}))
				}
				v.Values[x+n*(y+n*z)] = value
			}
		}
	}
	return Outputs{"volume": {Kind: "volume", Volume: v}}, nil
}

func registerStructuralDeck(r *Registry) {
	err := r.Register(Descriptor{ID: "structural_deck", Version: "1", Inputs: map[string]Port{"field": {"volume", "solid"}}, Outputs: map[string]Port{"volume": {"volume", "solid"}}, Parameters: map[string]Param{"density": parameter(.09, 0, .18, false, true), "contour": parameter(1, 0, 1, false, true)}, Requirements: "Shared-field structural floor; explicitly less-protected source; walking checks diagnostic only"}, func(p map[string]float64) (Operator, error) {
		return structuralDeckOperator{density: p["density"], contour: p["contour"]}, nil
	})
	if err != nil {
		panic(err)
	}
}

func BridgeStructuralRecipe() Recipe {
	r, err := sourceRecipe(bridgeGLB, bridgeStructuralSidecar)
	if err != nil {
		panic(err)
	}
	return r
}

func withStructuralDeck(r Recipe) Recipe {
	r.Name = "Bridge / structural floor (less protected)"
	node := Node{ID: "deck", Operator: "structural_deck", Version: "1", Params: map[string]float64{"density": .09, "contour": 1}, Inputs: map[string]Reference{"field": {"deform", "volume"}}}
	for k, v := range node.Params {
		if bounds, ok := r.Source.Spec.ParameterBounds["deck"][k]; ok {
			node.Params[k] = max(bounds.Min, min(bounds.Max, v))
		}
	}
	r.Nodes = append(r.Nodes, node)
	for i := range r.Nodes {
		if r.Nodes[i].ID == "condition" {
			r.Nodes[i].Inputs["decoration"] = Reference{"deck", "volume"}
		}
	}
	return r
}
