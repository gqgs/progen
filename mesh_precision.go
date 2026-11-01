package progen

import (
	"fmt"
)

// repairGLBCollapse changes a canonical surface only when float32 positions
// collapse a face. It never discards a distinct-position sliver: those require
// remeshing, not a larger area tolerance. The original mesh is not mutated.
func repairGLBCollapse(m *Mesh, d Domain) (*Mesh, error) {
	collapsed := false
	for i := 0; i < len(m.Indices); i += 3 {
		a, b, c := glbPrecisionVec(m.Positions[m.Indices[i]]), glbPrecisionVec(m.Positions[m.Indices[i+1]]), glbPrecisionVec(m.Positions[m.Indices[i+2]])
		if a == b || b == c || c == a {
			collapsed = true
			break
		}
	}
	if !collapsed {
		return m, nil
	}
	fail := func(reason string) (*Mesh, error) { return nil, fmt.Errorf("%w: %s", ErrMeshPrecision, reason) }
	q := &Mesh{}
	vertices := map[Vec3]uint32{}
	remap := make([]uint32, len(m.Positions))
	for i, p := range m.Positions {
		p = glbPrecisionVec(p)
		if !p.Finite() {
			return fail("non-finite quantized vertex")
		}
		id, ok := vertices[p]
		if !ok {
			id = uint32(len(q.Positions))
			vertices[p] = id
			q.Positions = append(q.Positions, p)
			q.Normals = append(q.Normals, Vec3{})
		}
		remap[i] = id
	}
	for i := 0; i < len(m.Indices); i += 3 {
		a, b, c := remap[m.Indices[i]], remap[m.Indices[i+1]], remap[m.Indices[i+2]]
		if a == b || b == c || c == a {
			continue
		}
		normal := q.Positions[b].Sub(q.Positions[a]).Cross(q.Positions[c].Sub(q.Positions[a]))
		original := m.Positions[m.Indices[i+1]].Sub(m.Positions[m.Indices[i]]).Cross(m.Positions[m.Indices[i+2]].Sub(m.Positions[m.Indices[i]]))
		if normal.Len() < 1e-14 {
			return fail("distinct-position degenerate triangle")
		}
		if normal.Dot(original) <= 0 {
			return fail("triangle orientation changed")
		}
		q.Indices = append(q.Indices, a, b, c)
		for _, v := range []uint32{a, b, c} {
			q.Normals[v] = q.Normals[v].Add(normal)
		}
	}
	if err := validateClosedSurface(q); err != nil {
		return fail(err.Error())
	}
	// Compact unused vertices in original encounter order and compute normals from
	// the retained, quantized faces rather than from discarded tiny triangles.
	compact := &Mesh{}
	for i, n := range q.Normals {
		if n.Len() == 0 {
			remap[i] = ^uint32(0)
			continue
		}
		remap[i] = uint32(len(compact.Positions))
		compact.Positions = append(compact.Positions, q.Positions[i])
		// Face areas already passed the geometric threshold. Normalize any
		// nonzero sum without Vec3.Unit's unrelated small-vector cutoff.
		compact.Normals = append(compact.Normals, n.Mul(1/n.Len()))
	}
	for _, v := range q.Indices {
		if remap[v] == ^uint32(0) {
			return fail("undefined repaired vertex normal")
		}
		compact.Indices = append(compact.Indices, remap[v])
	}
	if err := ValidateMesh(compact, d); err != nil {
		return fail(err.Error())
	}
	return compact, nil
}

// validateClosedSurface requires an oriented closed 2-manifold: every edge has
// two opposite uses, and every vertex link is a single cycle (no pinched shells).
// This is a repair precondition, not a new restriction on unrepaired meshes.
func validateClosedSurface(m *Mesh) error {
	type edge struct{ a, b uint32 }
	type usage struct{ count, balance int }
	edges := map[edge]usage{}
	links := make([]map[uint32][]uint32, len(m.Positions))
	faces := map[[3]uint32]bool{}
	for i := 0; i < len(m.Indices); i += 3 {
		f := [3]uint32{m.Indices[i], m.Indices[i+1], m.Indices[i+2]}
		key := f
		if key[0] > key[1] {
			key[0], key[1] = key[1], key[0]
		}
		if key[1] > key[2] {
			key[1], key[2] = key[2], key[1]
		}
		if key[0] > key[1] {
			key[0], key[1] = key[1], key[0]
		}
		if key[0] == key[1] || key[1] == key[2] || faces[key] {
			return fmt.Errorf("duplicate or collapsed face")
		}
		faces[key] = true
		for k, v := range f {
			a, b := f[(k+1)%3], f[(k+2)%3]
			if links[v] == nil {
				links[v] = map[uint32][]uint32{}
			}
			links[v][a] = append(links[v][a], b)
			links[v][b] = append(links[v][b], a)
			sign := 1
			if a > b {
				a, b = b, a
				sign = -1
			}
			e := edge{a, b}
			u := edges[e]
			u.count++
			u.balance += sign
			edges[e] = u
		}
	}
	if len(faces) == 0 {
		return fmt.Errorf("empty repaired surface")
	}
	for _, u := range edges {
		if u.count != 2 || u.balance != 0 {
			return fmt.Errorf("surface has boundary, non-manifold or inconsistently oriented edge")
		}
	}
	for _, link := range links {
		if len(link) == 0 {
			continue
		}
		var start uint32
		for v, neighbors := range link {
			if len(neighbors) != 2 {
				return fmt.Errorf("non-manifold vertex link")
			}
			start = v
		}
		seen := map[uint32]bool{}
		queue := []uint32{start}
		for len(queue) > 0 {
			v := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			if seen[v] {
				continue
			}
			seen[v] = true
			queue = append(queue, link[v]...)
		}
		if len(seen) != len(link) {
			return fmt.Errorf("disconnected vertex link")
		}
	}
	return nil
}
