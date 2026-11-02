package progen

import (
	"context"
	"fmt"
	"math"
)

// FloorReliefSpec explicitly authorizes a bounded volume deformation separately
// from porous wall replacement. Existing sources omit it and retain their contract.
type FloorReliefSpec struct {
	Region Box `json:"region"`
}

type floorReliefOperator struct {
	strength, depth float64
	source          *ConditionedSource
}

func (op floorReliefOperator) BindSource(s *ConditionedSource, _ []byte) Operator {
	op.source = s
	return op
}

func (op floorReliefOperator) Run(ctx context.Context, d Domain, _ uint64, in Inputs) (Outputs, error) {
	if op.source == nil || op.source.Spec.FloorRelief == nil {
		return nil, fmt.Errorf("floor relief requires an explicit source floor region")
	}
	v, field := in["volume"].Volume, in["field"].Volume
	if v == nil || field == nil || !sameDomain(v.Space, d) || !sameDomain(field.Space, d) {
		return nil, fmt.Errorf("floor relief requires compatible volume and shared field")
	}
	out, err := floorRelief(ctx, v, field, op.source.Spec, op.strength, op.depth)
	return Outputs{"volume": {Kind: "volume", Volume: out}}, err
}

func smoothFloor(t float64) float64 { t = max(0, min(1, t)); return t * t * (3 - 2*t) }

func floorRelief(ctx context.Context, v, field *VolumeGrid, s ConstraintSpec, strength, depth float64) (*VolumeGrid, error) {
	if !finite(strength) || !finite(depth) || strength < 0 || strength > 1 || depth < 0 || depth > .06 {
		return nil, fmt.Errorf("floor relief requires strength 0..1 and depth 0..0.06 meters")
	}
	out := &VolumeGrid{Space: v.Space, Values: append([]float64(nil), v.Values...)}
	if strength == 0 || depth == 0 {
		return out, nil
	}
	box := s.FloorRelief.Region
	n := v.Space.Resolution
	floor := s.Traversal.Endpoints[0].Y
	for z := 0; z < n; z++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < n; x++ {
			p := v.Space.Point(x, 0, z)
			p.Y = floor
			// A fixed physical filter radius, independent of grid resolution. The same
			// warped TPMS/Voronoi field feeds walls and floor; no second noise seed.
			f := 0.0
			for dz := -1; dz <= 1; dz++ {
				for dx := -1; dx <= 1; dx++ {
					w := 1.0
					if dx == 0 {
						w *= 2
					}
					if dz == 0 {
						w *= 2
					}
					f += w * field.SignedValue(p.Add(Vec3{float64(dx) * .18, 0, float64(dz) * .18})) / 16
				}
			}
			edge := smoothFloor((math.Abs(p.Z) - s.Traversal.Width/2) / .3)
			footprint := smoothFloor(min(p.X-box.Min.X, box.Max.X-p.X)/.4) * smoothFloor(min(p.Z-box.Min.Z, box.Max.Z-p.Z)/.18)
			h := strength * depth * math.Tanh(f/.08) * (1 + 1.5*edge) * footprint
			// Bound recesses above the built-in bridge core; only outer shoulders
			// may rise further. Every result still needs protected-region validation.
			h = max(-.06, min(.06+.09*edge, h))
			for y := 0; y < n; y++ {
				q := v.Space.Point(x, y, z)
				i := x + n*(y+n*z)
				if !box.Contains(q) {
					continue
				}
				fade := smoothFloor((q.Y-floor+.16)/.08) * smoothFloor((box.Max.Y-q.Y)/.12)
				if h*fade != 0 {
					out.Values[i] = v.SignedValue(q.Sub(Vec3{0, h * fade, 0}))
				}
			}
		}
	}
	return out, nil
}

func BridgeReliefRecipe() Recipe {
	r, err := sourceRecipe(bridgeGLB, bridgeReliefSidecar)
	if err != nil {
		panic(err)
	}
	return r
}

func withFloorRelief(r Recipe) Recipe {
	r.Name = "Bridge / floor relief"
	r.Nodes = append(r.Nodes, Node{ID: "floor", Operator: "floor_relief", Version: "1", Params: map[string]float64{"strength": 1, "depth": .06}, Inputs: map[string]Reference{"volume": {"condition", "volume"}, "field": {"deform", "volume"}}})
	for i := range r.Nodes {
		if r.Nodes[i].ID == "surface" {
			r.Nodes[i].Inputs["volume"] = Reference{"floor", "volume"}
		}
	}
	for key, value := range r.Nodes[len(r.Nodes)-1].Params {
		if bounds, ok := r.Source.Spec.ParameterBounds["floor"][key]; ok {
			r.Nodes[len(r.Nodes)-1].Params[key] = max(bounds.Min, min(bounds.Max, value))
		}
	}
	r.Outputs["volume"] = Reference{"floor", "volume"}
	return r
}
