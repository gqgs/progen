package progen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

type Vec3 struct{ X, Y, Z float64 }

func (a Vec3) Add(b Vec3) Vec3    { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Sub(b Vec3) Vec3    { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Mul(s float64) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }
func (a Vec3) Dot(b Vec3) float64 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func (a Vec3) Cross(b Vec3) Vec3 {
	return Vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}
func (a Vec3) Len() float64 { return math.Sqrt(a.Dot(a)) }
func (a Vec3) Unit() Vec3 {
	if n := a.Len(); n > 1e-12 {
		return a.Mul(1 / n)
	}
	return Vec3{}
}
func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func (a Vec3) Finite() bool { return finite(a.X) && finite(a.Y) && finite(a.Z) }

type Box struct{ Min, Max Vec3 }

func (b Box) Contains(p Vec3) bool {
	return p.X >= b.Min.X && p.Y >= b.Min.Y && p.Z >= b.Min.Z && p.X <= b.Max.X && p.Y <= b.Max.Y && p.Z <= b.Max.Z
}

type SpatialDomain interface {
	Bounds() Box
	Coordinates() string
	Units() string
}
type Domain struct {
	Box        Box    `json:"bounds"`
	Resolution int    `json:"resolution"`
	Frame      string `json:"frame"`
	Unit       string `json:"units"`
	Boundary   string `json:"boundary"`
	Exclusions []Box  `json:"exclusions,omitempty"`
}

func (d Domain) Bounds() Box         { return d.Box }
func (d Domain) Coordinates() string { return d.Frame }
func (d Domain) Units() string       { return d.Unit }
func (d Domain) Validate() error {
	s := d.Box.Max.Sub(d.Box.Min)
	if !s.Finite() || s.X <= 0 || s.Y <= 0 || s.Z <= 0 || !d.Box.Min.Finite() || !d.Box.Max.Finite() {
		return fmt.Errorf("invalid domain bounds")
	}
	if d.Resolution < 8 || d.Resolution > 128 {
		return fmt.Errorf("resolution must be 8..128")
	}
	if d.Frame != "right-handed-y-up" || d.Unit != "meters" || d.Boundary != "dirichlet" {
		return fmt.Errorf("unsupported coordinate system, units or boundary")
	}
	for _, b := range d.Exclusions {
		if !b.Min.Finite() || !b.Max.Finite() || b.Min.X >= b.Max.X || b.Min.Y >= b.Max.Y || b.Min.Z >= b.Max.Z {
			return fmt.Errorf("invalid exclusion")
		}
	}
	return nil
}
func (d Domain) Allowed(p Vec3) bool {
	if !d.Box.Contains(p) {
		return false
	}
	for _, b := range d.Exclusions {
		if b.Contains(p) {
			return false
		}
	}
	return true
}
func (d Domain) Point(x, y, z int) Vec3 {
	s := d.Box.Max.Sub(d.Box.Min).Mul(1 / float64(d.Resolution-1))
	return d.Box.Min.Add(Vec3{float64(x) * s.X, float64(y) * s.Y, float64(z) * s.Z})
}

type ScalarField interface {
	Domain() Domain
	Sample(Vec3) float64
}
type VectorField interface {
	Domain() Domain
	SampleVector(Vec3) Vec3
}
type Volume interface {
	Domain() Domain
	SignedValue(Vec3) float64
}
type ScalarGrid struct {
	Space  Domain    `json:"domain"`
	Values []float64 `json:"values"`
}

func (g *ScalarGrid) Domain() Domain { return g.Space }
func (g *ScalarGrid) Sample(p Vec3) float64 {
	idx, w := gridWeights(g.Space, p)
	v := 0.
	for i, j := range idx {
		v += g.Values[j] * w[i]
	}
	return v
}

type VectorGrid struct {
	Space  Domain `json:"domain"`
	Values []Vec3 `json:"values"`
}

func (g *VectorGrid) Domain() Domain { return g.Space }
func (g *VectorGrid) SampleVector(p Vec3) Vec3 {
	idx, w := gridWeights(g.Space, p)
	v := Vec3{}
	for i, j := range idx {
		v = v.Add(g.Values[j].Mul(w[i]))
	}
	return v
}

type VolumeGrid struct {
	Space  Domain    `json:"domain"`
	Values []float64 `json:"values"`
}

func (g *VolumeGrid) Domain() Domain             { return g.Space }
func (g *VolumeGrid) SignedValue(p Vec3) float64 { return (&ScalarGrid{g.Space, g.Values}).Sample(p) }
func gridWeights(d Domain, p Vec3) ([8]int, [8]float64) {
	s := d.Box.Max.Sub(d.Box.Min)
	q := p.Sub(d.Box.Min)
	n := d.Resolution
	t := [3]float64{q.X / s.X * float64(n-1), q.Y / s.Y * float64(n-1), q.Z / s.Z * float64(n-1)}
	var a [3]int
	for i := range t {
		t[i] = math.Max(0, math.Min(float64(n-1), t[i]))
		a[i] = min(n-2, int(t[i]))
		t[i] -= float64(a[i])
	}
	var idx [8]int
	var w [8]float64
	for k := 0; k < 8; k++ {
		x, y, z := k&1, (k>>1)&1, (k>>2)&1
		idx[k] = (a[0] + x) + n*((a[1]+y)+n*(a[2]+z))
		w[k] = 1
		for j, b := range []int{x, y, z} {
			if b == 1 {
				w[k] *= t[j]
			} else {
				w[k] *= 1 - t[j]
			}
		}
	}
	return idx, w
}

type Vertex struct {
	Position   Vec3    `json:"position"`
	Radius     float64 `json:"radius"`
	Parent     int     `json:"parent"`
	Generation int     `json:"generation"`
}
type Graph struct {
	Space           Domain   `json:"domain"`
	Vertices        []Vertex `json:"vertices"`
	AttractionCount int      `json:"attraction_count"`
	Reached         int      `json:"reached"`
}
type Mesh struct {
	Positions []Vec3   `json:"positions"`
	Normals   []Vec3   `json:"normals"`
	Indices   []uint32 `json:"indices"`
}
type FieldGenerator interface {
	Generate(context.Context, Domain) (*ScalarGrid, error)
}
type GraphGrowth interface {
	Grow(context.Context, VectorField, uint64) (*Graph, error)
}
type Solidifier interface {
	Solidify(context.Context, *Graph) (*VolumeGrid, error)
}
type SurfaceModifier interface {
	Modify(context.Context, Volume) (*VolumeGrid, error)
}
type Mesher interface {
	Mesh(context.Context, Volume) (*Mesh, error)
}
type Artifact struct {
	Kind   string      `json:"kind"`
	Scalar *ScalarGrid `json:"scalar,omitempty"`
	Vector *VectorGrid `json:"vector,omitempty"`
	Graph  *Graph      `json:"graph,omitempty"`
	Volume *VolumeGrid `json:"volume,omitempty"`
	Mesh   *Mesh       `json:"mesh,omitempty"`
}

func (a Artifact) Space() (Domain, bool) {
	switch a.Kind {
	case "scalar":
		if a.Scalar != nil {
			return a.Scalar.Space, true
		}
	case "vector":
		if a.Vector != nil {
			return a.Vector.Space, true
		}
	case "graph":
		if a.Graph != nil {
			return a.Graph.Space, true
		}
	case "volume":
		if a.Volume != nil {
			return a.Volume.Space, true
		}
	}
	return Domain{}, false
}
func sameDomain(a, b Domain) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Seed preserves all 64 bits across JSON clients, including JavaScript browsers.
// It accepts unsigned decimal numbers or strings and always emits a string.
type Seed uint64

func (s Seed) MarshalJSON() ([]byte, error) { return json.Marshal(strconv.FormatUint(uint64(s), 10)) }
func (s *Seed) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	text := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
	}
	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return fmt.Errorf("seed must be an unsigned 64-bit decimal integer: %w", err)
	}
	*s = Seed(n)
	return nil
}
