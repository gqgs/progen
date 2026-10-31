package progen

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// ImportTransform explicitly converts source glTF meters to the normalized
// right-handed Y-up meter frame. Node transforms are applied before this map.
type ImportTransform struct {
	Scale       float64 `json:"scale"`
	Translation Vec3    `json:"translation"`
}

const MaxArchetypeGLBBytes = 8 << 20

// DecodeArchetypeGLB imports the deliberately small static glTF 2.0 subset used
// for archetypes. Each welded connected shell must be closed, outward oriented
// and convex. Multiple shells are interpreted as a union, including overlapping
// shells. Concave shells must be decomposed into convex closed parts upstream.
// Returned samples are min/max plane fields, negative inside, NOT exact SDFs.
// Thin parts can have no interior samples at the requested exploration grid.
func DecodeArchetypeGLB(ctx context.Context, data []byte, transform ImportTransform, domain Domain) (*Mesh, *VolumeGrid, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := domain.Validate(); err != nil {
		return nil, nil, err
	}
	if !finite(transform.Scale) || transform.Scale <= 0 || !transform.Translation.Finite() {
		return nil, nil, fmt.Errorf("archetype transform requires positive finite scale and finite translation")
	}
	doc, bin, err := parseArchetypeGLB(data)
	if err != nil {
		return nil, nil, err
	}
	m, err := doc.mesh(ctx, bin, transform, domain)
	if err != nil {
		return nil, nil, err
	}
	shells, err := convexArchetypeShells(ctx, m, domain)
	if err != nil {
		return nil, nil, err
	}
	n := domain.Resolution
	v := &VolumeGrid{Space: domain, Values: make([]float64, n*n*n)}
	occupied := 0
	for z := 0; z < n; z++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				p := domain.Point(x, y, z)
				value := math.Inf(1)
				for _, shell := range shells {
					signed := math.Inf(-1)
					for _, plane := range shell {
						signed = math.Max(signed, plane.normal.Dot(p)-plane.offset)
					}
					value = math.Min(value, signed)
				}
				v.Values[x+n*(y+n*z)] = value
				if value < 0 {
					occupied++
				}
			}
		}
	}
	if occupied == 0 {
		return nil, nil, fmt.Errorf("archetype has no interior grid samples; thin features need a finer resolution")
	}
	return m, v, nil
}

type archetypeDoc struct {
	Asset struct {
		Version    string `json:"version"`
		MinVersion string `json:"minVersion"`
	} `json:"asset"`
	Scene  *int `json:"scene"`
	Scenes []struct {
		Nodes []int `json:"nodes"`
	} `json:"scenes"`
	Nodes []struct {
		Mesh        *int      `json:"mesh"`
		Children    []int     `json:"children"`
		Matrix      []float64 `json:"matrix"`
		Translation []float64 `json:"translation"`
		Rotation    []float64 `json:"rotation"`
		Scale       []float64 `json:"scale"`
		Skin        *int      `json:"skin"`
		Weights     []float64 `json:"weights"`
	} `json:"nodes"`
	Meshes []struct {
		Primitives []struct {
			Attributes map[string]int    `json:"attributes"`
			Indices    *int              `json:"indices"`
			Mode       *int              `json:"mode"`
			Targets    []json.RawMessage `json:"targets"`
		} `json:"primitives"`
		Weights []float64 `json:"weights"`
	} `json:"meshes"`
	Buffers []struct {
		ByteLength int    `json:"byteLength"`
		URI        string `json:"uri"`
	} `json:"buffers"`
	BufferViews []struct {
		Buffer     int `json:"buffer"`
		ByteOffset int `json:"byteOffset"`
		ByteLength int `json:"byteLength"`
		ByteStride int `json:"byteStride"`
	} `json:"bufferViews"`
	Accessors []struct {
		BufferView    *int            `json:"bufferView"`
		ByteOffset    int             `json:"byteOffset"`
		ComponentType int             `json:"componentType"`
		Count         int             `json:"count"`
		Type          string          `json:"type"`
		Normalized    bool            `json:"normalized"`
		Sparse        json.RawMessage `json:"sparse"`
	} `json:"accessors"`
	Animations []json.RawMessage `json:"animations"`
	Skins      []json.RawMessage `json:"skins"`
}

func parseArchetypeGLB(data []byte) (archetypeDoc, []byte, error) {
	var doc archetypeDoc
	fail := func(s string) (archetypeDoc, []byte, error) {
		return doc, nil, fmt.Errorf("unsupported archetype GLB: %s", s)
	}
	if len(data) < 28 || len(data) > MaxArchetypeGLBBytes {
		return fail("size must be 28 bytes..8 MiB")
	}
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(data[i : i+4]) }
	if u32(0) != 0x46546c67 || u32(4) != 2 || uint64(u32(8)) != uint64(len(data)) {
		return fail("invalid glTF 2.0 header or length")
	}
	pos := 12
	var raw, bin []byte
	for pos < len(data) {
		if len(data)-pos < 8 {
			return fail("truncated chunk header")
		}
		length, kind := u32(pos), u32(pos+4)
		pos += 8
		// Check the unsigned chunk length before narrowing it to int. A corrupt
		// uint32 length must not become negative on 32-bit hosts.
		if length%4 != 0 || uint64(length) > uint64(len(data)-pos) {
			return fail("invalid chunk length")
		}
		n := int(length)
		switch kind {
		case 0x4e4f534a:
			if raw != nil || pos != 20 {
				return fail("JSON must be the first and only JSON chunk")
			}
			raw = data[pos : pos+n]
		case 0x004e4942:
			if raw == nil || bin != nil {
				return fail("requires one embedded BIN after JSON")
			}
			bin = data[pos : pos+n]
		default:
			return fail("unknown chunk type")
		}
		pos += n
	}
	if raw == nil || bin == nil {
		return fail("requires JSON and embedded BIN")
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fail("invalid JSON: " + err.Error())
	}
	// Inspect unsupported geometry-changing metadata at every nested object,
	// rather than silently ignoring an extension on an accessor or primitive.
	var generic any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&generic); err != nil {
		return fail("invalid JSON")
	}
	var inspect func(any) error
	inspect = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				if key == "extensions" || key == "extensionsUsed" || key == "extensionsRequired" {
					if value != nil && fmt.Sprint(value) != "map[]" && fmt.Sprint(value) != "[]" {
						return fmt.Errorf("extensions are unsupported")
					}
				}
				if key == "uri" {
					if s, ok := value.(string); ok && s != "" {
						return fmt.Errorf("external/data URIs are unsupported")
					}
				}
				if err := inspect(value); err != nil {
					return err
				}
			}
		case []any:
			for _, value := range x {
				if err := inspect(value); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := inspect(generic); err != nil {
		return fail(err.Error())
	}
	if doc.Asset.Version != "2.0" || (doc.Asset.MinVersion != "" && doc.Asset.MinVersion != "2.0") {
		return fail("asset version must be 2.0")
	}
	if len(doc.Animations) > 0 || len(doc.Skins) > 0 {
		return fail("animations and skins are unsupported")
	}
	if len(doc.Buffers) != 1 || doc.Buffers[0].URI != "" || doc.Buffers[0].ByteLength <= 0 || doc.Buffers[0].ByteLength > len(bin) || len(bin)-doc.Buffers[0].ByteLength > 3 {
		return fail("requires one embedded buffer with valid length")
	}
	bin = bin[:doc.Buffers[0].ByteLength]
	if len(doc.Nodes) == 0 || len(doc.Nodes) > 256 || len(doc.Meshes) == 0 || len(doc.Meshes) > 64 || len(doc.Accessors) > 512 || len(doc.BufferViews) > 512 {
		return fail("node/mesh/accessor budget exceeded or missing geometry")
	}
	for _, n := range doc.Nodes {
		if n.Skin != nil || len(n.Weights) > 0 {
			return fail("skin and morph nodes are unsupported")
		}
	}
	for _, m := range doc.Meshes {
		if len(m.Weights) > 0 {
			return fail("morph meshes are unsupported")
		}
		for _, p := range m.Primitives {
			if len(p.Targets) > 0 {
				return fail("morph targets are unsupported")
			}
		}
	}
	for _, a := range doc.Accessors {
		if a.Sparse != nil && string(a.Sparse) != "null" {
			return fail("sparse accessors are unsupported")
		}
	}
	return doc, bin, nil
}

type importMatrix [16]float64

func identityImportMatrix() importMatrix {
	return importMatrix{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
}
func (a importMatrix) mul(b importMatrix) importMatrix {
	var c importMatrix
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			for k := 0; k < 4; k++ {
				c[col*4+row] += a[k*4+row] * b[col*4+k]
			}
		}
	}
	return c
}
func (a importMatrix) point(p Vec3) Vec3 {
	return Vec3{a[0]*p.X + a[4]*p.Y + a[8]*p.Z + a[12], a[1]*p.X + a[5]*p.Y + a[9]*p.Z + a[13], a[2]*p.X + a[6]*p.Y + a[10]*p.Z + a[14]}
}
func (a importMatrix) valid() bool {
	for _, v := range a {
		if !finite(v) {
			return false
		}
	}
	det := a[0]*(a[5]*a[10]-a[9]*a[6]) - a[4]*(a[1]*a[10]-a[9]*a[2]) + a[8]*(a[1]*a[6]-a[5]*a[2])
	return a[3] == 0 && a[7] == 0 && a[11] == 0 && a[15] == 1 && finite(det) && det > 1e-15
}

func (d archetypeDoc) accessor(bin []byte, index int, kind string) ([][]float64, error) {
	if index < 0 || index >= len(d.Accessors) {
		return nil, fmt.Errorf("accessor index out of bounds")
	}
	a := d.Accessors[index]
	if a.BufferView == nil || *a.BufferView < 0 || *a.BufferView >= len(d.BufferViews) || a.ByteOffset < 0 || a.Normalized || a.Type != kind || a.Count <= 0 || a.Count > 24576 {
		return nil, fmt.Errorf("invalid or unsupported accessor")
	}
	w := d.BufferViews[*a.BufferView]
	if w.Buffer != 0 || w.ByteOffset < 0 || w.ByteLength <= 0 || w.ByteOffset > len(bin) || w.ByteLength > len(bin)-w.ByteOffset {
		return nil, fmt.Errorf("invalid buffer view range")
	}
	components, bytesPer := 1, 0
	if kind == "VEC3" {
		components = 3
		if a.ComponentType != 5126 {
			return nil, fmt.Errorf("POSITION must use FLOAT VEC3")
		}
	}
	switch a.ComponentType {
	case 5121:
		bytesPer = 1
	case 5123:
		bytesPer = 2
	case 5125, 5126:
		bytesPer = 4
	default:
		return nil, fmt.Errorf("unsupported component type")
	}
	if kind == "SCALAR" && a.ComponentType == 5126 {
		return nil, fmt.Errorf("indices must use unsigned integer components")
	}
	stride := components * bytesPer
	if w.ByteStride != 0 {
		if kind == "SCALAR" || w.ByteStride < stride || w.ByteStride > 252 || w.ByteStride%4 != 0 {
			return nil, fmt.Errorf("invalid accessor stride")
		}
		stride = w.ByteStride
	}
	if a.ByteOffset%bytesPer != 0 || (w.ByteOffset+a.ByteOffset)%bytesPer != 0 || a.ByteOffset > w.ByteLength || a.Count > 1+(w.ByteLength-a.ByteOffset-components*bytesPer)/stride || a.ByteOffset+(a.Count-1)*stride+components*bytesPer > w.ByteLength {
		return nil, fmt.Errorf("accessor exceeds buffer view or is misaligned")
	}
	values := make([][]float64, a.Count)
	for i := range values {
		values[i] = make([]float64, components)
		for c := 0; c < components; c++ {
			offset := w.ByteOffset + a.ByteOffset + i*stride + c*bytesPer
			var value float64
			switch a.ComponentType {
			case 5121:
				value = float64(bin[offset])
			case 5123:
				value = float64(binary.LittleEndian.Uint16(bin[offset:]))
			case 5125:
				value = float64(binary.LittleEndian.Uint32(bin[offset:]))
			case 5126:
				value = float64(math.Float32frombits(binary.LittleEndian.Uint32(bin[offset:])))
			}
			if !finite(value) {
				return nil, fmt.Errorf("non-finite accessor value")
			}
			values[i][c] = value
		}
	}
	return values, nil
}

func (d archetypeDoc) mesh(ctx context.Context, bin []byte, normalization ImportTransform, domain Domain) (*Mesh, error) {
	if len(d.Scenes) != 1 {
		return nil, fmt.Errorf("archetype requires exactly one scene")
	}
	if d.Scene != nil && *d.Scene != 0 {
		return nil, fmt.Errorf("invalid default scene")
	}
	m := &Mesh{}
	seen := map[int]bool{}
	var visit func(int, importMatrix) error
	visit = func(index int, parent importMatrix) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if index < 0 || index >= len(d.Nodes) || seen[index] {
			return fmt.Errorf("invalid node index, cycle or multiply-parented node")
		}
		seen[index] = true
		n := d.Nodes[index]
		local := identityImportMatrix()
		if len(n.Matrix) > 0 {
			if len(n.Matrix) != 16 || len(n.Translation) > 0 || len(n.Scale) > 0 || len(n.Rotation) > 0 {
				return fmt.Errorf("invalid matrix/TRS node transform")
			}
			copy(local[:], n.Matrix)
		} else {
			translation, scale := Vec3{}, Vec3{1, 1, 1}
			if len(n.Translation) > 0 {
				if len(n.Translation) != 3 {
					return fmt.Errorf("invalid translation")
				}
				translation = Vec3{n.Translation[0], n.Translation[1], n.Translation[2]}
			}
			if len(n.Scale) > 0 {
				if len(n.Scale) != 3 {
					return fmt.Errorf("invalid node scale")
				}
				scale = Vec3{n.Scale[0], n.Scale[1], n.Scale[2]}
				if scale.X <= 0 || scale.Y <= 0 || scale.Z <= 0 {
					return fmt.Errorf("node scales must be positive")
				}
			}
			if len(n.Rotation) > 0 {
				if len(n.Rotation) != 4 {
					return fmt.Errorf("invalid quaternion")
				}
				x, y, z, w := n.Rotation[0], n.Rotation[1], n.Rotation[2], n.Rotation[3]
				if math.Abs(x*x+y*y+z*z+w*w-1) > 1e-6 {
					return fmt.Errorf("node quaternion must be normalized")
				}
				local = importMatrix{1 - 2*(y*y+z*z), 2 * (x*y + z*w), 2 * (x*z - y*w), 0, 2 * (x*y - z*w), 1 - 2*(x*x+z*z), 2 * (y*z + x*w), 0, 2 * (x*z + y*w), 2 * (y*z - x*w), 1 - 2*(x*x+y*y), 0, 0, 0, 0, 1}
			}
			for row := 0; row < 3; row++ {
				local[row] *= scale.X
				local[4+row] *= scale.Y
				local[8+row] *= scale.Z
			}
			local[12], local[13], local[14] = translation.X, translation.Y, translation.Z
		}
		world := parent.mul(local)
		if !local.valid() || !world.valid() {
			return fmt.Errorf("node transform must be finite, nonsingular, affine and orientation preserving")
		}
		if n.Mesh != nil {
			if *n.Mesh < 0 || *n.Mesh >= len(d.Meshes) {
				return fmt.Errorf("mesh index out of bounds")
			}
			for _, primitive := range d.Meshes[*n.Mesh].Primitives {
				position, ok := primitive.Attributes["POSITION"]
				if !ok || primitive.Indices == nil || (primitive.Mode != nil && *primitive.Mode != 4) {
					return fmt.Errorf("requires indexed TRIANGLES with POSITION")
				}
				for name := range primitive.Attributes {
					if name != "POSITION" && name != "NORMAL" && name != "TANGENT" && name != "TEXCOORD_0" && name != "TEXCOORD_1" && name != "COLOR_0" {
						return fmt.Errorf("unsupported vertex attribute %s", name)
					}
				}
				positions, err := d.accessor(bin, position, "VEC3")
				if err != nil {
					return err
				}
				indices, err := d.accessor(bin, *primitive.Indices, "SCALAR")
				if err != nil {
					return err
				}
				if len(indices)%3 != 0 || len(m.Positions)+len(positions) > 8192 || len(m.Indices)+len(indices) > 4096*3 {
					return fmt.Errorf("archetype exceeds 8192 vertex / 4096 triangle budget or malformed triangles")
				}
				base := uint32(len(m.Positions))
				for _, p := range positions {
					q := world.point(Vec3{p[0], p[1], p[2]}).Mul(normalization.Scale).Add(normalization.Translation)
					if !q.Finite() || !domain.Box.Contains(q) {
						return fmt.Errorf("normalized archetype vertex is non-finite or outside domain")
					}
					m.Positions = append(m.Positions, q)
					m.Normals = append(m.Normals, Vec3{})
				}
				for _, i := range indices {
					if i[0] >= float64(len(positions)) {
						return fmt.Errorf("triangle index out of bounds")
					}
					m.Indices = append(m.Indices, base+uint32(i[0]))
				}
			}
		}
		for _, child := range n.Children {
			if err := visit(child, world); err != nil {
				return err
			}
		}
		return nil
	}
	for _, root := range d.Scenes[0].Nodes {
		if err := visit(root, identityImportMatrix()); err != nil {
			return nil, err
		}
	}
	if len(m.Indices) == 0 {
		return nil, fmt.Errorf("scene contains no triangles")
	}
	for i := 0; i < len(m.Indices); i += 3 {
		a, b, c := m.Indices[i], m.Indices[i+1], m.Indices[i+2]
		normal := m.Positions[b].Sub(m.Positions[a]).Cross(m.Positions[c].Sub(m.Positions[a]))
		for _, id := range []uint32{a, b, c} {
			m.Normals[id] = m.Normals[id].Add(normal)
		}
	}
	for i := range m.Normals {
		length := m.Normals[i].Len()
		if !finite(length) {
			return nil, fmt.Errorf("normalized archetype normal magnitude overflows; reduce coordinate scale")
		}
		if length > 0 {
			m.Normals[i] = m.Normals[i].Mul(1 / length)
		} else {
			// An unreferenced vertex has no geometric normal. Keep the same
			// finite unit fallback as the built-in mesher for GLB compatibility.
			m.Normals[i] = Vec3{0, 1, 0}
		}
	}
	return m, nil
}

type archetypePlane struct {
	normal Vec3
	offset float64
}

func convexArchetypeShells(ctx context.Context, m *Mesh, d Domain) ([][]archetypePlane, error) {
	if err := ValidateMesh(m, d); err != nil {
		return nil, fmt.Errorf("archetype: %w", err)
	}
	// glTF commonly splits vertices at normal/UV seams. Exact positional welding
	// only; no tolerance-based repair changes the source geometry.
	weld := map[Vec3]int{}
	vertices := []Vec3{}
	ids := make([]int, len(m.Positions))
	for i, p := range m.Positions {
		id, ok := weld[p]
		if !ok {
			id = len(vertices)
			weld[p] = id
			vertices = append(vertices, p)
		}
		ids[i] = id
	}
	triangles := make([][3]int, len(m.Indices)/3)
	type edgeUse struct {
		tri      int
		from, to int
	}
	edges := map[[2]int][]edgeUse{}
	adj := make([][]int, len(triangles))
	for i := range triangles {
		if i%128 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		t := [3]int{ids[m.Indices[i*3]], ids[m.Indices[i*3+1]], ids[m.Indices[i*3+2]]}
		triangles[i] = t
		for k := 0; k < 3; k++ {
			a, b := t[k], t[(k+1)%3]
			key := [2]int{min(a, b), max(a, b)}
			edges[key] = append(edges[key], edgeUse{i, a, b})
		}
	}
	for _, uses := range edges {
		if len(uses) != 2 || uses[0].from != uses[1].to || uses[0].to != uses[1].from {
			return nil, fmt.Errorf("archetype requires closed consistently oriented manifold shells (open/nonmanifold edge)")
		}
		a, b := uses[0].tri, uses[1].tri
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
	}
	// Edge-map iteration must not choose the first representative of nearly
	// coplanar faces; stable traversal preserves exact sampled fingerprints.
	for _, neighbors := range adj {
		sort.Ints(neighbors)
	}
	visited := make([]bool, len(triangles))
	shells := [][]archetypePlane{}
	planeCount := 0
	tolerance := d.Box.Max.Sub(d.Box.Min).Len() * 1e-8
	for start := range triangles {
		if visited[start] {
			continue
		}
		queue := []int{start}
		visited[start] = true
		for head := 0; head < len(queue); head++ {
			for _, j := range adj[queue[head]] {
				if !visited[j] {
					visited[j] = true
					queue = append(queue, j)
				}
			}
		}
		if len(shells) >= 64 {
			return nil, fmt.Errorf("archetype exceeds 64 closed-shell budget")
		}
		points := map[int]bool{}
		for _, i := range queue {
			for _, v := range triangles[i] {
				points[v] = true
			}
		}
		// Euler=2 excludes handles and the vertex-link check excludes pinched
		// bowtie vertices even when every edge has two incident triangles.
		if len(points)-len(queue)/2 != 2 {
			return nil, fmt.Errorf("archetype shell must have sphere topology")
		}
		planes := []archetypePlane{}
		signedVolume := 0.
		origin := vertices[triangles[queue[0]][0]]
		for _, i := range queue {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			t := triangles[i]
			a, b, c := vertices[t[0]], vertices[t[1]], vertices[t[2]]
			normal := b.Sub(a).Cross(c.Sub(a))
			// ValidateMesh already rejects degenerate faces. Vec3.Unit's generic
			// near-zero cutoff is larger than that geometry threshold; applying
			// it here would turn valid thin boundary triangles into zero planes
			// and erase the entire shell's negative interior.
			normal = normal.Mul(1 / normal.Len())
			plane := archetypePlane{normal, normal.Dot(a)}
			for v := range points {
				if normal.Dot(vertices[v].Sub(a)) > tolerance {
					return nil, fmt.Errorf("archetype shell is concave, self-intersecting or inward oriented; decompose into outward convex closed parts")
				}
			}
			signedVolume += a.Sub(origin).Dot(b.Sub(origin).Cross(c.Sub(origin))) / 6
			duplicate := false
			for _, existing := range planes {
				if normal.Sub(existing.normal).Len() < 1e-8 && math.Abs(plane.offset-existing.offset) < tolerance {
					duplicate = true
					break
				}
			}
			if !duplicate {
				planes = append(planes, plane)
			}
		}
		if !finite(signedVolume) || signedVolume <= tolerance*tolerance*tolerance {
			return nil, fmt.Errorf("archetype shell has zero volume or inward orientation")
		}
		planeCount += len(planes)
		if planeCount > 512 {
			return nil, fmt.Errorf("archetype exceeds 512 convex face-plane budget")
		}
		// Every incident face around a vertex must form one edge-connected fan.
		for v := range points {
			incident := []int{}
			for _, i := range queue {
				t := triangles[i]
				if t[0] == v || t[1] == v || t[2] == v {
					incident = append(incident, i)
				}
			}
			fan := map[int]bool{incident[0]: true}
			work := []int{incident[0]}
			for head := 0; head < len(work); head++ {
				for _, j := range adj[work[head]] {
					t := triangles[j]
					if !fan[j] && (t[0] == v || t[1] == v || t[2] == v) {
						fan[j] = true
						work = append(work, j)
					}
				}
			}
			if len(fan) != len(incident) {
				return nil, fmt.Errorf("archetype has a nonmanifold vertex")
			}
		}
		shells = append(shells, planes)
	}
	return shells, nil
}
