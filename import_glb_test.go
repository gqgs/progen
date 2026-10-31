package progen

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func archetypeTestDomain() Domain {
	return Domain{Box: Box{Vec3{-1, -1, -1}, Vec3{1, 1, 1}}, Resolution: 32, Frame: "right-handed-y-up", Unit: "meters", Boundary: "dirichlet"}
}

func archetypeTestBox() *Mesh {
	m := &Mesh{Positions: []Vec3{{-.5, -.5, -.5}, {.5, -.5, -.5}, {.5, .5, -.5}, {-.5, .5, -.5}, {-.5, -.5, .5}, {.5, -.5, .5}, {.5, .5, .5}, {-.5, .5, .5}}, Indices: []uint32{0, 2, 1, 0, 3, 2, 4, 5, 6, 4, 6, 7, 0, 1, 5, 0, 5, 4, 3, 7, 6, 3, 6, 2, 0, 4, 7, 0, 7, 3, 1, 2, 6, 1, 6, 5}}
	m.Normals = make([]Vec3, len(m.Positions))
	for i := range m.Normals {
		m.Normals[i] = m.Positions[i].Unit()
	}
	return m
}

func archetypeTestGLB(t *testing.T, m *Mesh) []byte {
	t.Helper()
	b, err := EncodeGLB(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func alterArchetypeJSON(t *testing.T, data []byte, fn func(map[string]any)) []byte {
	t.Helper()
	n := int(binary.LittleEndian.Uint32(data[12:]))
	var doc map[string]any
	if err := json.Unmarshal(data[20:20+n], &doc); err != nil {
		t.Fatal(err)
	}
	fn(doc)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for len(raw)%4 != 0 {
		raw = append(raw, ' ')
	}
	var buf bytes.Buffer
	for _, v := range []uint32{0x46546c67, 2, uint32(20 + len(raw) + len(data) - (20 + n)), uint32(len(raw)), 0x4e4f534a} {
		if err := binary.Write(&buf, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	buf.Write(raw)
	buf.Write(data[20+n:])
	return buf.Bytes()
}

func TestArchetypeGLBConvexImport(t *testing.T) {
	d := archetypeTestDomain()
	d.Resolution = 32
	b := archetypeTestGLB(t, archetypeTestBox())
	m, v, err := DecodeArchetypeGLB(context.Background(), b, ImportTransform{Scale: 1}, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Positions) != 8 || len(m.Indices) != 36 {
		t.Fatalf("wrong mesh sizes: %+v", m)
	}
	for _, sample := range []struct {
		p      Vec3
		inside bool
	}{{Vec3{}, true}, {Vec3{.49, .2, -.3}, true}, {Vec3{.6, 0, 0}, false}, {Vec3{.9, .9, .9}, false}} {
		if got := v.SignedValue(sample.p) < 0; got != sample.inside {
			t.Fatalf("inside(%v)=%v", sample.p, got)
		}
	}
	_, again, err := DecodeArchetypeGLB(context.Background(), b, ImportTransform{Scale: 1}, d)
	if err != nil || !reflect.DeepEqual(v, again) {
		t.Fatalf("non deterministic import: %v", err)
	}
}

func TestArchetypeGLBTransforms(t *testing.T) {
	d := archetypeTestDomain()
	d.Resolution = 32
	b := archetypeTestGLB(t, archetypeTestBox())
	// Parent translation, child nonuniform scale and normalized quaternion, then
	// explicit source-unit normalization. This exercises glTF column-major TRS.
	b = alterArchetypeJSON(t, b, func(doc map[string]any) {
		doc["nodes"] = []any{map[string]any{"translation": []float64{.3, 0, 0}, "children": []int{1}}, map[string]any{"mesh": 0, "scale": []float64{.8, .4, .6}, "rotation": []float64{0, 0, math.Sqrt(.5), math.Sqrt(.5)}}}
	})
	m, v, err := DecodeArchetypeGLB(context.Background(), b, ImportTransform{Scale: 2, Translation: Vec3{-.6, .1, 0}}, d)
	if err != nil {
		t.Fatal(err)
	}
	if p := m.Positions[0]; p.Sub(Vec3{.4, -.7, -.6}).Len() > 1e-12 {
		t.Fatalf("unexpected transformed point %v", p)
	}
	if v.SignedValue(Vec3{0, .1, 0}) >= 0 || v.SignedValue(Vec3{.6, .1, 0}) < 0 {
		t.Fatal("normalization did not preserve expected interior")
	}
	b = archetypeTestGLB(t, archetypeTestBox())
	b = alterArchetypeJSON(t, b, func(doc map[string]any) {
		doc["nodes"] = []any{map[string]any{"mesh": 0, "matrix": []float64{.5, 0, 0, 0, 0, .5, 0, 0, 0, 0, .5, 0, .2, .1, 0, 1}}}
	})
	m, _, err = DecodeArchetypeGLB(context.Background(), b, ImportTransform{Scale: 1}, d)
	if err != nil {
		t.Fatal(err)
	}
	if m.Positions[0] != (Vec3{-.05, -.15, -.25}) {
		if m.Positions[0].Sub(Vec3{-.05, -.15, -.25}).Len() > 1e-12 {
			t.Fatalf("matrix transform failed %v", m.Positions[0])
		}
	}
}

func TestArchetypeGLBRejectUnsupported(t *testing.T) {
	d := archetypeTestDomain()
	d.Resolution = 16
	base := archetypeTestGLB(t, archetypeTestBox())
	tests := []struct {
		name, reason string
		alter        func(map[string]any)
	}{
		{"external", "URIs", func(doc map[string]any) { doc["buffers"].([]any)[0].(map[string]any)["uri"] = "source.bin" }},
		{"extension", "extensions", func(doc map[string]any) {
			doc["nodes"].([]any)[0].(map[string]any)["extensions"] = map[string]any{"UNKNOWN": map[string]any{}}
		}},
		{"skin", "skins", func(doc map[string]any) { doc["skins"] = []any{map[string]any{}} }},
		{"animation", "animations", func(doc map[string]any) { doc["animations"] = []any{map[string]any{}} }},
		{"morph", "morph", func(doc map[string]any) {
			doc["meshes"].([]any)[0].(map[string]any)["primitives"].([]any)[0].(map[string]any)["targets"] = []any{map[string]any{"POSITION": 0}}
		}},
		{"sparse", "sparse", func(doc map[string]any) { doc["accessors"].([]any)[0].(map[string]any)["sparse"] = map[string]any{} }},
		{"lines", "TRIANGLES", func(doc map[string]any) {
			doc["meshes"].([]any)[0].(map[string]any)["primitives"].([]any)[0].(map[string]any)["mode"] = 1
		}},
		{"cycle", "cycle", func(doc map[string]any) { doc["nodes"].([]any)[0].(map[string]any)["children"] = []int{0} }},
		{"reflection", "positive", func(doc map[string]any) { doc["nodes"].([]any)[0].(map[string]any)["scale"] = []int{-1, 1, 1} }},
		{"accessor-range", "accessor", func(doc map[string]any) { doc["accessors"].([]any)[0].(map[string]any)["byteOffset"] = 99999 }},
		{"view-range", "buffer view", func(doc map[string]any) { doc["bufferViews"].([]any)[0].(map[string]any)["byteLength"] = 99999 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := alterArchetypeJSON(t, base, tt.alter)
			_, _, err := DecodeArchetypeGLB(context.Background(), data, ImportTransform{Scale: 1}, d)
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("wanted %q, got %v", tt.reason, err)
			}
		})
	}
	for _, data := range [][]byte{nil, base[:20], append(append([]byte{}, base...), 0), make([]byte, MaxArchetypeGLBBytes+1)} {
		if _, _, err := DecodeArchetypeGLB(context.Background(), data, ImportTransform{Scale: 1}, d); err == nil {
			t.Fatal("accepted malformed GLB")
		}
	}
	for _, transform := range []ImportTransform{{}, {Scale: -1}, {Scale: math.Inf(1)}, {Scale: 1, Translation: Vec3{math.NaN(), 0, 0}}, {Scale: 3}} {
		if _, _, err := DecodeArchetypeGLB(context.Background(), base, transform, d); err == nil {
			t.Fatal("accepted invalid/out-of-domain transform")
		}
	}
}

func TestArchetypeGLBTopologyAndSeams(t *testing.T) {
	d := archetypeTestDomain()
	d.Resolution = 16
	t.Run("open", func(t *testing.T) {
		m := archetypeTestBox()
		m.Indices = m.Indices[3:]
		_, _, err := DecodeArchetypeGLB(context.Background(), archetypeTestGLB(t, m), ImportTransform{Scale: 1}, d)
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("accepted open mesh: %v", err)
		}
	})
	t.Run("nonmanifold", func(t *testing.T) {
		m := archetypeTestBox()
		m.Indices = append(m.Indices, m.Indices[:3]...)
		_, _, err := DecodeArchetypeGLB(context.Background(), archetypeTestGLB(t, m), ImportTransform{Scale: 1}, d)
		if err == nil || !strings.Contains(err.Error(), "manifold") {
			t.Fatalf("accepted nonmanifold mesh: %v", err)
		}
	})
	t.Run("inward", func(t *testing.T) {
		m := archetypeTestBox()
		for i := 0; i < len(m.Indices); i += 3 {
			m.Indices[i+1], m.Indices[i+2] = m.Indices[i+2], m.Indices[i+1]
		}
		_, _, err := DecodeArchetypeGLB(context.Background(), archetypeTestGLB(t, m), ImportTransform{Scale: 1}, d)
		if err == nil || !strings.Contains(err.Error(), "inward") {
			t.Fatalf("accepted inward mesh: %v", err)
		}
	})
	t.Run("concave", func(t *testing.T) {
		m := archetypeTestBox()
		m.Positions[6] = Vec3{}
		_, _, err := DecodeArchetypeGLB(context.Background(), archetypeTestGLB(t, m), ImportTransform{Scale: 1}, d)
		if err == nil || !strings.Contains(err.Error(), "concave") {
			t.Fatalf("accepted concave mesh: %v", err)
		}
	})
	t.Run("split normals", func(t *testing.T) {
		src := archetypeTestBox()
		m := &Mesh{}
		for _, i := range src.Indices {
			m.Indices = append(m.Indices, uint32(len(m.Positions)))
			m.Positions = append(m.Positions, src.Positions[i])
			m.Normals = append(m.Normals, src.Normals[i])
		}
		if _, _, err := DecodeArchetypeGLB(context.Background(), archetypeTestGLB(t, m), ImportTransform{Scale: 1}, d); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("overlapping convex union", func(t *testing.T) {
		m := archetypeTestBox()
		second := archetypeTestBox()
		for _, p := range second.Positions {
			m.Positions = append(m.Positions, p.Add(Vec3{.2, .1, 0}))
		}
		m.Normals = append(m.Normals, second.Normals...)
		for _, i := range second.Indices {
			m.Indices = append(m.Indices, i+8)
		}
		_, v, err := DecodeArchetypeGLB(context.Background(), archetypeTestGLB(t, m), ImportTransform{Scale: 1}, d)
		if err != nil {
			t.Fatal(err)
		}
		if v.SignedValue(Vec3{.6, .2, 0}) >= 0 {
			t.Fatal("overlapping shell union lost solid")
		}
	})
}

func TestArchetypeGLBCancelAndThin(t *testing.T) {
	d := archetypeTestDomain()
	d.Resolution = 16
	base := archetypeTestGLB(t, archetypeTestBox())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := DecodeArchetypeGLB(ctx, base, ImportTransform{Scale: 1}, d); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	m := archetypeTestBox()
	for i := range m.Positions {
		m.Positions[i].Y *= .0001
	}
	if _, _, err := DecodeArchetypeGLB(context.Background(), archetypeTestGLB(t, m), ImportTransform{Scale: 1}, d); err == nil || !strings.Contains(err.Error(), "thin features") {
		t.Fatalf("thin mesh should diagnose coarse sampling: %v", err)
	}
}
