package progen

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// ErrMeshTriangleLimit is a candidate-local complexity rejection, not a fatal batch error.
var ErrMeshTriangleLimit = errors.New("mesh exceeds two million triangles")

// ErrMeshPrecision is a candidate-local rejection when export precision cannot
// be repaired without compromising the canonical surface.
var ErrMeshPrecision = errors.New("unsafe mesh at GLB precision")

const maxMeshTriangles = 2_000_000

type Tetrahedra struct{}

func (Tetrahedra) Mesh(ctx context.Context, v Volume) (*Mesh, error) {
	d := v.Domain()
	n := d.Resolution
	m := &Mesh{}
	edges := map[[2]int]uint32{}
	tets := [][4]int{{0, 1, 3, 7}, {0, 3, 2, 7}, {0, 2, 6, 7}, {0, 6, 4, 7}, {0, 4, 5, 7}, {0, 5, 1, 7}}
	for z := 0; z < n-1; z++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for y := 0; y < n-1; y++ {
			for x := 0; x < n-1; x++ {
				var ids [8]int
				var pts [8]Vec3
				var vals [8]float64
				for k := 0; k < 8; k++ {
					a, b, c := x+(k&1), y+((k>>1)&1), z+((k>>2)&1)
					ids[k] = a + n*(b+n*c)
					pts[k] = d.Point(a, b, c)
					vals[k] = v.SignedValue(pts[k])
					if !finite(vals[k]) {
						return nil, fmt.Errorf("non-finite volume")
					}
				}
				for _, tet := range tets {
					inside, outside := []int{}, []int{}
					for _, k := range tet {
						if vals[k] < 0 {
							inside = append(inside, k)
						} else {
							outside = append(outside, k)
						}
					}
					if len(inside) == 0 || len(inside) == 4 {
						continue
					}
					inCenter, outCenter := Vec3{}, Vec3{}
					for _, k := range inside {
						inCenter = inCenter.Add(pts[k])
					}
					for _, k := range outside {
						outCenter = outCenter.Add(pts[k])
					}
					outward := outCenter.Mul(1 / float64(len(outside))).Sub(inCenter.Mul(1 / float64(len(inside))))
					vertex := func(a, b int) uint32 {
						key := [2]int{ids[a], ids[b]}
						if vals[a] == 0 {
							key = [2]int{ids[a], ids[a]}
						} else if vals[b] == 0 {
							key = [2]int{ids[b], ids[b]}
						}
						if key[0] > key[1] {
							key[0], key[1] = key[1], key[0]
						}
						if q, ok := edges[key]; ok {
							return q
						}
						t := vals[a] / (vals[a] - vals[b])
						p := pts[a].Add(pts[b].Sub(pts[a]).Mul(t))
						q := uint32(len(m.Positions))
						edges[key] = q
						m.Positions = append(m.Positions, p)
						m.Normals = append(m.Normals, Vec3{})
						return q
					}
					triangle := func(a, b, c uint32) {
						normal := m.Positions[b].Sub(m.Positions[a]).Cross(m.Positions[c].Sub(m.Positions[a]))
						if normal.Len() < 1e-14 {
							return
						}
						if normal.Dot(outward) < 0 {
							b, c = c, b
							normal = normal.Mul(-1)
						}
						m.Indices = append(m.Indices, a, b, c)
						m.Normals[a] = m.Normals[a].Add(normal)
						m.Normals[b] = m.Normals[b].Add(normal)
						m.Normals[c] = m.Normals[c].Add(normal)
					}
					if len(inside) == 1 {
						a := inside[0]
						triangle(vertex(a, outside[0]), vertex(a, outside[1]), vertex(a, outside[2]))
					} else if len(inside) == 3 {
						a := outside[0]
						triangle(vertex(a, inside[0]), vertex(a, inside[1]), vertex(a, inside[2]))
					} else {
						a, b := inside[0], inside[1]
						c, e := outside[0], outside[1]
						ac, ae, bc, be := vertex(a, c), vertex(a, e), vertex(b, c), vertex(b, e)
						triangle(ac, ae, be)
						triangle(ac, be, bc)
					}
					// Stop as soon as the budget is exceeded instead of constructing
					// the rest of an already unusable mesh.
					if len(m.Indices) > maxMeshTriangles*3 {
						return nil, ErrMeshTriangleLimit
					}
				}
			}
		}
	}
	for i := range m.Normals {
		m.Normals[i] = m.Normals[i].Unit()
		if m.Normals[i].Len() == 0 {
			m.Normals[i] = Vec3{0, 1, 0}
		}
	}
	return repairGLBCollapse(m, d)
}
func ValidateMesh(m *Mesh, d Domain) error {
	if m == nil || len(m.Positions) == 0 || len(m.Indices) == 0 || len(m.Indices)%3 != 0 || len(m.Normals) != len(m.Positions) {
		return fmt.Errorf("empty or malformed mesh")
	}
	if err := validateMeshStructure(m, d); err != nil {
		return err
	}
	for i := 0; i < len(m.Indices); i += 3 {
		a, b, c := m.Indices[i], m.Indices[i+1], m.Indices[i+2]
		if m.Positions[b].Sub(m.Positions[a]).Cross(m.Positions[c].Sub(m.Positions[a])).Len() < 1e-14 {
			return fmt.Errorf("degenerate triangle")
		}
	}
	return nil
}

// Empty surfaces are legitimate generation results, but invalid topology is
// corrupt output and must never be reused from cache.
func validateMeshStructure(m *Mesh, d Domain) error {
	if m == nil || len(m.Indices)%3 != 0 || len(m.Normals) != len(m.Positions) {
		return fmt.Errorf("malformed mesh")
	}
	for i, p := range m.Positions {
		if !p.Finite() || !m.Normals[i].Finite() || !d.Box.Contains(p) {
			return fmt.Errorf("invalid mesh vertex %d", i)
		}
	}
	for i, index := range m.Indices {
		if uint64(index) >= uint64(len(m.Positions)) {
			return fmt.Errorf("mesh index out of bounds: triangle %d references vertex %d of %d", i/3, index, len(m.Positions))
		}
	}
	return nil
}

func EncodeGLB(m *Mesh) ([]byte, error) {
	if m == nil || len(m.Positions) == 0 || len(m.Indices) == 0 {
		return nil, fmt.Errorf("empty mesh")
	}
	var bin bytes.Buffer
	put := func(x float64) { binary.Write(&bin, binary.LittleEndian, float32(x)) }
	lo, hi := m.Positions[0], m.Positions[0]
	for _, p := range m.Positions {
		put(p.X)
		put(p.Y)
		put(p.Z)
		lo = Vec3{math.Min(lo.X, p.X), math.Min(lo.Y, p.Y), math.Min(lo.Z, p.Z)}
		hi = Vec3{math.Max(hi.X, p.X), math.Max(hi.Y, p.Y), math.Max(hi.Z, p.Z)}
	}
	normalOffset := bin.Len()
	for _, p := range m.Normals {
		put(p.X)
		put(p.Y)
		put(p.Z)
	}
	indexOffset := bin.Len()
	for _, i := range m.Indices {
		binary.Write(&bin, binary.LittleEndian, i)
	}
	doc := map[string]any{"asset": map[string]any{"version": "2.0", "generator": "progen/1"}, "scene": 0, "scenes": []any{map[string]any{"nodes": []int{0}}}, "nodes": []any{map[string]any{"mesh": 0}}, "meshes": []any{map[string]any{"primitives": []any{map[string]any{"attributes": map[string]int{"POSITION": 0, "NORMAL": 1}, "indices": 2, "material": 0}}}}, "materials": []any{map[string]any{"pbrMetallicRoughness": map[string]any{"baseColorFactor": []float64{.72, .79, .74, 1}, "metallicFactor": .05, "roughnessFactor": .65}}}, "buffers": []any{map[string]any{"byteLength": bin.Len()}}, "bufferViews": []any{map[string]any{"buffer": 0, "byteOffset": 0, "byteLength": normalOffset, "target": 34962}, map[string]any{"buffer": 0, "byteOffset": normalOffset, "byteLength": indexOffset - normalOffset, "target": 34962}, map[string]any{"buffer": 0, "byteOffset": indexOffset, "byteLength": bin.Len() - indexOffset, "target": 34963}}, "accessors": []any{map[string]any{"bufferView": 0, "componentType": 5126, "count": len(m.Positions), "type": "VEC3", "min": []float32{float32(lo.X), float32(lo.Y), float32(lo.Z)}, "max": []float32{float32(hi.X), float32(hi.Y), float32(hi.Z)}}, map[string]any{"bufferView": 1, "componentType": 5126, "count": len(m.Normals), "type": "VEC3"}, map[string]any{"bufferView": 2, "componentType": 5125, "count": len(m.Indices), "type": "SCALAR"}}}
	j, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	for len(j)%4 != 0 {
		j = append(j, ' ')
	}
	var out bytes.Buffer
	for _, x := range []uint32{0x46546c67, 2, uint32(12 + 8 + len(j) + 8 + bin.Len()), uint32(len(j)), 0x4e4f534a} {
		binary.Write(&out, binary.LittleEndian, x)
	}
	out.Write(j)
	binary.Write(&out, binary.LittleEndian, uint32(bin.Len()))
	binary.Write(&out, binary.LittleEndian, uint32(0x004e4942))
	out.Write(bin.Bytes())
	return out.Bytes(), nil
}
