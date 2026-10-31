package progen

import (
	"bytes"
	"encoding/json"
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
