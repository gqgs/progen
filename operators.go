package progen

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
)

type Eigenmode struct {
	Order  int
	Weight float64
}

func modeAt(order int, lengths Vec3) [3]int {
	type mode struct {
		v [3]int
		l float64
	}
	var modes []mode
	for x := 1; x <= 12; x++ {
		for y := 1; y <= 12; y++ {
			for z := 1; z <= 12; z++ {
				modes = append(modes, mode{[3]int{x, y, z}, float64(x*x)/(lengths.X*lengths.X) + float64(y*y)/(lengths.Y*lengths.Y) + float64(z*z)/(lengths.Z*lengths.Z)})
			}
		}
	}
	sort.SliceStable(modes, func(i, j int) bool { return modes[i].l < modes[j].l })
	return modes[order-1].v
}
func (e Eigenmode) Generate(ctx context.Context, d Domain) (*ScalarGrid, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if e.Order < 1 || e.Order > 12 || !finite(e.Weight) {
		return nil, fmt.Errorf("invalid eigenmode configuration")
	}
	m := modeAt(e.Order, d.Box.Max.Sub(d.Box.Min))
	n := d.Resolution
	g := &ScalarGrid{d, make([]float64, n*n*n)}
	for z := 0; z < n; z++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				g.Values[x+n*(y+n*z)] = e.Weight * math.Sin(math.Pi*float64(m[0]*x)/float64(n-1)) * math.Sin(math.Pi*float64(m[1]*y)/float64(n-1)) * math.Sin(math.Pi*float64(m[2]*z)/float64(n-1))
			}
		}
	}
	return g, nil
}
func Gradient(ctx context.Context, f ScalarField) (*VectorGrid, error) {
	d := f.Domain()
	n := d.Resolution
	g := &VectorGrid{d, make([]Vec3, n*n*n)}
	step := d.Box.Max.Sub(d.Box.Min).Mul(1 / float64(n-1))
	for z := 0; z < n; z++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				v := [3]float64{}
				a := [3]int{x, y, z}
				for k, h := range []float64{step.X, step.Y, step.Z} {
					lo, hi := a, a
					lo[k] = max(0, a[k]-1)
					hi[k] = min(n-1, a[k]+1)
					v[k] = (f.Sample(d.Point(hi[0], hi[1], hi[2])) - f.Sample(d.Point(lo[0], lo[1], lo[2]))) / (h * float64(hi[k]-lo[k]))
				}
				g.Values[x+n*(y+n*z)] = Vec3{v[0], v[1], v[2]}
			}
		}
	}
	return g, nil
}

type Branching struct {
	Attractions, Iterations, MaxVertices    int
	Step, Influence, Kill, Guidance, Radius float64
}
type bucket [3]int

func cell(p Vec3, s float64) bucket {
	return bucket{int(math.Floor(p.X / s)), int(math.Floor(p.Y / s)), int(math.Floor(p.Z / s))}
}
func segmentAllowed(d Domain, a, b Vec3) bool {
	if !d.Allowed(a) || !d.Allowed(b) {
		return false
	}
	delta := b.Sub(a)
	for _, box := range d.Exclusions {
		lo, hi := 0., 1.
		for i, av := range []float64{a.X, a.Y, a.Z} {
			dv := []float64{delta.X, delta.Y, delta.Z}[i]
			mn := []float64{box.Min.X, box.Min.Y, box.Min.Z}[i]
			mx := []float64{box.Max.X, box.Max.Y, box.Max.Z}[i]
			if math.Abs(dv) < 1e-15 {
				if av < mn || av > mx {
					lo = 2
					break
				}
				continue
			}
			t, u := (mn-av)/dv, (mx-av)/dv
			if t > u {
				t, u = u, t
			}
			lo = math.Max(lo, t)
			hi = math.Min(hi, u)
		}
		if lo <= hi {
			return false
		}
	}
	return true
}
func (b Branching) Grow(ctx context.Context, f VectorField, seed uint64) (*Graph, error) {
	d := f.Domain()
	if b.Attractions < 1 || b.Attractions > 10000 || b.Iterations < 1 || b.Iterations > 1024 || b.MaxVertices < 2 || b.MaxVertices > 8192 || b.Step <= 0 || b.Influence <= 0 || b.Kill <= 0 || b.Radius <= 0 {
		return nil, fmt.Errorf("invalid growth budget")
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	s := d.Box.Max.Sub(d.Box.Min)
	center := d.Box.Min.Add(s.Mul(.5))
	root := Vec3{center.X, d.Box.Min.Y + s.Y*.12, center.Z}
	if !d.Allowed(root) {
		return nil, fmt.Errorf("root intersects exclusion")
	}
	g := &Graph{Space: d, Vertices: []Vertex{{root, b.Radius, -1, 0}}, AttractionCount: b.Attractions}
	points := make([]Vec3, 0, b.Attractions)
	for tries := 0; len(points) < b.Attractions && tries < b.Attractions*100; tries++ {
		if tries%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		p := Vec3{rng.Float64()*2 - 1, rng.Float64()*2 - 1, rng.Float64()*2 - 1}
		if p.Dot(p) > 1 {
			continue
		}
		p = Vec3{center.X + p.X*s.X*.38, d.Box.Min.Y + s.Y*(.58+p.Y*.31), center.Z + p.Z*s.Z*.38}
		if d.Allowed(p) {
			points = append(points, p)
		}
	}
	if len(points) != b.Attractions {
		return nil, fmt.Errorf("cannot place attraction points within constraints")
	}
	for iter := 0; iter < b.Iterations && len(points) > 0 && len(g.Vertices) < b.MaxVertices; iter++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		buckets := map[bucket][]int{}
		for i, v := range g.Vertices {
			k := cell(v.Position, b.Influence)
			buckets[k] = append(buckets[k], i)
		}
		sums := make([]Vec3, len(g.Vertices))
		counts := make([]int, len(g.Vertices))
		remaining := points[:0]
		for _, p := range points {
			k := cell(p, b.Influence)
			best := -1
			dist := b.Influence
			for dx := -1; dx <= 1; dx++ {
				for dy := -1; dy <= 1; dy++ {
					for dz := -1; dz <= 1; dz++ {
						for _, i := range buckets[bucket{k[0] + dx, k[1] + dy, k[2] + dz}] {
							v := p.Sub(g.Vertices[i].Position)
							dd := v.Len()
							if dd < dist || (dd == dist && (best < 0 || i < best)) {
								dist = dd
								best = i
							}
						}
					}
				}
			}
			if best >= 0 && dist < b.Kill {
				g.Reached++
				continue
			}
			remaining = append(remaining, p)
			if best >= 0 {
				sums[best] = sums[best].Add(p.Sub(g.Vertices[best].Position).Unit())
				counts[best]++
			}
		}
		points = remaining
		added := 0
		original := len(g.Vertices)
		for i := 0; i < original && len(g.Vertices) < b.MaxVertices; i++ {
			if counts[i] == 0 {
				continue
			}
			v := g.Vertices[i]
			attract := sums[i].Unit()
			direction := attract.Add(f.SampleVector(v.Position).Unit().Mul(b.Guidance)).Unit()
			if direction.Len() < 1e-10 {
				direction = attract
			}
			next := v.Position.Add(direction.Mul(b.Step))
			if !segmentAllowed(d, v.Position, next) {
				next = v.Position.Add(attract.Mul(b.Step))
				if !segmentAllowed(d, v.Position, next) {
					continue
				}
			}
			duplicate := false
			for _, j := range buckets[cell(next, b.Influence)] {
				if next.Sub(g.Vertices[j].Position).Len() < b.Step*.4 {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			g.Vertices = append(g.Vertices, Vertex{next, b.Radius, i, iter + 1})
			buckets[cell(next, b.Influence)] = append(buckets[cell(next, b.Influence)], len(g.Vertices)-1)
			added++
		}
		if len(points) == 0 {
			break
		}
		if added == 0 {
			tip := len(g.Vertices) - 1
			near := points[0]
			for _, p := range points {
				if p.Sub(g.Vertices[tip].Position).Len() < near.Sub(g.Vertices[tip].Position).Len() {
					near = p
				}
			}
			v := g.Vertices[tip]
			next := v.Position.Add(near.Sub(v.Position).Unit().Mul(b.Step))
			if !segmentAllowed(d, v.Position, next) {
				break
			}
			g.Vertices = append(g.Vertices, Vertex{next, b.Radius, tip, iter + 1})
		}
	}
	children := make([]int, len(g.Vertices))
	for i := len(g.Vertices) - 1; i >= 0; i-- {
		children[i]++
		if p := g.Vertices[i].Parent; p >= 0 {
			children[p] += children[i]
		}
		g.Vertices[i].Radius = b.Radius * math.Pow(float64(children[i]), .22)
	}
	return g, nil
}

type Capsules struct{}

func (Capsules) Solidify(ctx context.Context, g *Graph) (*VolumeGrid, error) {
	d := g.Space
	n := d.Resolution
	s := d.Box.Max.Sub(d.Box.Min)
	v := &VolumeGrid{d, make([]float64, n*n*n)}
	for i := range v.Values {
		v.Values[i] = s.Len()
	}
	for j, node := range g.Vertices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if node.Parent < 0 {
			continue
		}
		if node.Parent >= j {
			return nil, fmt.Errorf("invalid graph parent")
		}
		a := g.Vertices[node.Parent].Position
		b := node.Position
		r := math.Max(node.Radius, g.Vertices[node.Parent].Radius)
		pad := r + 2*s.Len()/float64(n-1)
		low := Vec3{math.Min(a.X, b.X) - pad, math.Min(a.Y, b.Y) - pad, math.Min(a.Z, b.Z) - pad}
		high := Vec3{math.Max(a.X, b.X) + pad, math.Max(a.Y, b.Y) + pad, math.Max(a.Z, b.Z) + pad}
		convert := func(p Vec3) [3]int {
			q := p.Sub(d.Box.Min)
			return [3]int{int(q.X / s.X * float64(n-1)), int(q.Y / s.Y * float64(n-1)), int(q.Z / s.Z * float64(n-1))}
		}
		lo, hi := convert(low), convert(high)
		ab := b.Sub(a)
		den := ab.Dot(ab)
		for z := max(0, lo[2]); z <= min(n-1, hi[2]+1); z++ {
			for y := max(0, lo[1]); y <= min(n-1, hi[1]+1); y++ {
				for x := max(0, lo[0]); x <= min(n-1, hi[0]+1); x++ {
					p := d.Point(x, y, z)
					t := 0.
					if den > 0 {
						t = math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/den))
					}
					rr := g.Vertices[node.Parent].Radius*(1-t) + node.Radius*t
					value := p.Sub(a.Add(ab.Mul(t))).Len() - rr
					idx := x + n*(y+n*z)
					v.Values[idx] = math.Min(v.Values[idx], value)
				}
			}
		}
	}
	applyConstraints(v)
	return v, nil
}
func boxSigned(p Vec3, b Box) float64 {
	c := b.Min.Add(b.Max).Mul(.5)
	h := b.Max.Sub(b.Min).Mul(.5)
	q := p.Sub(c)
	q = Vec3{math.Abs(q.X) - h.X, math.Abs(q.Y) - h.Y, math.Abs(q.Z) - h.Z}
	return Vec3{math.Max(q.X, 0), math.Max(q.Y, 0), math.Max(q.Z, 0)}.Len() + math.Min(math.Max(q.X, math.Max(q.Y, q.Z)), 0)
}
func applyConstraints(v *VolumeGrid) {
	d := v.Space
	n := d.Resolution
	for z := 0; z < n; z++ {
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				i := x + n*(y+n*z)
				p := d.Point(x, y, z)
				for _, b := range d.Exclusions {
					v.Values[i] = math.Max(v.Values[i], -boxSigned(p, b))
				}
				if x == 0 || y == 0 || z == 0 || x == n-1 || y == n-1 || z == n-1 {
					v.Values[i] = math.Max(v.Values[i], 1e-6)
				}
			}
		}
	}
}

type Porous struct{ Amount, Frequency float64 }

func (p Porous) Modify(ctx context.Context, source Volume) (*VolumeGrid, error) {
	if p.Amount < 0 || p.Amount > 1 || p.Frequency <= 0 {
		return nil, fmt.Errorf("invalid pore parameters")
	}
	d := source.Domain()
	n := d.Resolution
	v := &VolumeGrid{d, make([]float64, n*n*n)}
	for z := 0; z < n; z++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				pos := d.Point(x, y, z)
				value := source.SignedValue(pos)
				if p.Amount > 0 {
					q := pos.Mul(p.Frequency * math.Pi)
					gyroid := math.Sin(q.X)*math.Cos(q.Y) + math.Sin(q.Y)*math.Cos(q.Z) + math.Sin(q.Z)*math.Cos(q.X)
					value = math.Max(value, (p.Amount*1.4-math.Abs(gyroid))/(p.Frequency*math.Pi))
				}
				v.Values[x+n*(y+n*z)] = value
			}
		}
	}
	return v, nil
}
