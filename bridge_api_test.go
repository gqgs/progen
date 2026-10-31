package progen

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBridgeImportAndReferenceAPI(t *testing.T) {
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(e)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sidecar := append(append([]byte{}, bridgeSidecar...), '\n')
	payload, _ := json.Marshal(map[string][]byte{"glb": bridgeGLB, "sidecar": sidecar})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/archetypes", bytes.NewReader(payload)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var r Recipe
	if err = json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(e.Workspace, "sources", r.Source.SidecarSHA256+".json"))
	if err != nil || !bytes.Equal(stored, sidecar) {
		t.Fatal("HTTP changed exact sidecar bytes", err)
	}
	request, _ := json.Marshal(r)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/reference", bytes.NewReader(request)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "positions") {
		t.Fatal(w.Code, w.Body.String())
	}
	for i := 0; i < 12; i++ {
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/recipe/randomize", bytes.NewReader(request)))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var randomized Recipe
		json.Unmarshal(w.Body.Bytes(), &randomized)
		if randomized.Source.GLBSHA256 != r.Source.GLBSHA256 || randomized.Source.SidecarSHA256 != r.Source.SidecarSHA256 {
			t.Fatal("randomizer changed sources")
		}
		if _, _, err = e.Registry.Compile(randomized); err != nil {
			t.Fatal("randomizer escaped allowed ranges", err)
		}
	}
}

func TestBridgeParameterContracts(t *testing.T) {
	for _, tc := range []struct {
		node, key string
		bounds    ParameterRange
	}{
		{"missing", "wall", ParameterRange{.05, .1}},
		{"cells", "unknown", ParameterRange{0, 1}},
		{"cells", "wall", ParameterRange{.01, 5}},
		{"cells", "cells", ParameterRange{12.1, 13}},
	} {
		r := BridgeRecipe()
		r.Source.Spec.ParameterBounds[tc.node] = map[string]ParameterRange{tc.key: tc.bounds}
		if _, _, err := Builtins().Compile(r); err == nil {
			t.Fatalf("invalid parameter bounds accepted: %+v", tc)
		}
	}
}

func TestBridgeRejectsSubstitutedMeshFromAdmission(t *testing.T) {
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = e.Registry.Register(Descriptor{ID: "extra_shell", Version: "1", Inputs: map[string]Port{"volume": {"volume", "solid"}}, Outputs: map[string]Port{"mesh": {"mesh", "surface"}}}, func(map[string]float64) (Operator, error) {
		return OperatorFunc(func(ctx context.Context, d Domain, _ uint64, inputs Inputs) (Outputs, error) {
			mesh, err := (Tetrahedra{}).Mesh(ctx, inputs["volume"].Volume)
			if err != nil {
				return nil, err
			}
			box := archetypeTestBox()
			offset := uint32(len(mesh.Positions))
			for i, p := range box.Positions {
				mesh.Positions = append(mesh.Positions, Vec3{p.X * 8, p.Y*2.8 + .8, p.Z * 3.6})
				mesh.Normals = append(mesh.Normals, box.Normals[i])
			}
			for _, index := range box.Indices {
				mesh.Indices = append(mesh.Indices, index+offset)
			}
			return Outputs{"mesh": {Kind: "mesh", Mesh: mesh}}, nil
		}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := BridgeRecipe()
	for i, n := range r.Nodes {
		if n.ID == "surface" {
			r.Nodes[i].Operator = "extra_shell"
		}
	}
	c, err := e.Evaluate(context.Background(), r, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Valid || !strings.Contains(strings.Join(c.Problems, ";"), "does not match the canonical") {
		t.Fatal("substituted enclosing shell admitted", c.Problems)
	}
	if err = e.Feedback(c.ID, "favorite"); err != nil {
		t.Fatal(err)
	}
	a, err := e.Archive(r)
	if err != nil || len(a.Cells) > 0 {
		t.Fatal("kept invalid candidate admitted", err)
	}
	if err = e.Export(context.Background(), c.ID, io.Discard); err == nil {
		t.Fatal("invalid source bundle exported")
	}
	s, err := NewServer(e)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, file := range []string{"asset.glb", "export.zip"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/candidates/"+c.ID+"/"+file, nil))
		if w.Code != 404 {
			t.Fatal("invalid download admitted", file, w.Code)
		}
	}
}
